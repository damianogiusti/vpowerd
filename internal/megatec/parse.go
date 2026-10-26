// Package megatec implements the Voltronic "PMV" family of the Megatec ASCII
// protocol, as used by ViewPower for USB line-interactive UPS units.
package megatec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/damianogiusti/vpowerd/internal/ups"
)

// StatusReading is the decoded QS/Q1 reply:
//
//	(MMM.M NNN.N PPP.P QQQ RR.R S.SS TT.T b7b6b5b4b3b2b1b0
//
// Field order matches PMVProcessor.queryWorkInfo in the shipped monitor.
type StatusReading struct {
	InputVoltage      ups.Volts
	InputFaultVoltage ups.Volts
	OutputVoltage     ups.Volts
	LoadPercent       ups.Percent
	OutputFrequency   ups.Hertz
	BatteryVoltage    ups.Volts
	Temperature       *ups.Celsius
	Flags             ups.StatusFlags
}

// ParseStatus decodes a QS or Q1 reply.
func ParseStatus(reply string) (StatusReading, error) {
	body, ok := strings.CutPrefix(strings.TrimSpace(reply), "(")
	if !ok {
		return StatusReading{}, fmt.Errorf("status reply %q: missing '('", reply)
	}
	fields := strings.Fields(body)
	if len(fields) != 8 {
		return StatusReading{}, fmt.Errorf("status reply %q: want 8 fields, got %d", reply, len(fields))
	}
	flags, err := parseFlags(fields[7])
	if err != nil {
		return StatusReading{}, fmt.Errorf("status reply %q: %w", reply, err)
	}
	var p numberParser
	r := StatusReading{
		InputVoltage:      ups.Volts(p.float(fields[0], "input voltage")),
		InputFaultVoltage: ups.Volts(p.float(fields[1], "input fault voltage")),
		OutputVoltage:     ups.Volts(p.float(fields[2], "output voltage")),
		LoadPercent:       ups.Percent(p.float(fields[3], "load")),
		OutputFrequency:   ups.Hertz(p.float(fields[4], "frequency")),
		BatteryVoltage:    ups.Volts(p.float(fields[5], "battery voltage")),
		Flags:             flags,
	}
	if p.err != nil {
		return StatusReading{}, fmt.Errorf("status reply %q: %w", reply, p.err)
	}
	// Some firmwares send "--.-" when there is no sensor; ViewPower ignores
	// this field entirely for PMV, so a missing value is not an error.
	if t, err := strconv.ParseFloat(fields[6], 64); err == nil {
		c := ups.Celsius(t)
		r.Temperature = &c
	}
	return r, nil
}

// parseFlags decodes "b7..b0". Bit meaning follows the Megatec spec and the
// character indexes used in PMVProcessor (charAt(0) = b7 ... charAt(7) = b0).
func parseFlags(bits string) (ups.StatusFlags, error) {
	if len(bits) != 8 || strings.Trim(bits, "01") != "" {
		return ups.StatusFlags{}, fmt.Errorf("status bits %q: want 8 binary digits", bits)
	}
	on := func(i int) bool { return bits[i] == '1' }
	return ups.StatusFlags{
		UtilityFail:     on(0),
		BatteryLow:      on(1),
		BypassBoost:     on(2),
		UPSFailed:       on(3),
		LineInteractive: on(4),
		SelfTest:        on(5),
		ShutdownActive:  on(6),
		BeeperOn:        on(7),
	}, nil
}

// ParseRating decodes the "F" reply: "#MMM.M QQQ SS.SS RR.R".
// The shipped test fixture starts with '(' instead of '#', so both are accepted.
func ParseRating(reply string) (ups.Rating, error) {
	body := strings.TrimSpace(reply)
	if len(body) == 0 || (body[0] != '#' && body[0] != '(') {
		return ups.Rating{}, fmt.Errorf("rating reply %q: missing '#'", reply)
	}
	fields := strings.Fields(body[1:])
	if len(fields) != 4 {
		return ups.Rating{}, fmt.Errorf("rating reply %q: want 4 fields, got %d", reply, len(fields))
	}
	var p numberParser
	r := ups.Rating{
		Voltage:        ups.Volts(p.float(fields[0], "voltage")),
		Current:        ups.Amperes(p.float(fields[1], "current")),
		BatteryVoltage: ups.Volts(p.float(fields[2], "battery voltage")),
		Frequency:      ups.Hertz(p.float(fields[3], "frequency")),
	}
	if p.err != nil {
		return ups.Rating{}, fmt.Errorf("rating reply %q: %w", reply, p.err)
	}
	return r, nil
}

// CapacityReading is the decoded "QI" reply of the H/I/J dialects.
type CapacityReading struct {
	Charge  ups.Percent
	Runtime ups.Seconds
}

// ParseCapacity decodes "(CC RRRR ...": charge percent and runtime in seconds.
// PMV_HProcessor reads fields 0 and 1 and divides the second by 60 to show
// minutes, which confirms the unit is seconds.
func ParseCapacity(reply string) (CapacityReading, error) {
	body, ok := strings.CutPrefix(strings.TrimSpace(reply), "(")
	if !ok {
		return CapacityReading{}, fmt.Errorf("QI reply %q: missing '('", reply)
	}
	fields := strings.Fields(body)
	if len(fields) < 2 {
		return CapacityReading{}, fmt.Errorf("QI reply %q: want at least 2 fields", reply)
	}
	charge, err1 := strconv.Atoi(fields[0])
	runtime, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return CapacityReading{}, fmt.Errorf("QI reply %q: non numeric charge or runtime", reply)
	}
	if charge < 0 || charge > 100 || runtime < 0 {
		return CapacityReading{}, fmt.Errorf("QI reply %q: value out of range", reply)
	}
	return CapacityReading{Charge: ups.Percent(charge), Runtime: ups.Seconds(runtime)}, nil
}

var ratedPowerRe = regexp.MustCompile(`(\d{2,5})\s*VA\b`)

// InfoReading is the decoded "I" reply.
type InfoReading struct {
	Model      string
	RatedPower *ups.VoltAmperes
}

// ParseInfo decodes the "I" reply. The layout differs between firmwares
// ("#Company Model Version" or "#MODEL 1500VA"), so it is kept as one string
// and only the VA rating is extracted.
func ParseInfo(reply string) (InfoReading, error) {
	body, ok := strings.CutPrefix(strings.TrimSpace(reply), "#")
	if !ok {
		return InfoReading{}, fmt.Errorf("info reply %q: missing '#'", reply)
	}
	info := InfoReading{Model: strings.Join(strings.Fields(body), " ")}
	if m := ratedPowerRe.FindStringSubmatch(body); m != nil {
		if va, err := strconv.Atoi(m[1]); err == nil {
			power := ups.VoltAmperes(va)
			info.RatedPower = &power
		}
	}
	return info, nil
}

// numberParser collects the first float parse error so callers can parse a
// whole reply and check once.
type numberParser struct{ err error }

func (p *numberParser) float(s, name string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("%s %q: not a number", name, s)
	}
	return v
}
