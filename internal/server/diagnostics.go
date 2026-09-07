package server

import (
	"net/http"
	"os"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/auth"
	"github.com/ElodineOfficial/GobboNet/internal/config"
	"github.com/ElodineOfficial/GobboNet/internal/debugreport"
	"github.com/ElodineOfficial/GobboNet/internal/httpx"
)

// recentJobsReported is how many generations the runtime section carries. Far
// enough back to cover "it broke, I retried twice, then it broke differently",
// short enough that a busy server does not bury the report.
const recentJobsReported = 10

// stderrTailLines is how much of llama-server's output to include. A failed
// model load prints its diagnosis within the last dozen or so lines; forty
// gives room for the allocation table that precedes an out-of-memory kill.
const stderrTailLines = 40

// handleDiagnostics serves GET and POST /diagnostics.json.
//
// This route answers before the auth gate, which makes it the fourth entry in a
// deliberately short list (see ServeHTTP). It has to: the report exists for
// people who cannot get in, and one that requires getting in first would be
// unreachable in exactly that case. The unauthenticated answer is built by a
// separate path in the collector rather than by stripping fields off the full
// one, and it runs no probes at all — an anonymous request can never make this
// server open an outbound connection.
//
// GET collects and probes. POST additionally sends a one-token test message
// upstream: that is a real outbound request against what may be a metered API,
// so it is a method with side effects rather than something a browser prefetch
// can trigger.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "GET or POST")
		return
	}

	authed := s.authenticated(r)
	opts := debugreport.Options{
		Cfg:     s.reportConfig(),
		Session: s.sessionState(r),
	}

	if !authed {
		opts.Tier = debugreport.TierPublic
		httpx.WriteJSON(w, r, http.StatusOK, debugreport.Collect(r.Context(), opts))
		return
	}

	opts.Tier = debugreport.TierFull
	opts.Runtime = s
	opts.RedactHome = r.URL.Query().Get("redact_home") == "1"
	// Probes are the point of the report, so they are on unless explicitly
	// waived — a caller polling this endpoint for something else can pass
	// probe=0 rather than paying five timeouts.
	opts.Probe = r.URL.Query().Get("probe") != "0"
	opts.TestPrompt = r.Method == http.MethodPost

	httpx.WriteJSON(w, r, http.StatusOK, debugreport.Collect(r.Context(), opts))
}

// reportConfig is the configuration as it stands NOW, not as it was loaded.
//
// Two fields drift while the server runs and both would be reported wrongly
// from the startup copy. The secret is rewritten in place when a legacy hash
// verifies and is upgraded to Argon2id, so the loaded copy would keep claiming
// "legacy-sha256" long after the migration happened — turning the one field
// that proves the migration works into a field that says it never does. And
// ctx_size is whatever /perf last applied, which is what the model is actually
// running with.
func (s *Server) reportConfig() config.Config {
	cfg := s.cfg

	s.secretMu.RLock()
	cfg.AccessSecret = s.secret
	s.secretMu.RUnlock()

	current, auto, overridden := s.tuning.get()
	cfg.CtxSize = current.CtxSize
	cfg.GPULayers = current.GPULayers
	cfg.KVCacheType = current.KVCacheType
	cfg.AutoCtxSize = auto.CtxSize
	cfg.AutoGPULayers = auto.GPULayers
	cfg.AutoKVCacheType = auto.KVCacheType
	cfg.PerfOverridden = overridden

	return cfg
}

// sessionState maps the request's cookie to the three states the report names.
func (s *Server) sessionState(r *http.Request) string {
	if !s.authRequired() {
		// Nothing to be signed in to. Reporting "none" here would read as "you
		// are logged out" on a server that has no gate at all.
		return debugreport.SessionValid
	}
	return s.sessions.Diagnose(auth.TokenFromRequest(r), auth.ClientFingerprint(r))
}

// DebugRuntime implements debugreport.RuntimeSource.
//
// Everything here is state that dies with the process, which is why the CLI
// asks a running server for it over HTTP rather than trying to reconstruct it
// from disk: the captured stderr of a llama-server that has already exited
// exists only in this process's memory.
func (s *Server) DebugRuntime() debugreport.Runtime {
	rt := debugreport.Runtime{
		PID:         os.Getpid(),
		Mode:        string(s.mode),
		Hotswap:     s.sup != nil,
		UpstreamOK:  s.upstreamOK(),
		UpstreamURL: s.cfg.LLMURL,
	}
	if !s.started.IsZero() {
		rt.Uptime = debugreport.FormatUptime(time.Since(s.started))
	}

	if b := s.Bind(); b != nil {
		l := &debugreport.Listener{
			Host:         b.Host,
			Port:         b.Port,
			LANReachable: b.LANReachable(),
			FellBack:     b.FellBack,
		}
		if b.FellBack && b.WideErr != nil {
			l.BindError = b.WideErr.Error()
		}
		if l.LANReachable {
			for _, a := range LANAddrsFor(b.Host) {
				l.Addresses = append(l.Addresses, a.URL(b.Port))
			}
		}
		rt.Listener = l
	}

	if s.sup != nil {
		st := s.sup.Status()
		rt.Supervisor = &debugreport.SupervisorState{
			Phase:     st.Phase,
			Model:     st.Name,
			File:      st.File,
			Message:   st.Message,
			LastError: s.sup.StderrLastError(),
			Stderr:    s.sup.StderrTail(stderrTailLines),
		}
	}

	for _, j := range s.jobs.Recent(recentJobsReported) {
		rt.Jobs = append(rt.Jobs, debugreport.JobRecord{
			ID:        j.ID,
			Status:    j.Status,
			Error:     j.Error,
			Bytes:     j.Bytes,
			StartedAt: j.StartedAt,
			UpdatedAt: j.UpdatedAt,
		})
	}

	return rt
}
