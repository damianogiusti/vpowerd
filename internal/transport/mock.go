package transport

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Mock simulates a UPS for development and tests. The F, QI, I and FW?
// replies are the fixtures embedded in the shipped USBHandler test mode. Its
// QS fixture is not used here because it has the "UPS failed" bit set, which
// makes a confusing demo; the megatec tests still cover it.
type Mock struct {
	mu        sync.Mutex
	replies   map[string]string
	beeperOn  bool
	testUntil time.Time
	closed    bool
	now       func() time.Time
}

// NewMock returns a mock answering modelLetter ("V", "H", "I", "J") to "M".
func NewMock(modelLetter string) *Mock {
	return &Mock{
		now: time.Now,
		replies: map[string]string{
			"M":   modelLetter,
			"QS":  "(231.0 231.0 230.0 021 50.0 27.10 25.0 00001000",
			"Q1":  "(231.0 231.0 230.0 021 50.0 27.10 25.0 00001000",
			"F":   "(220.0 220.0 72.00 50.0",
			"QI":  "(99 7000 50.0 34 220 222 v1.00 0000100000000000",
			"I":   "#MMMMMMMMMMMMMMM 1500VA",
			"FW?": "Ver00.01",
			"QPI": "(NAK",
			"QID": "(NAK",
		},
	}
}

// SetReply overrides the reply for cmd. Mainly for tests.
func (m *Mock) SetReply(cmd, reply string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replies[cmd] = reply
}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Query(ctx context.Context, cmd string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", ErrClosed
	}
	reply, ok := m.replies[cmd]
	if !ok {
		return "(NAK", nil
	}
	if cmd == "QS" || cmd == "Q1" {
		reply = m.withDynamicBits(reply)
	}
	return reply, nil
}

func (m *Mock) Send(ctx context.Context, cmd string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	switch cmd {
	case "Q":
		m.beeperOn = !m.beeperOn
	case "T":
		m.testUntil = m.now().Add(10 * time.Second)
	case "CT":
		m.testUntil = time.Time{}
	}
	return nil
}

func (m *Mock) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// withDynamicBits patches the self-test (b2) and beeper (b0) status bits.
func (m *Mock) withDynamicBits(reply string) string {
	idx := strings.LastIndexByte(reply, ' ')
	if idx < 0 || len(reply)-idx-1 != 8 {
		return reply
	}
	bits := []byte(reply[idx+1:])
	bits[5] = boolBit(m.now().Before(m.testUntil))
	bits[7] = boolBit(m.beeperOn)
	return reply[:idx+1] + string(bits)
}

func boolBit(b bool) byte {
	if b {
		return '1'
	}
	return '0'
}
