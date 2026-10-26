//go:build linux

package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// isHIDRawNode resolves /sys/dev/char/<major>:<minor>, which links into the
// hidraw class for hidraw nodes.
func isHIDRawNode(path, sysRoot string) bool {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return false
	}
	link := filepath.Join(sysRoot, "dev", "char", fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev)))
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	return strings.Contains(target, "/hidraw/")
}

// findDevNode returns the node in devRoot whose device number is majMin
// ("241:0"). preferred is checked first; the scan is not recursive and skips
// symlinks, so it returns the real node rather than an alias.
func findDevNode(devRoot, majMin, preferred string) (string, bool) {
	if devNumber(preferred, unix.Stat) == majMin {
		return preferred, true
	}
	entries, err := os.ReadDir(devRoot)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		path := filepath.Join(devRoot, e.Name())
		if devNumber(path, unix.Lstat) == majMin {
			return path, true
		}
	}
	return "", false
}

// devNumber returns "major:minor" of a character device, or "".
func devNumber(path string, stat func(string, *unix.Stat_t) error) string {
	var st unix.Stat_t
	if err := stat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return ""
	}
	return fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev))
}
