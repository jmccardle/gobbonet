//go:build !windows

package debugreport

import (
	"os"
	"strings"
)

// localeEnv are the variables that decide text encoding on a Unix system, in
// POSIX precedence order: LC_ALL overrides everything, then the specific
// LC_CTYPE, then LANG as the fallback.
var localeEnv = []string{"LC_ALL", "LC_CTYPE", "LANG", "LANGUAGE"}

// collectLocale reads the environment's idea of text encoding.
//
// The failure this catches is narrower than the Windows code-page case but the
// same shape: a process started from systemd, cron or a bare container inherits
// no locale at all, falls back to the POSIX/C locale, and then mishandles any
// path or model filename that is not pure ASCII. An empty Effective field below
// is therefore a finding, not a gap in the report.
func collectLocale() Locale {
	l := Locale{Env: map[string]string{}}
	for _, k := range localeEnv {
		if v, ok := os.LookupEnv(k); ok {
			l.Env[k] = v
		}
	}

	for _, k := range localeEnv[:3] {
		if v := l.Env[k]; v != "" {
			l.Effective = v
			break
		}
	}
	l.Notes = localeNotes(l)
	return l
}

func localeNotes(l Locale) []string {
	var notes []string
	if l.Effective == "" {
		notes = append(notes,
			"no locale is set (LC_ALL, LC_CTYPE and LANG are all unset) — the process "+
				"is running in the C/POSIX locale, which mishandles non-ASCII filenames. "+
				"Typical of a service started by systemd or a minimal container.")
		return notes
	}
	upper := strings.ToUpper(l.Effective)
	if upper == "C" || upper == "POSIX" {
		notes = append(notes,
			"locale is C/POSIX — non-ASCII filenames and model names may not round-trip.")
		return notes
	}
	if !strings.Contains(upper, "UTF-8") && !strings.Contains(upper, "UTF8") {
		notes = append(notes,
			"locale is not UTF-8 — paths containing non-ASCII characters may be "+
				"misinterpreted between the shell and this process.")
	}
	return notes
}
