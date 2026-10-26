package megatec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/damianogiusti/vpowerd/internal/transport"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

// ErrNAK is returned when the device refuses or does not know a command.
var ErrNAK = errors.New("megatec: command refused (NAK)")

// ErrNoUPS is returned when nothing on the transport speaks a known dialect.
var ErrNoUPS = errors.New("megatec: no supported UPS answered")

// UnsupportedError reports a device that answered, but with a dialect this
// daemon does not implement yet. The raw answers help adding support.
type UnsupportedError struct {
	Dialect  string
	MReply   string
	QPIReply string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("megatec: unsupported dialect %s (M=%q, QPI=%q)", e.Dialect, e.MReply, e.QPIReply)
}

// Attempt counts mirror the shipped code: up to 5 tries while detecting
// (PMV.matchProtocol) and up to 3 for a normal query (USBHandler.excuteCommand).
const (
	detectAttempts = 5
	queryAttempts  = 3
)

// Driver talks to one detected UPS.
type Driver struct {
	t         transport.Transport
	protocol  ups.Protocol
	statusCmd string
	charge    *ChargeEstimator
	smoother  chargeSmoother
	now       func() time.Time
}

// Detect identifies the dialect, following the order in which ViewPower
// registers its PMV processors (StartMain.initMoniterProcess).
func Detect(ctx context.Context, t transport.Transport) (*Driver, error) {
	m, err := queryUntilAnswer(ctx, t, "M", detectAttempts)
	if err != nil && !errors.Is(err, ErrNAK) {
		return nil, err
	}
	newDriver := func(p ups.Protocol, statusCmd string) *Driver {
		return &Driver{t: t, protocol: p, statusCmd: statusCmd, charge: NewChargeEstimator(), now: time.Now}
	}

	switch m {
	case "V":
		// PMV.matchProtocol rejects a "V" device that also answers QPI with
		// a "PI" id: it belongs to the P00..P99 family instead.
		if qpi, ok := answersQPI(ctx, t); ok {
			return nil, &UnsupportedError{Dialect: qpi, MReply: m, QPIReply: qpi}
		}
		return newDriver(ups.ProtocolPMV, "QS"), nil
	case "H":
		return newDriver(ups.ProtocolPMVH, "QS"), nil
	case "I":
		return newDriver(ups.ProtocolPMVI, "QS"), nil
	case "J":
		return newDriver(ups.ProtocolPMVJ, "QS"), nil
	case "P", "T":
		// PMV_Simplify: QS starts with '#' and has another layout.
		return nil, &UnsupportedError{Dialect: "PMV_Simplify", MReply: m}
	}

	if qpi, ok := answersQPI(ctx, t); ok {
		return nil, &UnsupportedError{Dialect: qpi, MReply: m, QPIReply: qpi}
	}
	// Not a ViewPower path: plain Megatec devices answer Q1 but not M.
	if reply, err := queryUntilAnswer(ctx, t, "Q1", queryAttempts); err == nil {
		if _, perr := ParseStatus(reply); perr == nil {
			return newDriver(ups.ProtocolMegatec, "Q1"), nil
		}
	}
	return nil, fmt.Errorf("%w (M=%q)", ErrNoUPS, m)
}

func answersQPI(ctx context.Context, t transport.Transport) (string, bool) {
	reply, err := queryUntilAnswer(ctx, t, "QPI", queryAttempts)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(reply, "(PI") {
		return strings.TrimPrefix(reply, "("), true
	}
	return "", false
}

// Protocol returns the detected dialect.
func (d *Driver) Protocol() ups.Protocol { return d.protocol }

// Identify reads the static data. Missing optional answers are not errors.
func (d *Driver) Identify(ctx context.Context) (ups.Identity, *ups.Rating, error) {
	id := ups.Identity{Protocol: d.protocol}

	var rating *ups.Rating
	if reply, err := d.query(ctx, "F"); err == nil {
		if r, perr := ParseRating(reply); perr == nil {
			rating = &r
		}
	} else if ctx.Err() != nil {
		return id, nil, ctx.Err()
	}

	if reply, err := d.query(ctx, "I"); err == nil {
		if info, perr := ParseInfo(reply); perr == nil {
			id.Model = info.Model
			id.RatedPower = info.RatedPower
		}
	}
	if d.protocol.ReportsCapacity() {
		if reply, err := d.query(ctx, "FW?"); err == nil {
			id.Firmware = strings.TrimSpace(reply)
		}
	}
	// PMVProcessor reads the serial number with QID and strips the '('.
	if reply, err := d.query(ctx, "QID"); err == nil {
		id.Serial = strings.TrimSpace(strings.TrimPrefix(reply, "("))
	}
	return id, rating, ctx.Err()
}

// Poll reads one status sample.
func (d *Driver) Poll(ctx context.Context) (ups.Status, error) {
	reply, err := d.query(ctx, d.statusCmd)
	if err != nil {
		return ups.Status{}, err
	}
	r, err := ParseStatus(reply)
	if err != nil {
		return ups.Status{}, err
	}
	now := d.now()
	s := ups.Status{
		Time:              now,
		Mode:              DeriveMode(r),
		InputVoltage:      r.InputVoltage,
		InputFaultVoltage: r.InputFaultVoltage,
		OutputVoltage:     r.OutputVoltage,
		OutputFrequency:   r.OutputFrequency,
		LoadPercent:       r.LoadPercent,
		BatteryVoltage:    r.BatteryVoltage,
		Temperature:       r.Temperature,
		Flags:             r.Flags,
	}

	if d.protocol.ReportsCapacity() {
		if qi, err := d.query(ctx, "QI"); err == nil {
			if c, perr := ParseCapacity(qi); perr == nil {
				s.BatteryCharge = c.Charge
				s.ChargeSource = ups.ChargeReported
				runtime := c.Runtime
				s.RuntimeRemaining = &runtime
				return s, nil
			}
		} else if ctx.Err() != nil {
			return ups.Status{}, ctx.Err()
		}
	}
	s.BatteryCharge = d.smoother.add(d.charge.Estimate(r.BatteryVoltage, s.Mode, r.LoadPercent, now))
	s.ChargeSource = ups.ChargeEstimated
	return s, nil
}

// Execute sends a control command.
func (d *Driver) Execute(ctx context.Context, cmd ups.Command) error {
	var wire string
	switch cmd.Kind {
	case ups.CmdBeeperToggle:
		wire = "Q"
	case ups.CmdTest10s:
		wire = "T"
	case ups.CmdTestCancel:
		wire = "CT"
	case ups.CmdShutdownCancel:
		wire = "C"
	case ups.CmdShutdown:
		s, err := FormatShutdown(cmd.ShutdownDelay, cmd.RestoreAfter)
		if err != nil {
			return err
		}
		wire = s
	default:
		return fmt.Errorf("megatec: unknown command %q", cmd.Kind)
	}
	return d.t.Send(ctx, wire)
}

// Raw sends a read-only query and returns the raw reply, for diagnostics.
func (d *Driver) Raw(ctx context.Context, cmd string) (string, error) {
	return d.t.Query(ctx, cmd)
}

// FormatShutdown builds "S<n>R<mmmm>".
//
// n is the delay in minutes: ".1".."..9" under one minute (6 s steps), "01".."99"
// otherwise, as in AbstractMonitorProcessor.getDelayShutdownTime.
// mmmm is the restore delay in minutes, 0000..9999. Whether 0000 means
// "restore immediately" or "never" depends on the firmware.
func FormatShutdown(delay, restore time.Duration) (string, error) {
	if delay < 6*time.Second || delay > 99*time.Minute {
		return "", fmt.Errorf("shutdown delay %s: want 6s..99m", delay)
	}
	if restore < 0 || restore > 9999*time.Minute {
		return "", fmt.Errorf("restore delay %s: want 0..9999m", restore)
	}
	var n string
	if delay < time.Minute {
		n = fmt.Sprintf(".%d", int(delay/(6*time.Second)))
	} else {
		n = fmt.Sprintf("%02d", int(math.Round(delay.Minutes())))
	}
	return fmt.Sprintf("S%sR%04d", n, int(restore/time.Minute)), nil
}

func (d *Driver) query(ctx context.Context, cmd string) (string, error) {
	return queryUntilAnswer(ctx, d.t, cmd, queryAttempts)
}

// queryUntilAnswer retries on timeout or NAK, like the shipped handler.
// An echo of the command is final: the firmware does not know it, and on
// the target unit (PMV) "I", "QID" and "QPI" are always echoed.
func queryUntilAnswer(ctx context.Context, t transport.Transport, cmd string, attempts int) (string, error) {
	var lastErr error
	for range attempts {
		reply, err := t.Query(ctx, cmd)
		switch {
		case err == nil && strings.TrimSpace(reply) == cmd:
			return "", fmt.Errorf("%s: %w (echoed)", cmd, ErrNAK)
		case err == nil && isNAK(cmd, reply):
			lastErr = ErrNAK
		case err == nil:
			return reply, nil
		case errors.Is(err, transport.ErrTimeout):
			lastErr = err
		default:
			return "", err
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("%s: %w", cmd, lastErr)
}

// isNAK also treats an echo of the command as a refusal: firmwares that do
// not know a command often send it back (see CommConnector.isSupportBatNum).
func isNAK(cmd, reply string) bool {
	r := strings.TrimSpace(reply)
	return r == "" || r == "(NAK" || r == "NAK" || r == cmd
}
