package megatec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damianogiusti/vpowerd/internal/transport"
	"github.com/damianogiusti/vpowerd/internal/ups"
)

// Fixture from the shipped USBHandler.initMap test mode.
const shippedQS = "(208.4 140.0 208.4 034 59.9 52.8 35.0 00110000"

func TestParseStatusShippedFixture(t *testing.T) {
	r, err := ParseStatus(shippedQS)
	if err != nil {
		t.Fatal(err)
	}
	if r.InputVoltage != 208.4 || r.InputFaultVoltage != 140.0 || r.OutputVoltage != 208.4 {
		t.Errorf("voltages: %+v", r)
	}
	if r.LoadPercent != 34 || r.OutputFrequency != 59.9 || r.BatteryVoltage != 52.8 {
		t.Errorf("load/freq/battery: %+v", r)
	}
	if r.Temperature == nil || *r.Temperature != 35.0 {
		t.Errorf("temperature: %v", r.Temperature)
	}
	want := ups.StatusFlags{BypassBoost: true, UPSFailed: true}
	if r.Flags != want {
		t.Errorf("flags = %+v, want %+v", r.Flags, want)
	}
}

func TestParseStatusRejectsGarbage(t *testing.T) {
	for _, reply := range []string{
		"",
		"(NAK",
		"QS",
		"(230.0 230.0 230.0 010 50.0 13.6 25.0", // 7 fields
		"(230.0 230.0 230.0 010 50.0 13.6 25.0 0000100",  // 7 bits
		"(230.0 230.0 230.0 010 50.0 13.6 25.0 00002000", // non binary
		"(230.0 230.0 abc.d 010 50.0 13.6 25.0 00001000", // non numeric
		"230.0 230.0 230.0 010 50.0 13.6 25.0 00001000",  // no '('
	} {
		if _, err := ParseStatus(reply); err == nil {
			t.Errorf("ParseStatus(%q) accepted garbage", reply)
		}
	}
}

func TestParseStatusMissingTemperature(t *testing.T) {
	r, err := ParseStatus("(230.0 230.0 230.0 010 50.0 13.6 --.- 00001000")
	if err != nil {
		t.Fatal(err)
	}
	if r.Temperature != nil {
		t.Errorf("temperature = %v, want nil", *r.Temperature)
	}
}

func TestParseRating(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  ups.Rating
	}{
		{"#220.0 003 048.0 50.0", ups.Rating{Voltage: 220, Current: 3, BatteryVoltage: 48, Frequency: 50}},
		// Shipped fixture, with '(' instead of '#'.
		{"(220.0 220.0 72.00 50.0", ups.Rating{Voltage: 220, Current: 220, BatteryVoltage: 72, Frequency: 50}},
	} {
		got, err := ParseRating(tc.reply)
		if err != nil {
			t.Fatalf("%q: %v", tc.reply, err)
		}
		if got != tc.want {
			t.Errorf("%q: got %+v, want %+v", tc.reply, got, tc.want)
		}
	}
}

func TestParseCapacity(t *testing.T) {
	c, err := ParseCapacity("(99 7000 50.0 34 220 222 v1.00 0000100000000000")
	if err != nil {
		t.Fatal(err)
	}
	if c.Charge != 99 || c.Runtime != 7000 {
		t.Errorf("got %+v", c)
	}
	if _, err := ParseCapacity("(150 10"); err == nil {
		t.Error("accepted charge over 100")
	}
}

func TestParseInfo(t *testing.T) {
	info, err := ParseInfo("#MMMMMMMMMMMMMMM 1500VA")
	if err != nil {
		t.Fatal(err)
	}
	if info.RatedPower == nil || *info.RatedPower != 1500 {
		t.Errorf("rated power = %v", info.RatedPower)
	}
	info, err = ParseInfo("#TECNOWARE       UPS 800      V1.0  ")
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "TECNOWARE UPS 800 V1.0" || info.RatedPower != nil {
		t.Errorf("got %+v", info)
	}
}

func TestDeriveMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		out   ups.Volts
		flags ups.StatusFlags
		want  ups.WorkMode
	}{
		{"line", 230, ups.StatusFlags{}, ups.ModeLine},
		{"battery", 230, ups.StatusFlags{UtilityFail: true}, ups.ModeBattery},
		{"self test", 230, ups.StatusFlags{SelfTest: true}, ups.ModeBatteryTest},
		{"standby", 0, ups.StatusFlags{}, ups.ModeStandby},
		{"fault overrides", 230, ups.StatusFlags{UPSFailed: true}, ups.ModeFault},
		{"fault with no output", 0, ups.StatusFlags{UPSFailed: true}, ups.ModeFault},
	} {
		got := DeriveMode(StatusReading{OutputVoltage: tc.out, Flags: tc.flags})
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestChargeEstimator(t *testing.T) {
	e := NewChargeEstimator()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if got := e.Estimate(27.4, ups.ModeLine, 10, t0); got != 100 {
		t.Errorf("24V string at 13.7 V/block: got %v, want 100", got)
	}
	if got := e.Estimate(12.45, ups.ModeLine, 10, t0); got != 44 {
		// 90 * (12.45 - 11.6) / 1.7 = 44.99..., truncated like ViewPower does
		t.Errorf("charging: got %v, want 44", got)
	}
	// Floating between 13.3 and 13.5 V climbs 1% every 12 minutes from 90%.
	if got := e.Estimate(13.4, ups.ModeLine, 10, t0); got != 90 {
		t.Errorf("float start: got %v, want 90", got)
	}
	if got := e.Estimate(13.4, ups.ModeLine, 10, t0.Add(11*time.Minute)); got != 90 {
		t.Errorf("float before interval: got %v, want 90", got)
	}
	if got := e.Estimate(13.4, ups.ModeLine, 10, t0.Add(12*time.Minute)); got != 91 {
		t.Errorf("float after interval: got %v, want 91", got)
	}
	// Battery mode resets the float state and uses the discharge curve.
	if got := e.Estimate(11.45, ups.ModeBattery, 50, t0); got != 50 {
		// 100 * (11.45 - 10.2) / 2.5 = 50
		t.Errorf("discharge: got %v, want 50", got)
	}
	if got := e.Estimate(13.4, ups.ModeLine, 10, t0.Add(time.Hour)); got != 90 {
		t.Errorf("after battery: got %v, want 90", got)
	}
	if got := e.Estimate(2.27, ups.ModeLine, 10, t0); got != 100 {
		t.Errorf("per-cell 2.27 V: got %v, want 100", got)
	}
}

func TestFormatShutdown(t *testing.T) {
	for _, tc := range []struct {
		delay, restore time.Duration
		want           string
	}{
		{12 * time.Second, 0, "S.2R0000"},
		{2 * time.Minute, time.Minute, "S02R0001"},
		{30 * time.Minute, 9999 * time.Minute, "S30R9999"},
	} {
		got, err := FormatShutdown(tc.delay, tc.restore)
		if err != nil || got != tc.want {
			t.Errorf("FormatShutdown(%s, %s) = %q, %v; want %q", tc.delay, tc.restore, got, err, tc.want)
		}
	}
	if _, err := FormatShutdown(time.Second, 0); err == nil {
		t.Error("accepted 1s delay")
	}
	if _, err := FormatShutdown(100*time.Minute, 0); err == nil {
		t.Error("accepted 100m delay")
	}
}

func TestDetect(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		m    string
		want ups.Protocol
	}{
		{"V", ups.ProtocolPMV},
		{"H", ups.ProtocolPMVH},
		{"I", ups.ProtocolPMVI},
		{"J", ups.ProtocolPMVJ},
	} {
		d, err := Detect(ctx, transport.NewMock(tc.m))
		if err != nil {
			t.Fatalf("M=%s: %v", tc.m, err)
		}
		if d.Protocol() != tc.want {
			t.Errorf("M=%s: got %s, want %s", tc.m, d.Protocol(), tc.want)
		}
	}
}

func TestDetectQPIFamilyIsUnsupported(t *testing.T) {
	m := transport.NewMock("V")
	m.SetReply("QPI", "(PI01")
	_, err := Detect(context.Background(), m)
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Dialect != "PI01" {
		t.Fatalf("got %v, want UnsupportedError PI01", err)
	}
}

func TestDetectPlainMegatecFallback(t *testing.T) {
	m := transport.NewMock("(NAK")
	d, err := Detect(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if d.Protocol() != ups.ProtocolMegatec {
		t.Errorf("got %s", d.Protocol())
	}
}

func TestPollReportedCapacity(t *testing.T) {
	d, err := Detect(context.Background(), transport.NewMock("J"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ChargeSource != ups.ChargeReported || s.BatteryCharge != 99 {
		t.Errorf("charge = %v (%s)", s.BatteryCharge, s.ChargeSource)
	}
	if s.RuntimeRemaining == nil || *s.RuntimeRemaining != 7000 {
		t.Errorf("runtime = %v", s.RuntimeRemaining)
	}
}

func TestPollEstimatedCapacity(t *testing.T) {
	m := transport.NewMock("V")
	m.SetReply("QS", "(230.0 230.0 230.0 020 50.0 13.70 25.0 00001000")
	d, err := Detect(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != ups.ModeLine || s.ChargeSource != ups.ChargeEstimated || s.BatteryCharge != 100 {
		t.Errorf("got mode %s charge %v (%s)", s.Mode, s.BatteryCharge, s.ChargeSource)
	}
	if s.RuntimeRemaining != nil {
		t.Errorf("runtime should be unknown, got %v", *s.RuntimeRemaining)
	}
}

func TestChargeSmoother(t *testing.T) {
	var s chargeSmoother
	if got := s.add(100); got != 100 {
		t.Errorf("first sample should seed the window, got %v", got)
	}
	// One low reading moves the average by a tenth only.
	if got := s.add(0); got != 90 {
		t.Errorf("got %v, want 90", got)
	}
	for range chargeWindow {
		s.add(50)
	}
	if got := s.add(50); got != 50 {
		t.Errorf("steady state: got %v, want 50", got)
	}
}

// Replies captured from the target unit (Tecnoware 800VA, USB 0665:5161)
// on 2026-10-26 through vpowerd debug logs.
const (
	tecnowareQS = "(240.2 240.2 236.5 016 49.8 13.8 --.- 00001001"
	tecnowareF  = "#230.0 005 12.00 50.0"
)

func TestTecnowareReplies(t *testing.T) {
	r, err := ParseStatus(tecnowareQS)
	if err != nil {
		t.Fatal(err)
	}
	if r.InputVoltage != 240.2 || r.OutputVoltage != 236.5 || r.LoadPercent != 16 || r.BatteryVoltage != 13.8 {
		t.Errorf("values %+v", r)
	}
	if r.Temperature != nil {
		t.Errorf("temperature should be missing, got %v", *r.Temperature)
	}
	if want := (ups.StatusFlags{LineInteractive: true, BeeperOn: true}); r.Flags != want {
		t.Errorf("flags %+v", r.Flags)
	}
	if DeriveMode(r) != ups.ModeLine {
		t.Errorf("mode %s", DeriveMode(r))
	}
	rating, err := ParseRating(tecnowareF)
	if err != nil {
		t.Fatal(err)
	}
	if rating != (ups.Rating{Voltage: 230, Current: 5, BatteryVoltage: 12, Frequency: 50}) {
		t.Errorf("rating %+v", rating)
	}
}

// echoMock counts queries to check that an echoed command is not retried.
type echoMock struct {
	*transport.Mock
	queries map[string]int
}

func (e *echoMock) Query(ctx context.Context, cmd string) (string, error) {
	e.queries[cmd]++
	return e.Mock.Query(ctx, cmd)
}

func TestTecnowareDetectionDoesNotRetryEchoes(t *testing.T) {
	m := &echoMock{Mock: transport.NewMock("V"), queries: map[string]int{}}
	m.SetReply("QPI", "QPI")
	m.SetReply("I", "I")
	m.SetReply("QID", "QID")
	m.SetReply("QS", tecnowareQS)
	m.SetReply("F", tecnowareF)

	d, err := Detect(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	id, rating, err := d.Identify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id.Protocol != ups.ProtocolPMV || id.Model != "" || id.Serial != "" || rating == nil {
		t.Errorf("identity %+v rating %+v", id, rating)
	}
	for _, cmd := range []string{"QPI", "I", "QID"} {
		if m.queries[cmd] != 1 {
			t.Errorf("%s queried %d times, want 1", cmd, m.queries[cmd])
		}
	}
}
