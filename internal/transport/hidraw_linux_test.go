//go:build linux

package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestHIDReportsFraming(t *testing.T) {
	reports := hidReports([]byte("QS\r"))
	if len(reports) != 1 {
		t.Fatalf("got %d reports", len(reports))
	}
	want := [9]byte{0, 'Q', 'S', '\r'}
	if reports[0] != want {
		t.Errorf("got %v, want %v", reports[0], want)
	}

	reports = hidReports([]byte("S.2R0001\r"))
	if len(reports) != 2 {
		t.Fatalf("got %d reports", len(reports))
	}
	if reports[1] != [9]byte{0, '\r'} {
		t.Errorf("second report %v", reports[1])
	}
}

// pipeConn wires an fdConn to a pipe: tests write the "device" side.
func pipeConn(t *testing.T, timing Timing) (*fdConn, int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	discard := func(int, []byte) error { return nil }
	c := newFDConn(p[0], "pipe", timing, discard)
	t.Cleanup(func() {
		_ = c.Close()
		_ = unix.Close(p[1])
	})
	return c, p[1]
}

func TestReadReplyAcrossZeroPaddedReports(t *testing.T) {
	c, dev := pipeConn(t, Timing{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = unix.Write(dev, []byte("(230.0 2"))
		_, _ = unix.Write(dev, []byte("30.0\r\x00\x00\x00"))
	}()
	got, err := c.Query(context.Background(), "QS")
	if err != nil {
		t.Fatal(err)
	}
	if got != "(230.0 230.0" {
		t.Errorf("got %q", got)
	}
}

func TestReadReplyTimesOut(t *testing.T) {
	c, _ := pipeConn(t, Timing{FirstByte: 50 * time.Millisecond, InterChunk: 50 * time.Millisecond, Total: 200 * time.Millisecond})
	start := time.Now()
	_, err := c.Query(context.Background(), "QS")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("timeout took %s", elapsed)
	}
}

func TestQueryDrainsStaleBytes(t *testing.T) {
	c, dev := pipeConn(t, Timing{})
	_, _ = unix.Write(dev, []byte("(STALE\r"))
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = unix.Write(dev, []byte("V\r"))
	}()
	got, err := c.Query(context.Background(), "M")
	if err != nil {
		t.Fatal(err)
	}
	if got != "V" {
		t.Errorf("got %q, want V", got)
	}
}

func TestQueryHonoursContext(t *testing.T) {
	c, _ := pipeConn(t, Timing{FirstByte: 5 * time.Second, Total: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Query(ctx, "QS")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestDeviceGoneOnHangup(t *testing.T) {
	c, dev := pipeConn(t, Timing{})
	_ = unix.Close(dev)
	_, err := c.Query(context.Background(), "QS")
	if !errors.Is(err, ErrDeviceGone) {
		t.Fatalf("got %v, want ErrDeviceGone", err)
	}
}
