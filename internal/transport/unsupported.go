//go:build !linux

package transport

import "errors"

var errUnsupported = errors.New("transport: hidraw and serial need Linux; use the mock transport for local development")

// ErrDeviceGone mirrors the Linux definition so callers compile everywhere.
var ErrDeviceGone = errors.New("transport: device disconnected")

func OpenHIDRaw(string, Timing) (Transport, error) { return nil, errUnsupported }

func OpenSerial(string, Timing) (Transport, error) { return nil, errUnsupported }
