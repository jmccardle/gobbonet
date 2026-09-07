package debugreport

import (
	"fmt"
	"syscall"
	"unsafe"
)

type osVersionInfoExW struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
	ServicePackMajor  uint16
	ServicePackMinor  uint16
	SuiteMask         uint16
	ProductType       byte
	Reserved          byte
}

// osVersion asks RtlGetVersion rather than GetVersionEx.
//
// GetVersionEx lies by design: since Windows 8.1 it reports 6.2 to any binary
// without a compatibility manifest, so it would tell us every reporter is on
// Windows 8 forever. RtlGetVersion is the documented way to get the truth, and
// the build number is what actually matters — the difference between Windows 10
// 1809 and 22H2 is a build number, and console and path behaviour differ across
// that range.
func osVersion() string {
	ntdll := syscall.NewLazyDLL("ntdll.dll")
	proc := ntdll.NewProc("RtlGetVersion")

	var info osVersionInfoExW
	info.OSVersionInfoSize = uint32(unsafe.Sizeof(info))
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&info)))
	if r != 0 {
		return ""
	}
	return fmt.Sprintf("Windows %d.%d build %d",
		info.MajorVersion, info.MinorVersion, info.BuildNumber)
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func totalRAM() int64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GlobalMemoryStatusEx")

	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return 0
	}
	return int64(st.TotalPhys)
}

// elevated reports whether the process token is elevated.
//
// This is the other half of the mapped-drive diagnosis: drive letters are
// per-logon-session, and an elevated process runs in a different session from
// the desktop that created the mapping. "It works when I double-click it but
// not from the service" is this field.
func elevated() bool {
	var token syscall.Token
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	// TokenElevation = 20; the returned DWORD is non-zero when elevated.
	const tokenElevation = 20
	var elevation uint32
	var outLen uint32
	err = syscall.GetTokenInformation(token, tokenElevation,
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &outLen)
	if err != nil {
		return false
	}
	return elevation != 0
}
