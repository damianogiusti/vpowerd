package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/damianogiusti/vpowerd/internal/transport"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

func TestRingKeepsNewest(t *testing.T) {
	r := newRing[int](3)
	for i := range 5 {
		r.push(i)
	}
	got := r.snapshot()
	if len(got) != 3 || got[0] != 2 || got[2] != 4 {
		t.Errorf("got %v, want [2 3 4]", got)
	}
}

func TestDiffEvents(t *testing.T) {
	line := ups.Status{Time: time.Now()}
	battery := ups.Status{Time: time.Now(), Flags: ups.StatusFlags{UtilityFail: true, BatteryLow: true}}
	events := diffEvents(&line, battery)
	if len(events) != 2 || events[0].Kind != EventOnBattery || events[1].Kind != EventBatteryLow {
		t.Fatalf("got %+v", events)
	}
	events = diffEvents(&battery, line)
	if len(events) != 1 || events[0].Kind != EventPowerRestored {
		t.Fatalf("got %+v", events)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMonitorWithMock(t *testing.T) {
	mock := transport.NewMock("J")
	m := New(func() (transport.Transport, error) { return mock, nil }, Options{
		PollInterval: 20 * time.Millisecond,
		Logger:       quietLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitFor(t, func() bool { return m.Snapshot().Status != nil })
	snap := m.Snapshot()
	if !snap.Connected || snap.Identity.Protocol != ups.ProtocolPMVJ {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.Status.Flags.BeeperOn {
		t.Fatal("beeper should start off")
	}

	if err := m.Execute(ctx, ups.Command{Kind: ups.CmdBeeperToggle}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s := m.Snapshot().Status; return s != nil && s.Flags.BeeperOn })

	if len(m.History()) == 0 {
		t.Error("history is empty")
	}
}

func TestMonitorReportsOpenFailure(t *testing.T) {
	m := New(func() (transport.Transport, error) { return nil, errors.New("no device") }, Options{Logger: quietLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	waitFor(t, func() bool { return m.Snapshot().LastError != "" })
	if m.Snapshot().Connected {
		t.Error("should not be connected")
	}
	if err := m.Execute(ctx, ups.Command{Kind: ups.CmdTest10s}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("got %v, want ErrNotConnected", err)
	}
}
