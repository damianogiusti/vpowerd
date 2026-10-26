package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// USBID is a USB vendor/product pair.
type USBID struct {
	Vendor  uint16
	Product uint16
}

func (id USBID) String() string { return fmt.Sprintf("%04x:%04x", id.Vendor, id.Product) }

// KnownUPSIDs are the pairs registered by the shipped driver
// (USBCommEx static initialiser -> USBComm.addVidPid).
var KnownUPSIDs = []USBID{
	{0x0665, 0x5161},
	{0x2e66, 0x0100},
	{0x2e66, 0x0101},
	{0x2e66, 0x0102},
	{0x2e28, 0x0001},
	{0x10af, 0x0002},
	{0x09ae, 0x3028},
}

// HIDRawDevice is a hidraw node matched to a USB ID.
type HIDRawDevice struct {
	Path string
	ID   USBID
}

// FindHIDRaw lists hidraw nodes whose USB ID is in ids, sorted by path.
// sysRoot and devRoot are "/sys" and "/dev" in production.
//
// The node is matched by device number, not by name: inside a Proxmox LXC the
// host's hidraw1 can appear as /dev/ups, while /sys still says "hidraw1".
func FindHIDRaw(sysRoot, devRoot string, ids []USBID) ([]HIDRawDevice, error) {
	entries, err := filepath.Glob(filepath.Join(sysRoot, "class", "hidraw", "hidraw*"))
	if err != nil {
		return nil, err
	}
	var found []HIDRawDevice
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(entry, "device", "uevent"))
		if err != nil {
			continue
		}
		id, ok := parseHIDUevent(string(raw))
		if !ok || !containsID(ids, id) {
			continue
		}
		preferred := filepath.Join(devRoot, filepath.Base(entry))
		number, err := os.ReadFile(filepath.Join(entry, "dev"))
		if err != nil {
			continue
		}
		path, ok := findDevNode(devRoot, strings.TrimSpace(string(number)), preferred)
		if !ok {
			continue
		}
		found = append(found, HIDRawDevice{Path: path, ID: id})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found, nil
}

// parseHIDUevent reads the "HID_ID=0003:00000665:00005161" line.
func parseHIDUevent(uevent string) (USBID, bool) {
	for _, line := range strings.Split(uevent, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "HID_ID=")
		if !ok {
			continue
		}
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return USBID{}, false
		}
		vendor, err1 := strconv.ParseUint(parts[1], 16, 32)
		product, err2 := strconv.ParseUint(parts[2], 16, 32)
		if err1 != nil || err2 != nil || vendor > 0xffff || product > 0xffff {
			return USBID{}, false
		}
		return USBID{Vendor: uint16(vendor), Product: uint16(product)}, true
	}
	return USBID{}, false
}

func containsID(ids []USBID, id USBID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
