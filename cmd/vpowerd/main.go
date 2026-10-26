// Command vpowerd is a headless monitor for Voltronic-based UPS units, a
// replacement for the ViewPower monitor service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/damianogiusti/vpowerd/internal/api"
	"github.com/damianogiusti/vpowerd/internal/config"
	"github.com/damianogiusti/vpowerd/internal/monitor"
	"github.com/damianogiusti/vpowerd/internal/mqtt"
	"github.com/damianogiusti/vpowerd/internal/settings"
	"github.com/damianogiusti/vpowerd/internal/transport"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-healthcheck":
			os.Exit(healthcheck())
		case "-version", "--version":
			fmt.Println(version)
			return
		}
	}
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := settings.Open(filepath.Join(cfg.StateDir, "settings.json"), cfg.MQTT)
	if err != nil {
		return err
	}
	mqttCfg, mqttSource := store.MQTT()
	log.Info("starting vpowerd", "version", version, "device", cfg.Device, "mock", cfg.MockModel,
		"control", cfg.AllowControl, "mqtt", mqttCfg.Enabled, "mqtt_settings", mqttSource, "state_dir", cfg.StateDir)
	if cfg.AllowControl && cfg.APIToken == "" {
		log.Warn("control commands are enabled without VPOWERD_API_TOKEN: anyone on the network can cut the UPS output")
	}

	mon := monitor.New(opener(cfg, log), monitor.Options{
		PollInterval:  cfg.PollInterval,
		HistoryWindow: cfg.HistoryWindow,
		Logger:        log,
	})

	mqttManager := mqtt.NewManager(store, mon, cfg.DeviceID, cfg.AllowControl, log)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.NewHandler(mon, mqttManager, api.Options{AllowControl: cfg.AllowControl, APIToken: cfg.APIToken, Logger: log}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()
	mqttManager.Start(ctx)
	defer mqttManager.Stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("HTTP listening", "addr", listener.Addr().String())
		serveErr <- srv.Serve(listener)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stop()
		wg.Wait()
		return err
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("HTTP shutdown", "error", err)
	}
	wg.Wait()
	return nil
}

// opener picks the transport for each connection attempt.
func opener(cfg config.Config, log *slog.Logger) monitor.Opener {
	open := deviceOpener(cfg, log)
	if cfg.LogLevel > slog.LevelDebug {
		return open
	}
	return func() (transport.Transport, error) {
		t, err := open()
		if err != nil {
			return nil, err
		}
		return transport.WithLogging(t, log), nil
	}
}

func deviceOpener(cfg config.Config, log *slog.Logger) monitor.Opener {
	if cfg.MockModel != "" {
		return func() (transport.Transport, error) { return transport.NewMock(cfg.MockModel), nil }
	}
	return func() (transport.Transport, error) {
		path := cfg.Device
		if path == config.DeviceAuto {
			devices, err := transport.FindHIDRaw("/sys", "/dev", transport.KnownUPSIDs)
			if err != nil {
				return nil, err
			}
			if len(devices) == 0 {
				return nil, errors.New("no known UPS found under /sys/class/hidraw; set VPOWERD_DEVICE")
			}
			if len(devices) > 1 {
				log.Warn("several UPS found, using the first one", "devices", devices)
			}
			path = devices[0].Path
			log.Debug("auto-detected UPS", "path", path, "usb_id", devices[0].ID)
		}
		kind := cfg.Transport
		if kind == transport.KindAuto {
			kind = transport.DetectKind(path, "/sys")
		}
		log.Debug("opening device", "path", path, "transport", kind)
		if kind == transport.KindHIDRaw {
			return transport.OpenHIDRaw(path, transport.DefaultTiming)
		}
		return transport.OpenSerial(path, transport.DefaultTiming)
	}
}

// healthcheck probes /healthz for Docker HEALTHCHECK.
func healthcheck() int {
	cfg, err := config.Load(nil, os.Getenv)
	if err != nil {
		return 1
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return 1
	}
	return 0
}
