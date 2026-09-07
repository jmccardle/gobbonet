package debugreport

import (
	"os"
	"strconv"
	"strings"
)

// osVersion prefers the distribution's own name over the kernel version,
// because "Debian GNU/Linux 12 (bookworm)" tells a maintainer which glibc and
// which packaged llama.cpp to expect, and "6.1.0-42-amd64" does not.
// Both are reported when both are readable.
func osVersion() string {
	pretty := osReleaseField("PRETTY_NAME")
	kernel := strings.TrimSpace(readFileString("/proc/sys/kernel/osrelease"))

	switch {
	case pretty != "" && kernel != "":
		return pretty + " (kernel " + kernel + ")"
	case pretty != "":
		return pretty
	default:
		return kernel
	}
}

func osReleaseField(key string) string {
	// /usr/lib/os-release is the fallback the spec defines for systems where
	// /etc is not populated, which is the normal state inside a container.
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		for _, line := range strings.Split(readFileString(p), "\n") {
			name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || name != key {
				continue
			}
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

// totalRAM reads MemTotal, which is in kibibytes.
//
// Worth having because "the model will not load" is very often "this machine
// does not have room for it", and a user who knows their GPU's VRAM rarely
// thinks to mention system memory.
func totalRAM() int64 {
	for _, line := range strings.Split(readFileString("/proc/meminfo"), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

func elevated() bool { return os.Geteuid() == 0 }

func readFileString(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
