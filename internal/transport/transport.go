// Package transport moves ASCII commands to the UPS and replies back.
//
// Every implementation is synchronous, serialised by its own mutex and bounded
// by a deadline: a silent device returns ErrTimeout, it never blocks forever.
package transport

import (
	"context"
	"errors"
	"time"
)

// ErrTimeout is returned when the device does not complete a reply in time.
var ErrTimeout = errors.New("transport: reply timeout")

// ErrClosed is returned after Close.
var ErrClosed = errors.New("transport: closed")

// Transport is a command/reply channel to one UPS.
type Transport interface {
	// Query sends cmd followed by CR and returns the reply without the
	// trailing CR.
	Query(ctx context.Context, cmd string) (string, error)
	// Send sends cmd followed by CR for commands with no reply ("T", "Q").
	// Any stray reply is drained so it cannot pollute the next Query.
	Send(ctx context.Context, cmd string) error
	// Name is a human readable identifier, e.g. "/dev/hidraw0".
	Name() string
	Close() error
}

// Timing controls the reply deadlines. Zero values are replaced by defaults.
type Timing struct {
	// FirstByte is how long to wait for the start of a reply.
	FirstByte time.Duration
	// InterChunk is the maximum gap between two chunks of the same reply.
	InterChunk time.Duration
	// Total caps a whole exchange.
	Total time.Duration
}

// DefaultTiming is derived from the shipped driver, which waits up to
// 4 x 400 ms for the first HID report and gives up on the first gap after
// data has started (libUSBDevice.so, ups_read).
var DefaultTiming = Timing{
	FirstByte:  1600 * time.Millisecond,
	InterChunk: 400 * time.Millisecond,
	Total:      3 * time.Second,
}

func (t Timing) withDefaults() Timing {
	if t.FirstByte <= 0 {
		t.FirstByte = DefaultTiming.FirstByte
	}
	if t.InterChunk <= 0 {
		t.InterChunk = DefaultTiming.InterChunk
	}
	if t.Total <= 0 {
		t.Total = DefaultTiming.Total
	}
	return t
}

// maxReplyLen bounds memory used by a misbehaving device. The shipped driver
// uses a 256 byte buffer; real replies are under 120 bytes.
const maxReplyLen = 256
