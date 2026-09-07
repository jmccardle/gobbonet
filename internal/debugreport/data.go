package debugreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// Data fingerprints the user's saved state without reading any of it.
//
// Counts and key names only — never a thread name, a message, a character card
// or a persona. The report is written to be pasted in public, and the whole of
// what a maintainer needs from this section is shape: how much is there, which
// file it is in, and whether the file the server writes is the file the user's
// history is actually in.
type Data struct {
	DataDir string      `json:"data_dir"`
	Files   []StateFile `json:"files"`

	// SchemaVersion is deliberately absent from the state format today, and
	// saying so is more useful than omitting the field: it tells a reader that
	// "is this state from an older GobboNet?" cannot be answered directly and
	// has to be inferred from the key set below.
	SchemaVersionSupported bool `json:"schema_version_supported"`

	// Findings are the conclusions worth drawing from the files, chiefly the
	// stranded-legacy-state case described in checkStranded.
	Findings []string `json:"findings,omitempty"`
}

// StateFile is one state document on disk.
type StateFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Role     string `json:"role"` // "live" | "legacy"
	Exists   bool   `json:"exists"`
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
	Error    string `json:"error,omitempty"`

	// Keys is the top-level key set, sorted. This is the only handle on schema
	// drift there is: a state file written by an older build is recognised by
	// which keys it does and does not have.
	Keys []string `json:"keys,omitempty"`

	Threads        int `json:"threads"`
	Messages       int `json:"messages"`
	CharacterCards int `json:"character_cards"`
	PersonaCards   int `json:"persona_cards"`
	Folders        int `json:"folders"`
	Schedules      int `json:"schedules"`
	Extensions     int `json:"extensions"`
	Macros         int `json:"macros"`
}

// legacyStateName is what the PowerShell era wrote. The Go server writes
// state.json beside it and never reads this one, so an install upgraded from
// that era has its entire history in a file nothing loads.
const legacyStateName = ".gobbonet-state.json"

func collectData(cfg config.Config, red redactor) Data {
	d := Data{
		DataDir:                red.path(cfg.DataDir),
		SchemaVersionSupported: false,
	}

	live := readStateFile(cfg.StatePath(), "live", red)
	legacy := readStateFile(filepath.Join(cfg.DataDir, legacyStateName), "legacy", red)
	d.Files = []StateFile{live, legacy}

	d.Findings = checkStranded(live, legacy)
	return d
}

// checkStranded looks for history the running server cannot see.
//
// This is the failure it catches: a user upgrades from the PowerShell build,
// the Go server starts with an empty state.json, and their conversations are
// still sitting in .gobbonet-state.json where nothing will ever load them. From
// the user's side that looks like the upgrade deleted everything, and the file
// is right there the whole time.
func checkStranded(live, legacy StateFile) []string {
	var out []string
	if !legacy.Exists {
		return nil
	}
	if legacy.Messages > live.Messages {
		out = append(out, "a legacy state file holds more history than the live one "+
			"("+itoa(legacy.Messages)+" messages vs "+itoa(live.Messages)+"). "+
			"The Go server only reads state.json, so this history is present on disk "+
			"but not loaded. It has not been deleted.")
	} else {
		out = append(out, "a legacy state file from the PowerShell build is still present. "+
			"It is not read by this server; it holds no more history than the live file.")
	}
	return out
}

func readStateFile(path, role string, red redactor) StateFile {
	f := StateFile{
		Name: filepath.Base(path),
		Path: red.path(path),
		Role: role,
	}

	st, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			f.Error = red.text(err.Error())
		}
		return f
	}
	f.Exists = true
	f.Size = st.Size()
	f.Modified = st.ModTime().Format(time.RFC3339)

	raw, err := os.ReadFile(path)
	if err != nil {
		f.Error = red.text(err.Error())
		return f
	}

	// Decoded one key at a time rather than into a struct of typed slices.
	//
	// The state format is whatever the browser last serialised, and it is not
	// uniform: `extensions` is an object keyed by id while its neighbours are
	// arrays. A struct that assumed arrays throughout failed the whole document
	// on that one field and reported an install with 47 messages as empty —
	// which is worse than no report, because "your history is gone" is a
	// conclusion someone would act on. So each collection is counted
	// independently, and a key that cannot be read is named instead of taking
	// the rest of the file down with it.
	shallow := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &shallow); err != nil {
		// A state file that will not parse at all IS the finding: the server
		// would have started with empty state, and the user would report having
		// lost everything.
		f.Error = "does not parse as JSON: " + red.text(err.Error())
		return f
	}

	f.Keys = sortedKeys(shallow)
	f.Threads, f.Messages = countThreads(shallow["threads"])
	f.CharacterCards = countCollection(shallow["characterCards"])
	f.PersonaCards = countCollection(shallow["personaCards"])
	f.Folders = countCollection(shallow["folders"])
	f.Schedules = countCollection(shallow["schedules"])
	f.Extensions = countCollection(shallow["extensions"])
	f.Macros = countCollection(shallow["macros"])

	return f
}

// countThreads returns the thread count and the total message count across
// them. The message bodies are decoded as raw JSON and never inspected, so
// nothing a user wrote can escape into the report through this function.
func countThreads(raw json.RawMessage) (threads, messages int) {
	if len(raw) == 0 {
		return 0, 0
	}
	var list []struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		// Tolerated the same way as any other collection: a threads value in an
		// unexpected shape still has a countable length.
		return countCollection(raw), 0
	}
	for _, t := range list {
		messages += countCollection(t.Messages)
	}
	return len(list), messages
}

// countCollection returns the number of elements in a JSON array or the number
// of entries in a JSON object, and -1 for anything else.
//
// Both shapes occur in the same document, so accepting either is the only way
// to count these keys without knowing in advance which the browser used for
// each — and which it uses has already changed once.
func countCollection(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		return len(arr)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return len(obj)
	}
	return -1
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func itoa(n int) string { return strconv.Itoa(n) }
