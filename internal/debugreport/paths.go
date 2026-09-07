package debugreport

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/ElodineOfficial/GobboNet/internal/modelfetch"
)

// PathInfo is one filesystem location, examined rather than assumed.
//
// The Bytes field is the reason this type exists. A reporter on a Japanese
// locale sees their path printed with ¥ where the backslashes are and
// reasonably concludes their paths are corrupted; in CP932 the byte 0x5C simply
// renders as ¥, and the path is fine. Nothing in a screenshot can settle that.
// The raw bytes can, in one line, which turns a multi-message thread into a
// glance.
type PathInfo struct {
	Label string `json:"label"`
	Raw   string `json:"raw"`

	// NonASCII marks a path that any encoding bug could be hiding in. Bytes is
	// filled only for those: hex for every path would be unreadable noise, and
	// an all-ASCII path has nothing to disambiguate.
	NonASCII bool   `json:"non_ascii"`
	Bytes    string `json:"bytes,omitempty"`
	// ValidUTF8 is false when the string is not decodable at all — a path that
	// came out of a file written in a legacy codepage, which is a real
	// corruption rather than a display artefact.
	ValidUTF8 bool `json:"valid_utf8"`

	Absolute bool `json:"absolute"`
	Exists   bool `json:"exists"`
	IsDir    bool `json:"is_dir"`
	// Writable is tested by writing, not inferred from mode bits. Mode bits lie
	// on Windows, on network shares, and under any of the several ways a
	// directory can be read-only while looking writable.
	Writable bool   `json:"writable"`
	Error    string `json:"error,omitempty"`

	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`

	// FreeBytes is the space available on the volume holding this path. Zero
	// means the check could not answer, not that the disk is full.
	FreeBytes int64 `json:"free_bytes,omitempty"`

	// Volume and VolumeType are Windows-only and are the other half of the
	// screenshot case: "The system cannot find the drive specified" against a
	// path on F: is explained instantly by F: being a network mapping (which is
	// per-logon-session, so a service cannot see it) or removable media that is
	// not currently attached.
	Volume     string `json:"volume,omitempty"`
	VolumeType string `json:"volume_type,omitempty"`
}

// examinePath gathers everything knowable about one path without changing it.
// An empty path is reported as such rather than examined, because "" resolves
// to the working directory and would produce a confidently wrong answer.
func examinePath(label, p string, red redactor) PathInfo {
	info := PathInfo{
		Label:     label,
		Raw:       red.path(p),
		ValidUTF8: utf8.ValidString(p),
	}
	if p == "" {
		info.Error = "not set"
		return info
	}

	info.Absolute = filepath.IsAbs(p)
	info.NonASCII = !isASCII(p)
	if info.NonASCII || !info.ValidUTF8 {
		// Hex of the redacted form, so a user who asked for home redaction does
		// not get their username back in the bytes. The evidence survives:
		// whatever is non-ASCII in the remainder is still visible.
		info.Bytes = hex.EncodeToString([]byte(info.Raw))
	}

	info.Volume, info.VolumeType = volumeInfo(p)

	st, err := os.Stat(p)
	switch {
	case err == nil:
		info.Exists = true
		info.IsDir = st.IsDir()
		info.Size = st.Size()
		info.Modified = st.ModTime().Format(time.RFC3339)
	case os.IsNotExist(err):
		// Not an error worth shouting about — half these paths are created on
		// demand — but the reader needs to know it is absent right now.
		info.Error = "does not exist"
	default:
		info.Error = red.text(err.Error())
	}

	if info.IsDir {
		info.Writable = dirWritable(p)
		info.FreeBytes = modelfetch.FreeBytes(p)
	} else if info.Exists {
		info.Writable = fileWritable(p)
		info.FreeBytes = modelfetch.FreeBytes(filepath.Dir(p))
	} else {
		// Still worth reporting for a directory that does not exist yet: a
		// download about to write 15 GB into it fails the same way whether the
		// directory is missing or the volume is full, and FreeBytes walks up to
		// the nearest existing ancestor for exactly this case.
		info.FreeBytes = modelfetch.FreeBytes(p)
	}

	return info
}

// dirWritable answers by creating and removing a file. The name is explicit so
// that a leftover after a crash is self-explaining rather than mysterious.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".gobbonet-writetest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func fileWritable(p string) bool {
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// collectPaths examines every location the server depends on, in the order a
// reader would check them.
func collectPaths(cfgPath, webRoot, dataDir, modelDir, serverExe string, red redactor) []PathInfo {
	out := []PathInfo{
		examinePath("config", cfgPath, red),
		examinePath("web_root", webRoot, red),
		examinePath("data_dir", dataDir, red),
		examinePath("model_dir", modelDir, red),
	}
	// server_exe is empty in remote mode, where its absence is correct and
	// reporting it as a missing file would send the reader down a dead end.
	if serverExe != "" {
		out = append(out, examinePath("server_exe", serverExe, red))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, examinePath("working_dir", cwd, red))
	}
	return out
}

// humanBytes renders a size for the markdown view. Kept here so the JSON stays
// numeric and only the human-facing render is formatted.
func humanBytes(n int64) string {
	if n <= 0 {
		return "unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// shortHex trims a hex dump for display. The full value stays in the JSON.
func shortHex(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
