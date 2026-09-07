//go:build !linux && !windows

package debugreport

import "os"

// The BSDs and macOS would need sysctl for both of these. Left unimplemented
// rather than guessed: an empty OS version reads as "not collected", whereas a
// wrong one sends the reader chasing a platform difference that is not there.
// Both are cheap to add if reports start arriving from these platforms.
func osVersion() string { return "" }

func totalRAM() int64 { return 0 }

func elevated() bool { return os.Geteuid() == 0 }
