//go:build linux

package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDetectKindByName(t *testing.T) {
	if got := DetectKind("/dev/hidraw3", t.TempDir()); got != KindHIDRaw {
		t.Errorf("got %s", got)
	}
	if got := DetectKind("/dev/ttyUSB0", t.TempDir()); got != KindSerial {
		t.Errorf("got %s", got)
	}
}

// /dev/null is a char device available everywhere; a fake sysfs makes its
// number look like a hidraw node, as /dev/ups does inside an LXC.
func TestDetectKindByDeviceNumber(t *testing.T) {
	var st unix.Stat_t
	if err := unix.Stat("/dev/null", &st); err != nil {
		t.Skip("no /dev/null")
	}
	sys := t.TempDir()
	charDir := filepath.Join(sys, "dev", "char")
	if err := os.MkdirAll(charDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev))
	if err := os.Symlink("../../devices/pci0000:00/usb2/2-1/2-1:1.0/0003:0665:5161.0002/hidraw/hidraw1", filepath.Join(charDir, name)); err != nil {
		t.Fatal(err)
	}
	if got := DetectKind("/dev/null", sys); got != KindHIDRaw {
		t.Errorf("got %s, want hidraw", got)
	}
	if got := DetectKind("/dev/null", t.TempDir()); got != KindSerial {
		t.Errorf("without sysfs entry got %s, want serial", got)
	}
}

// Inside an LXC the host's hidraw1 shows up under another name. /dev/null
// stands in for it: the sysfs fixture gives hidraw1 the number of /dev/null.
func TestFindHIDRawMatchesByDeviceNumber(t *testing.T) {
	var st unix.Stat_t
	if err := unix.Stat("/dev/null", &st); err != nil {
		t.Skip("no /dev/null")
	}
	sys := t.TempDir()
	number := fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev))
	writeHIDRawFixture(t, sys, "hidraw1", "HID_ID=0003:00000665:00005161\n", number)

	got, err := FindHIDRaw(sys, "/dev", KnownUPSIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/dev/null" {
		t.Fatalf("got %+v, want /dev/null", got)
	}
}
