//go:build !windows

package supervisor

// EnsureChildrenDieWithUs does nothing outside Windows, and says so here rather
// than leaving the caller to guess.
//
// The Windows build joins a job object so the kernel ends llama-server when this
// process dies by any means. POSIX has no portable equivalent: PR_SET_PDEATHSIG
// is Linux-only and reaches direct children only, and a cgroup needs privileges
// the server does not ask for. Windows is the deployment target; the Unix build
// exists so the server can be developed and tested off it, and there the orderly
// stop() path remains the only cleanup, exactly as it was before.
func EnsureChildrenDieWithUs() error { return nil }
