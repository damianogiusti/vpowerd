package transport

import (
	"context"
	"log/slog"
	"time"
)

// WithLogging logs every exchange at debug level. It is meant for the first
// run on a new device, where the raw replies are what matters.
func WithLogging(t Transport, log *slog.Logger) Transport {
	return &logged{inner: t, log: log}
}

type logged struct {
	inner Transport
	log   *slog.Logger
}

func (l *logged) Name() string { return l.inner.Name() }
func (l *logged) Close() error { return l.inner.Close() }

func (l *logged) Query(ctx context.Context, cmd string) (string, error) {
	start := time.Now()
	reply, err := l.inner.Query(ctx, cmd)
	l.log.Debug("query", "device", l.inner.Name(), "cmd", cmd, "reply", reply, "error", err, "took", time.Since(start))
	return reply, err
}

func (l *logged) Send(ctx context.Context, cmd string) error {
	err := l.inner.Send(ctx, cmd)
	l.log.Debug("send", "device", l.inner.Name(), "cmd", cmd, "error", err)
	return err
}
