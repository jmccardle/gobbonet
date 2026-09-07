package debugreport

import (
	"os"
	"os/user"
	"runtime"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// System is the machine the install is failing on.
type System struct {
	Hostname  string `json:"hostname,omitempty"`
	OS        string `json:"os"`
	OSVersion string `json:"os_version,omitempty"`
	Arch      string `json:"arch"`
	NumCPU    int    `json:"num_cpu"`
	// TotalRAM is 0 when it could not be read, which is not the same as a
	// machine with no memory. The markdown render says "unknown" for 0.
	TotalRAM int64 `json:"total_ram_bytes,omitempty"`

	// User is who the process is running as. A mapped network drive belongs to
	// a logon session, so "the path works in Explorer but not in gobbonet" is
	// very often "gobbonet is running as somebody else" — a service account, or
	// an elevated shell, which is a different session from the desktop one.
	User     string `json:"user,omitempty"`
	UID      string `json:"uid,omitempty"`
	Elevated bool   `json:"elevated"`

	Locale Locale     `json:"locale"`
	Paths  []PathInfo `json:"paths"`

	// Env carries only the variables that change where this program looks for
	// things. The full environment is not collected: it is long, it is where
	// people keep their API keys, and none of the rest of it has ever explained
	// a bug here.
	Env map[string]string `json:"env,omitempty"`

	// HomeRedacted records how the report was produced, so a reader who sees
	// "~" everywhere knows it was a choice rather than a path that genuinely
	// looks like that.
	HomeRedacted bool `json:"home_redacted"`
}

// Locale is the text-encoding environment. Its fields differ by platform and
// the empty ones are omitted, so a Unix report carries Env and a Windows report
// carries the code pages.
type Locale struct {
	// Unix
	Env       map[string]string `json:"env,omitempty"`
	Effective string            `json:"effective,omitempty"`

	// Windows
	ANSICodePage          int    `json:"ansi_code_page,omitempty"`
	OEMCodePage           int    `json:"oem_code_page,omitempty"`
	ConsoleOutputCodePage int    `json:"console_output_code_page,omitempty"`
	ConsoleInputCodePage  int    `json:"console_input_code_page,omitempty"`
	UserLocale            string `json:"user_locale,omitempty"`

	// Notes carries the interpretation, so the reader does not have to know
	// that 932 means backslashes render as ¥.
	Notes []string `json:"notes,omitempty"`
}

// envOfInterest are the variables that relocate this program's files.
var envOfInterest = []string{
	"GOBBONET_CONFIG",
	"GEMMA_CONFIG", // deprecated, still honoured — worth seeing when it is set
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"HOME",
	"USERPROFILE",
	"APPDATA",
	"LOCALAPPDATA",
	"TMPDIR",
	"TEMP",
}

func collectSystem(cfg config.Config, red redactor) System {
	s := System{
		OS:           runtime.GOOS,
		OSVersion:    osVersion(),
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		TotalRAM:     totalRAM(),
		Locale:       collectLocale(),
		HomeRedacted: red.enabled(),
		Env:          map[string]string{},
	}

	if h, err := os.Hostname(); err == nil {
		s.Hostname = h
	}
	if u, err := user.Current(); err == nil {
		s.User = u.Username
		s.UID = u.Uid
	}
	s.Elevated = elevated()

	for _, k := range envOfInterest {
		if v, ok := os.LookupEnv(k); ok {
			s.Env[k] = red.path(v)
		}
	}
	if len(s.Env) == 0 {
		s.Env = nil
	}

	s.Paths = collectPaths(cfg.Path, cfg.WebRoot, cfg.DataDir, cfg.ModelDir, cfg.ServerExe, red)
	return s
}
