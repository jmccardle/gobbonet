//go:build windows

package supervisor

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle is deliberately never closed.
//
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE fires when the *last* handle to the job is
// closed. Holding this one open for the lifetime of the process is what turns
// that into a guarantee: the kernel closes it when we go away — including when
// we are TerminateProcess'd, which no defer, signal handler or atexit hook ever
// sees — and it is that close which kills llama-server.
var jobHandle windows.Handle

// EnsureChildrenDieWithUs puts this process into a job object that terminates
// every member when the job's last handle closes.
//
// The orderly shutdown path (stop -> terminateGroup -> taskkill /PID /T) only
// runs when we get to run code at all: Ctrl+C, SIGTERM, a normal return. It
// cannot run when the server is ended with Task Manager's "End task", killed by
// taskkill /F, or lost to a runtime fatal error. In every one of those cases
// llama-server survives, keeps the upstream port bound and keeps several
// gigabytes of VRAM allocated; the next launch then fails to bind, exits within
// milliseconds, and reports a model that will not load with no visible cause.
// CREATE_NEW_PROCESS_GROUP makes that worse rather than better, because it
// deliberately detaches the child from our console's Ctrl+C.
//
// A job object moves the cleanup into the kernel, where it does not depend on
// this process being alive to perform it. It is a backstop, not a replacement:
// stop() still exists because a swap needs the old server gone *before* the new
// one binds, which is a sequencing problem the job object says nothing about.
func EnsureChildrenDieWithUs() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("SetInformationJobObject(kill on job close): %w", err)
	}

	// Assigning ourselves, rather than each child as it is launched, is what
	// removes the race: a child inherits job membership at creation, so there is
	// no interval between CreateProcess and AssignProcessToJobObject in which
	// llama-server could spawn a helper that escapes the job.
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	jobHandle = job
	return nil
}
