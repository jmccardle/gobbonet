package debugreport

import (
	"os"
	"strings"
)

// redactor rewrites the parts of a report a user might not want to publish.
//
// Deliberately narrow. Two values are secret and are removed unconditionally in
// config.go: the access secret and the upstream API key. Everything else is
// evidence, and the home directory in particular is evidence — the entire class
// of bug where a non-ASCII username or a relocated profile breaks path handling
// is invisible once the path is rewritten to "~". So home redaction is opt-in,
// offered as a checkbox in the modal and a flag on the CLI, and the report says
// on its face which mode produced it.
type redactor struct {
	home string // "" when redaction is off or no home could be resolved
}

func newRedactor(redactHome bool) redactor {
	if !redactHome {
		return redactor{}
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return redactor{}
	}
	return redactor{home: strings.TrimRight(h, `/\`)}
}

// path rewrites a filesystem path for output.
func (r redactor) path(p string) string {
	if r.home == "" || p == "" {
		return p
	}
	// Case-insensitive on Windows, where the same directory is reachable as
	// both C:\Users\… and c:\users\…. Comparing lowercased and slicing the
	// original keeps the reported remainder in its real case.
	if len(p) >= len(r.home) && strings.EqualFold(p[:len(r.home)], r.home) {
		return "~" + p[len(r.home):]
	}
	return p
}

// text rewrites free-form output — a captured stderr tail, an error message —
// where a path may appear anywhere in the string.
func (r redactor) text(s string) string {
	if r.home == "" || s == "" {
		return s
	}
	return strings.ReplaceAll(s, r.home, "~")
}

// enabled reports whether home redaction is on, so the report can say so.
func (r redactor) enabled() bool { return r.home != "" }
