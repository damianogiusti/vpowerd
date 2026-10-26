package megatec

import "github.com/damianogiusti/vpowerd/internal/ups"

// standbyOutputThreshold is the output voltage under which ViewPower considers
// the output off.
const standbyOutputThreshold ups.Volts = 20

// DeriveMode ports the work mode decision of PMVProcessor.queryWorkInfo.
func DeriveMode(r StatusReading) ups.WorkMode {
	f := r.Flags
	mode := ups.ModeUnknown
	switch {
	case r.OutputVoltage < standbyOutputThreshold:
		if !f.UPSFailed {
			mode = ups.ModeStandby
		}
	case !f.UtilityFail && !f.SelfTest:
		mode = ups.ModeLine
	case !f.UtilityFail && f.SelfTest:
		mode = ups.ModeBatteryTest
	case f.UtilityFail:
		mode = ups.ModeBattery
	}
	// ViewPower overrides the mode with Fault at the end, whatever came before.
	if f.UPSFailed {
		mode = ups.ModeFault
	}
	return mode
}
