package debugreport

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/version"
)

// Build is the identity of the binary that produced this report.
//
// "Which build are you on" is the first question on every bug report and the
// one users are worst at answering, because the honest answer is usually "the
// one I downloaded, I think". Origin below answers it from evidence instead: a
// release binary, a clean local build, and a build from a tree with uncommitted
// edits are three very different things to be debugging, and only the last one
// explains a symptom that no one else can reproduce.
type Build struct {
	// Version is the stamped release identity, or "dev" for an unstamped build.
	Version string `json:"version"`
	// Full is version.Full() — the string the startup banner prints, kept
	// verbatim so a pasted report and a pasted terminal line can be compared
	// character for character.
	Full string `json:"full"`

	// Origin is the verdict: "release", "source-clean", "source-modified" or
	// "source-unknown". See classifyOrigin.
	Origin string `json:"origin"`
	// OriginDetail explains the verdict in a sentence, for the markdown render.
	OriginDetail string `json:"origin_detail"`

	VCSRevision string `json:"vcs_revision,omitempty"`
	VCSTime     string `json:"vcs_time,omitempty"`
	VCSModified bool   `json:"vcs_modified"`

	// StampMismatch is set when the ldflags version and the toolchain's own VCS
	// stamp disagree about which commit this is. That means the release script
	// was run against a different tree than it claims, and every other field in
	// the report should be read with that in mind.
	StampMismatch string `json:"stamp_mismatch,omitempty"`

	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Settings carries the build flags that change behaviour between otherwise
	// identical versions — CGO in particular, since a CGO-less build cannot use
	// some backends.
	Settings map[string]string `json:"settings,omitempty"`

	ExePath     string `json:"exe_path,omitempty"`
	ExeSize     int64  `json:"exe_size,omitempty"`
	ExeModified string `json:"exe_modified,omitempty"`
}

// Origin values.
const (
	OriginRelease  = "release"
	OriginClean    = "source-clean"
	OriginModified = "source-modified"
	OriginUnknown  = "source-unknown"
)

// buildSettingsOfInterest are the keys worth carrying. The full list from
// ReadBuildInfo is long and mostly noise; these are the ones that have actually
// differed between a working and a broken install.
var buildSettingsOfInterest = []string{
	"-trimpath", "-tags", "CGO_ENABLED", "GOAMD64", "GOARM", "vcs",
}

func collectBuild() Build {
	b := Build{
		Version:   version.String(),
		Full:      version.Full(),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		want := map[string]bool{}
		for _, k := range buildSettingsOfInterest {
			want[k] = true
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.VCSRevision = s.Value
			case "vcs.time":
				b.VCSTime = s.Value
			case "vcs.modified":
				b.VCSModified = s.Value == "true"
			default:
				if want[s.Key] && s.Value != "" {
					if b.Settings == nil {
						b.Settings = map[string]string{}
					}
					b.Settings[s.Key] = s.Value
				}
			}
		}
	}

	b.Origin, b.OriginDetail = classifyOrigin(b)
	b.StampMismatch = stampMismatch(b)

	// The binary's own path and mtime catch the install that is not the install
	// the user thinks they are running: a second copy earlier on PATH, or a
	// months-old binary next to a freshly updated checkout.
	if exe, err := os.Executable(); err == nil {
		b.ExePath = exe
		if st, err := os.Stat(exe); err == nil {
			b.ExeSize = st.Size()
			b.ExeModified = st.ModTime().Format(time.RFC3339)
		}
	}

	return b
}

// classifyOrigin decides how this binary came to exist.
//
// version.Version defaults to "dev" and is overridden only by an -ldflags -X,
// which build-release.sh and the installer scripts both pass. So a stamped
// version is a distributed build and an unstamped one was compiled by whoever
// is running it. The VCS settings then separate a clean checkout from one with
// local edits, which is the "works on my machine" case stated as data.
func classifyOrigin(b Build) (origin, detail string) {
	if b.Version != "" && b.Version != "dev" {
		return OriginRelease, "stamped by the release script — a distributed build, not compiled locally"
	}
	switch {
	case b.VCSRevision == "":
		return OriginUnknown, "compiled from source with no VCS metadata (built outside a git checkout, or with -buildvcs=false)"
	case b.VCSModified:
		return OriginModified, "compiled from a git checkout with uncommitted changes — this binary does not match any published commit"
	default:
		return OriginClean, "compiled from a clean git checkout"
	}
}

// stampMismatch cross-checks the two independent records of which commit this
// is. Version carries a short sha as its suffix ("1.7.3-go-afb7e0d"); the
// toolchain stamps the real one. They disagree when the release script was run
// against a tree other than the one it named, which has happened, and which
// makes every "fixed in 1.7.3" answer wrong for that user.
func stampMismatch(b Build) string {
	if b.VCSRevision == "" || b.Version == "" || b.Version == "dev" {
		return ""
	}
	idx := strings.LastIndex(b.Version, "-go-")
	if idx < 0 {
		return ""
	}
	claimed := b.Version[idx+len("-go-"):]
	if claimed == "" {
		return ""
	}
	if strings.HasPrefix(b.VCSRevision, claimed) {
		return ""
	}
	short := b.VCSRevision
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("version stamp says %s but the build was made from %s", claimed, short)
}

// publicBuild strips a Build down to what the login page needs.
//
// Rebuilt field by field rather than blanked in place: a new field added to
// Build must be opted into here deliberately, instead of appearing in the
// unauthenticated payload because nobody remembered to remove it.
func publicBuild(b Build) Build {
	return Build{
		Version:      b.Version,
		Origin:       b.Origin,
		OriginDetail: b.OriginDetail,
		OS:           b.OS,
		Arch:         b.Arch,
	}
}
