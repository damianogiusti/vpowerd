//go:build linux

package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ErrDeviceGone is returned when the kernel reports the device as removed.
var ErrDeviceGone = errors.New("transport: device disconnected")

// pollSlice bounds a single poll(2) so context cancellation is noticed quickly.
const pollSlice = 100 * time.Millisecond

// sendReplyWindow is how long Send waits for an unexpected reply to drain.
const sendReplyWindow = 300 * time.Millisecond

// frameWriter writes one command (CR included) using the transport framing.
type frameWriter func(fd int, payload []byte) error

// fdConn implements Transport over a non-blocking file descriptor.
// It is shared by the hidraw and serial transports, which only differ in how
// a command is framed on write.
type fdConn struct {
	mu     sync.Mutex
	fd     int
	closed bool
	name   string
	timing Timing
	write  frameWriter
}

func newFDConn(fd int, name string, timing Timing, write frameWriter) *fdConn {
	return &fdConn{fd: fd, name: name, timing: timing.withDefaults(), write: write}
}

func (c *fdConn) Name() string { return c.name }

func (c *fdConn) Query(ctx context.Context, cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", ErrClosed
	}
	if err := c.drain(); err != nil {
		return "", err
	}
	if err := c.write(c.fd, []byte(cmd+"\r")); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}
	return c.readReply(ctx, c.timing.FirstByte)
}

func (c *fdConn) Send(ctx context.Context, cmd string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := c.drain(); err != nil {
		return err
	}
	if err := c.write(c.fd, []byte(cmd+"\r")); err != nil {
		return fmt.Errorf("write %q: %w", cmd, err)
	}
	// Some firmwares echo or ACK commands that are documented as silent.
	if _, err := c.readReply(ctx, sendReplyWindow); err != nil && !errors.Is(err, ErrTimeout) {
		return err
	}
	return nil
}

func (c *fdConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return unix.Close(c.fd)
}

// readReply collects bytes until CR. HID reports are zero padded, so NUL
// bytes are skipped.
func (c *fdConn) readReply(ctx context.Context, firstByte time.Duration) (string, error) {
	deadline := time.Now().Add(c.timing.Total)
	reply := make([]byte, 0, 64)
	var chunk [64]byte
	started := false
	for {
		wait := firstByte
		if started {
			wait = c.timing.InterChunk
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", ErrTimeout
		}
		wait = min(wait, remaining)
		ready, err := c.waitReadable(ctx, wait)
		if err != nil {
			return "", err
		}
		if !ready {
			return "", ErrTimeout
		}
		n, err := unix.Read(c.fd, chunk[:])
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			return "", ErrDeviceGone
		}
		for _, b := range chunk[:n] {
			switch {
			case b == '\r':
				return string(reply), nil
			case b == 0:
				continue
			case len(reply) >= maxReplyLen:
				return "", fmt.Errorf("reply longer than %d bytes", maxReplyLen)
			default:
				reply = append(reply, b)
			}
		}
		started = true
	}
}

// waitReadable polls in short slices so ctx cancellation is honoured.
func (c *fdConn) waitReadable(ctx context.Context, d time.Duration) (bool, error) {
	deadline := time.Now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		slice := min(remaining, pollSlice)
		fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(slice.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("poll: %w", err)
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return false, ErrDeviceGone
		}
		return true, nil
	}
}

// drain discards stale bytes, e.g. a late reply to a timed out query.
func (c *fdConn) drain() error {
	var buf [64]byte
	for range 64 {
		fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}
		if n == 0 {
			return nil
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return ErrDeviceGone
		}
		if _, err := unix.Read(c.fd, buf[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("drain: %w", err)
		}
	}
	return nil
}
