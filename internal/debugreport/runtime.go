package debugreport

// Runtime is live process state, filled in by the running server through
// RuntimeSource. Everything here is lost when the process exits, which is why
// `gobbonet debug-report` asks a running server for it rather than reproducing
// it from disk.
type Runtime struct {
	PID     int    `json:"pid"`
	Uptime  string `json:"uptime,omitempty"`
	Mode    string `json:"mode"`
	Hotswap bool   `json:"hotswap"`

	Listener *Listener `json:"listener,omitempty"`

	Supervisor *SupervisorState `json:"supervisor,omitempty"`
	Jobs       []JobRecord      `json:"jobs,omitempty"`

	// UpstreamOK is the server's own cached view, which can differ from a fresh
	// probe. When they disagree the cache is stale, and that disagreement is
	// itself worth seeing: it is what a status pill showing green against a
	// dead upstream looks like from the inside.
	UpstreamOK  bool   `json:"upstream_ok"`
	UpstreamURL string `json:"upstream_url,omitempty"`
}

// Listener is where the server actually bound, which is not always where it was
// asked to bind.
type Listener struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// LANReachable is false for a loopback bind, so "I cannot reach it from my
	// phone" is answered in one field.
	LANReachable bool `json:"lan_reachable"`
	// FellBack and BindError record a wide bind that was refused and downgraded
	// to loopback — the Windows firewall case, which otherwise presents as the
	// server working perfectly on the machine it runs on and being invisible
	// everywhere else.
	FellBack  bool     `json:"fell_back"`
	BindError string   `json:"bind_error,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

// SupervisorState is the llama-server child process.
type SupervisorState struct {
	Phase   string `json:"phase"`
	Model   string `json:"model,omitempty"`
	File    string `json:"file,omitempty"`
	Message string `json:"message,omitempty"`

	// LastError is the one line the supervisor picked out of llama-server's
	// stderr as actionable, and Stderr is the tail it came from.
	//
	// This is the single highest-value field in the report and until now nothing
	// surfaced it. The supervisor has captured the child's stderr since it
	// replaced Get-LlamaStartupError, and "out of memory", "no such file" and
	// "address already in use" have been sitting in that buffer the whole time
	// while the user was shown "Error: 502".
	LastError string `json:"last_error,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
}

// JobRecord is one detached generation, reduced to its metadata.
//
// The spooled reply is never included and never can be: the source of these is
// Job.snapshot(), which returns the buffer's length and not its contents. Size
// is worth keeping — a job that errored at 0 bytes failed before the model said
// anything, and one that errored at 4 KB was interrupted mid-answer, which are
// different bugs.
type JobRecord struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Bytes     int    `json:"bytes"`
	StartedAt int64  `json:"started_at,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

// redact applies home-directory rewriting to the free-form fields. Paths turn
// up inside llama-server's stderr constantly — it names the GGUF it could not
// open — so the tail has to go through the same rewriting as a path field.
func (r *Runtime) redact(red redactor) {
	if !red.enabled() {
		return
	}
	r.UpstreamURL = red.text(r.UpstreamURL)
	if r.Supervisor != nil {
		r.Supervisor.Model = red.text(r.Supervisor.Model)
		r.Supervisor.File = red.text(r.Supervisor.File)
		r.Supervisor.Message = red.text(r.Supervisor.Message)
		r.Supervisor.LastError = red.text(r.Supervisor.LastError)
		r.Supervisor.Stderr = red.text(r.Supervisor.Stderr)
	}
	if r.Listener != nil {
		r.Listener.BindError = red.text(r.Listener.BindError)
	}
	for i := range r.Jobs {
		r.Jobs[i].Error = red.text(r.Jobs[i].Error)
	}
}
