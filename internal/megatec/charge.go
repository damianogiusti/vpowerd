package megatec

import (
	"time"

	"github.com/damianogiusti/vpowerd/internal/ups"
)

// ChargeEstimator ports PMVProcessor.calculateBatteryCapacity, used by the
// "V" dialect that does not report charge.
//
// The original keeps its state in static fields shared by every UPS; here it
// is per instance.
type ChargeEstimator struct {
	// current is the value that slowly climbs while the battery floats.
	current  int
	lastBump time.Time
}

// floatClimbInterval is how often the floating charge climbs by 1%.
const floatClimbInterval = 12 * time.Minute

// NewChargeEstimator returns an estimator in its initial state.
func NewChargeEstimator() *ChargeEstimator {
	return &ChargeEstimator{current: 90}
}

// Estimate returns the charge for a battery voltage as reported by the UPS.
func (e *ChargeEstimator) Estimate(battery ups.Volts, mode ups.WorkMode, load ups.Percent, now time.Time) ups.Percent {
	v := float64(normalizeTo12V(battery))
	switch mode {
	case ups.ModeLine, ups.ModeStandby:
		return clampPercent(e.onLine(v, now))
	case ups.ModeBattery, ups.ModeBatteryTest:
		e.current = 90
		e.lastBump = time.Time{}
		return clampPercent(onBattery(v, load))
	default:
		return 0
	}
}

func (e *ChargeEstimator) onLine(v float64, now time.Time) float64 {
	switch {
	case v < 11.6:
		return 0
	case v >= 13.5:
		return 100
	case v >= 13.3:
		if e.current >= 100 {
			return 100
		}
		if e.lastBump.IsZero() {
			e.lastBump = now
			return float64(e.current)
		}
		if now.Sub(e.lastBump) >= floatClimbInterval {
			e.lastBump = now
			e.current++
		}
		return float64(e.current)
	default:
		return 90 * (v - 11.6) / 1.7
	}
}

func onBattery(v float64, load ups.Percent) float64 {
	if load < 20 {
		switch {
		case v > 13.2:
			return 100
		case v > 10.2:
			return 100 * (v - 10.2) / 3.0
		default:
			return 0
		}
	}
	switch {
	case v > 12.7:
		return 100
	case v > 10.2:
		return 100 * (v - 10.2) / 2.5
	default:
		return 0
	}
}

// normalizeTo12V scales a battery string voltage to one 12 V block, as
// ViewPower does for 24, 36 and 48 V strings.
//
// Values under 3 V are treated as per-cell readings (6 cells per block).
// This branch is NOT in ViewPower, which would report 0% for them; some
// Megatec firmwares report per-cell voltage, so it is a deliberate addition.
func normalizeTo12V(v ups.Volts) ups.Volts {
	switch {
	case v < 3:
		return v * 6
	case v < 15:
		return v
	case v <= 30:
		return v / 2
	case v <= 45:
		return v / 3
	case v <= 60:
		return v / 4
	default:
		return v
	}
}

func clampPercent(v float64) ups.Percent {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		// ViewPower truncates with an int cast; keep the same rounding.
		return ups.Percent(int(v))
	}
}

// chargeWindow is the number of samples averaged, as in ViewPower.
const chargeWindow = 10

// chargeSmoother ports the moving average of WorkInfo.setBatteryCapacity.
//
// ViewPower averages the window before adding the new sample. On the user's
// unit its window was seen half full of zeros, showing 0% and then 40% while
// each sample was 100%. Here the window is seeded with the first sample and
// includes the current one, so there is no ramp from zero.
type chargeSmoother struct {
	window [chargeWindow]ups.Percent
	next   int
	seeded bool
}

func (s *chargeSmoother) add(v ups.Percent) ups.Percent {
	if !s.seeded {
		for i := range s.window {
			s.window[i] = v
		}
		s.seeded = true
		return v
	}
	s.window[s.next] = v
	s.next = (s.next + 1) % chargeWindow
	var sum ups.Percent
	for _, w := range s.window {
		sum += w
	}
	// Integer division, as in ViewPower.
	return ups.Percent(int(sum) / chargeWindow)
}
