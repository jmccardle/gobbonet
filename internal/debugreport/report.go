// Package debugreport assembles the "must gather" packet: everything a
// maintainer needs to diagnose a broken install from a single paste, collected
// in one pass with no follow-up questions.
//
// The threat model here is a confused user on their own machine, not an
// attacker on their network. A report is optimised for diagnostic yield, so it
// errs towards including a field rather than omitting it: an absent field costs
// a round trip to the reporter, and round trips are what this package exists to
// eliminate. Only two things are genuinely secret and both are redacted at the
// source (see config.go): the access secret and the upstream API key.
//
// One collector, three surfaces:
//
//	GET /diagnostics.json     internal/server/diagnostics.go
//	gobbonet debug-report     cmd/gobbonet/main.go
//	the status-label modal    js/24-diagnostics.js, rendering the JSON
//
// Anything that reads live process state — the supervisor's captured stderr,
// the job table, the listener's bind — arrives through the RuntimeSource
// interface rather than an import, because internal/server imports this package
// and the reverse would be a cycle.
//
// Absence is always explicit. A section that could not be collected is nil and
// leaves a line in Notes saying why; nothing here renders a blank where it
// meant "unknown", because a blank reads as "fine" and sends the reader looking
// somewhere else.
package debugreport

import (
	"context"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// SchemaVersion is the shape of this report, bumped when a consumer would have
// to change. The frontend renders defensively regardless, but a maintainer
// reading a pasted report needs to know which fields to expect.
const SchemaVersion = 1

// Tier names how much of the report was assembled.
const (
	// TierFull is everything: config, probes, runtime state, model inventory.
	TierFull = "full"
	// TierPublic is what an unauthenticated caller gets. Enough for the login
	// page to explain why a sign-in is failing, and nothing else. It runs no
	// probes at all, so an anonymous request can never make the server emit an
	// outbound connection.
	TierPublic = "public"
)

// Report is the packet.
type Report struct {
	Schema    int    `json:"schema"`
	Tier      string `json:"tier"`
	Generated string `json:"generated"` // RFC3339, local time with offset

	Build  Build          `json:"build"`
	System *System        `json:"system,omitempty"`
	Config *ConfigSection `json:"config,omitempty"`

	Auth    Auth     `json:"auth"`
	Network *Network `json:"network,omitempty"`
	Runtime *Runtime `json:"runtime,omitempty"`
	Data    *Data    `json:"data,omitempty"`
	Models  *Models  `json:"models,omitempty"`

	// Notes records why a section is missing, one line each. Never empty when a
	// pointer above is nil.
	Notes []string `json:"notes,omitempty"`
}

// Auth describes the password gate. Present in both tiers: in the public tier
// it is the whole point of the report, and in the full tier it confirms which
// secret format is in use, which is the difference between a login that works
// and one that fails after an upgrade.
type Auth struct {
	// Required is false when --no-auth was passed. A running server with auth
	// on always has a usable secret: ensurePassword refuses to start on a
	// malformed one, so "malformed" is a state this field can never report.
	Required bool `json:"required"`
	// SecretFormat is "argon2id", "legacy-sha256" or "none". A legacy secret
	// still works and upgrades itself on the next successful login.
	SecretFormat string `json:"secret_format"`
	// Session is "valid", "none" (nothing presented) or "stale" (presented and
	// rejected).
	//
	// Deliberately not split into expired / unknown / fingerprint-mismatch.
	// Those distinctions would tell whoever holds a copied cookie that their
	// token is live and only the client fingerprint is missing, and the
	// fingerprint is documented as a cheap extra bar rather than a real
	// identity. "stale" carries the whole of the user-facing meaning — sign in
	// again — without that.
	Session string `json:"session"`
	Login   string `json:"login"`
}

// Session values. SessionNotApplicable is the CLI's: it reads the config off
// disk and authenticates to nothing, so reporting a session state there would
// be answering a question nobody asked.
const (
	SessionValid         = "valid"
	SessionNone          = "none"
	SessionStale         = "stale"
	SessionNotApplicable = "n/a — collected from the command line"
)

// Secret format values.
const (
	SecretArgon2id = "argon2id"
	SecretLegacy   = "legacy-sha256"
	SecretNone     = "none"
)

// RuntimeSource is implemented by the running server. Nil when the collector is
// invoked from a CLI with no server up, which is a normal case rather than an
// error — `gobbonet debug-report` has to work on an install that will not start.
type RuntimeSource interface {
	DebugRuntime() Runtime
}

// Options steers a collection.
type Options struct {
	// Cfg is the loaded configuration. Required.
	Cfg config.Config

	// Tier selects the payload. Defaults to TierFull.
	Tier string

	// Session is the caller's auth state, which only the HTTP surface can know.
	// Empty means SessionValid — the CLI reads the config off disk and is not
	// gated by anything.
	Session string

	// Probe runs the connectivity checks. Never set in TierPublic.
	Probe bool

	// TestPrompt sends a one-token completion upstream. Off unless the operator
	// asked for it: it is an outbound request and, against a metered API
	// endpoint, it costs money. Implies Probe.
	TestPrompt bool

	// RedactHome rewrites the user's home directory to "~" everywhere in the
	// report. Off by default: for the whole class of bug where a non-ASCII
	// username breaks path handling, the username IS the evidence, and
	// redacting it by default would quietly destroy the thing we need. The UI
	// and CLI both offer it as a choice.
	RedactHome bool

	// Runtime supplies live process state. Nil when no server is running.
	Runtime RuntimeSource

	// RuntimeNote replaces the default explanation when Runtime is nil. The CLI
	// sets it to say which of the two reasons applies — no server at all, or a
	// server that is up but behind a password this command deliberately does
	// not ask for — because those send the reader to different next steps.
	RuntimeNote string

	// Now is injectable for tests. Zero means time.Now().
	Now time.Time
}

// Collect assembles a report. It never returns an error: a section that cannot
// be gathered is recorded as a note and the rest of the report is still worth
// having. A collector that refuses to produce anything because one probe failed
// would be useless in exactly the situation it exists for.
func Collect(ctx context.Context, opts Options) Report {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	tier := opts.Tier
	if tier == "" {
		tier = TierFull
	}
	session := opts.Session
	if session == "" {
		session = SessionValid
	}

	r := Report{
		Schema:    SchemaVersion,
		Tier:      tier,
		Generated: now.Format(time.RFC3339),
		Build:     collectBuild(),
		Auth:      collectAuth(opts.Cfg, session),
	}

	if tier == TierPublic {
		// Everything below this line either names a filesystem path, reveals an
		// upstream address, or makes an outbound request. The public tier is
		// the login page's, and the login page needs none of it.
		r.Build = publicBuild(r.Build)
		return r
	}

	red := newRedactor(opts.RedactHome)

	sys := collectSystem(opts.Cfg, red)
	r.System = &sys

	cfgSec := collectConfig(opts.Cfg, red)
	r.Config = &cfgSec

	data := collectData(opts.Cfg, red)
	r.Data = &data

	models := collectModels(opts.Cfg, red)
	r.Models = &models

	if opts.Runtime != nil {
		rt := opts.Runtime.DebugRuntime()
		rt.redact(red)
		r.Runtime = &rt
	} else {
		note := opts.RuntimeNote
		if note == "" {
			note = "runtime: not collected — no gobbonet server was running to ask. " +
				"Supervisor state, llama-server's captured stderr and recent " +
				"generation attempts are only available from a live process."
		}
		r.Notes = append(r.Notes, note)
	}

	if opts.Probe || opts.TestPrompt {
		net := probe(ctx, opts.Cfg, opts.TestPrompt)
		r.Network = &net
	} else {
		r.Notes = append(r.Notes,
			"network: not collected — probes were disabled. Re-run without "+
				"--no-probe to test the upstream connection.")
	}

	return r
}
