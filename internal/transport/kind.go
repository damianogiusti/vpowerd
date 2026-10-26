package transport

import (
	"fmt"
	"strings"
)

// Kind selects how a device path is driven.
type Kind string

const (
	KindAuto   Kind = "auto"
	KindHIDRaw Kind = "hidraw"
	KindSerial Kind = "serial"
)

// ParseKind validates an external value.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(s); k {
	case KindAuto, KindHIDRaw, KindSerial:
		return k, nil
	default:
		return "", fmt.Errorf("transport %q: want auto, hidraw or serial", s)
	}
}

// DetectKind tells whether path is a hidraw node or a serial port.
//
// The name is not enough: a Proxmox LXC "dev0: /dev/ups" passthrough shows
// the hidraw node as /dev/ups inside the container. The device number is
// looked up in sysRoot ("/sys") instead, falling back to the name.
func DetectKind(path, sysRoot string) Kind {
	if strings.Contains(path, "hidraw") {
		return KindHIDRaw
	}
	if isHIDRawNode(path, sysRoot) {
		return KindHIDRaw
	}
	return KindSerial
}
