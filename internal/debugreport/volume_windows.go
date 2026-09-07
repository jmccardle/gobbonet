package debugreport

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Drive type codes from GetDriveTypeW.
const (
	driveUnknown   = 0
	driveNoRootDir = 1
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4
	driveCDROM     = 5
	driveRAMDisk   = 6
)

var driveTypeNames = map[uintptr]string{
	driveUnknown:   "unknown",
	driveNoRootDir: "no-root-dir",
	driveRemovable: "removable",
	driveFixed:     "fixed",
	driveRemote:    "network",
	driveCDROM:     "cdrom",
	driveRAMDisk:   "ramdisk",
}

// volumeInfo names the volume a path sits on and what kind of volume it is.
//
// This is the field that explains "The system cannot find the drive specified"
// for a path that plainly exists. Two answers do it. "network" means a mapped
// drive, and drive mappings belong to a logon session — a process started by
// the service manager, a scheduled task, or an elevated shell has a different
// session and genuinely cannot see F: even though Explorer can. "no-root-dir"
// or "removable" means the letter is not currently attached at all.
//
// A UNC path has no drive letter and is reported as such, because cmd.exe
// refuses to make one the current directory, which produces the same error
// from a completely different cause.
func volumeInfo(p string) (volume, kind string) {
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return "", "unc"
	}
	vol := filepath.VolumeName(p)
	if vol == "" {
		return "", ""
	}

	root := vol + `\`
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return vol, ""
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDriveType := kernel32.NewProc("GetDriveTypeW")
	r, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(rootPtr)))

	name, ok := driveTypeNames[r]
	if !ok {
		name = "unknown"
	}
	return vol, name
}
