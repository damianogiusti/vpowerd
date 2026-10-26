//go:build linux

package transport

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// hidReportSize is the payload size of one output report.
//
// The shipped driver (libUSBDevice.so, ups_write) splits a command in 8 byte
// chunks and sends each one with a HID SET_REPORT control transfer
// (bmRequestType 0x21, bRequest 0x09, wValue 0x0200: output report, ID 0).
// Writing to hidraw produces the same transfer when the first byte is the
// report ID 0, so no libusb and no native code are needed.
const hidReportSize = 8

// OpenHIDRaw opens a /dev/hidrawN node of a Voltronic-style UPS.
func OpenHIDRaw(path string, timing Timing) (Transport, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return newFDConn(fd, path, timing, writeHIDReports), nil
}

func writeHIDReports(fd int, payload []byte) error {
	for _, report := range hidReports(payload) {
		if err := writeAll(fd, report[:]); err != nil {
			return err
		}
	}
	return nil
}

// hidReports frames payload as report-ID-prefixed, zero padded reports.
func hidReports(payload []byte) [][hidReportSize + 1]byte {
	reports := make([][hidReportSize + 1]byte, 0, (len(payload)+hidReportSize-1)/hidReportSize)
	for off := 0; off < len(payload); off += hidReportSize {
		var r [hidReportSize + 1]byte // r[0] = report ID 0
		copy(r[1:], payload[off:min(off+hidReportSize, len(payload))])
		reports = append(reports, r)
	}
	return reports
}

func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}
