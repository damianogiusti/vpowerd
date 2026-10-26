//go:build linux

package transport

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// OpenSerial opens an RS232 port in raw 2400 8N1 mode, the default baud rate
// in the shipped viewpower.properties.
func OpenSerial(path string, timing Timing) (Transport, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := setRaw2400(fd); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("configure %s: %w", path, err)
	}
	return newFDConn(fd, path, timing, writeAll), nil
}

func setRaw2400(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag = 0
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = unix.CS8 | unix.CREAD | unix.CLOCAL | unix.B2400
	t.Ispeed = unix.B2400
	t.Ospeed = unix.B2400
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		return err
	}
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
}
