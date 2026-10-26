package transport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindHIDRawSkipsUnknownIDs(t *testing.T) {
	root := t.TempDir()
	writeHIDRawFixture(t, root, "hidraw0", "DRIVER=hid-generic\nHID_ID=0003:0000046D:0000C52B\nHID_NAME=Logitech\n", "1:3")

	got, err := FindHIDRaw(root, t.TempDir(), KnownUPSIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func writeHIDRawFixture(t *testing.T, root, node, uevent, devNumber string) {
	t.Helper()
	dir := filepath.Join(root, "class", "hidraw", node)
	if err := os.MkdirAll(filepath.Join(dir, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "device", "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev"), []byte(devNumber+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseHIDUeventRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "HID_ID=0003:0665", "HID_ID=0003:zzzz:5161", "HID_ID=0003:100000:5161"} {
		if _, ok := parseHIDUevent(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
}
