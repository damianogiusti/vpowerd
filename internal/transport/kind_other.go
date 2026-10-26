//go:build !linux

package transport

func isHIDRawNode(string, string) bool { return false }

func findDevNode(_, _, preferred string) (string, bool) { return preferred, true }
