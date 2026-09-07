package debugreport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// publicTierAllowedKeys is the exact top-level key set an unauthenticated
// caller may receive.
//
// Asserted as an equality rather than a subset on purpose. A new field added to
// Report will appear in the full payload automatically, and the only thing
// standing between that and it also appearing in the pre-login payload is this
// test failing. Growing the list is a deliberate edit; forgetting to shrink it
// is not something anyone will notice by reading the diff.
var publicTierAllowedKeys = []string{
	"auth", "build", "generated", "schema", "tier",
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.DataDir = dir
	cfg.WebRoot = dir
	cfg.ModelDir = filepath.Join(dir, "models")
	cfg.RequireAuth = true
	cfg.AccessSecret = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGg"
	cfg.LLMAPIKey = "sk-do-not-leak-this-value-anywhere"
	return cfg
}

func TestPublicTierKeysAreExactlyTheAllowlist(t *testing.T) {
	r := Collect(context.Background(), Options{
		Cfg:     testConfig(t),
		Tier:    TierPublic,
		Session: SessionStale,
	})

	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if !reflect.DeepEqual(keys, publicTierAllowedKeys) {
		t.Errorf("public tier key set changed.\n got: %v\nwant: %v\n\n"+
			"If a new field belongs in the unauthenticated payload, add it to "+
			"publicTierAllowedKeys deliberately. If it does not, make sure Collect "+
			"returns before it is populated.", keys, publicTierAllowedKeys)
	}
}

// TestPublicTierRunsNoProbes is the property that keeps an anonymous request
// from making this server open outbound connections. Probe and TestPrompt are
// both set here and both must be ignored.
func TestPublicTierRunsNoProbes(t *testing.T) {
	cfg := testConfig(t)
	// A port nothing is listening on. If a probe ran, Network would be non-nil
	// and carry the connection error.
	cfg.LLMURL = "http://127.0.0.1:1"

	r := Collect(context.Background(), Options{
		Cfg:        cfg,
		Tier:       TierPublic,
		Probe:      true,
		TestPrompt: true,
	})

	if r.Network != nil {
		t.Fatal("public tier ran network probes; an unauthenticated caller must " +
			"never be able to make this server dial out")
	}
	if r.System != nil || r.Config != nil || r.Data != nil || r.Models != nil {
		t.Error("public tier carried a section that names filesystem paths")
	}
}

// TestSecretsNeverAppearInOutput is the one redaction guarantee that is not
// negotiable: whatever else a report carries, it does not carry credentials.
func TestSecretsNeverAppearInOutput(t *testing.T) {
	cfg := testConfig(t)

	for _, tier := range []string{TierFull, TierPublic} {
		r := Collect(context.Background(), Options{Cfg: cfg, Tier: tier})

		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, form := range []string{string(raw), r.Markdown()} {
			if strings.Contains(form, cfg.LLMAPIKey) {
				t.Errorf("%s tier leaked the API key", tier)
			}
			// The full Argon2id string embeds the salt and hash. Checking for
			// the hash segment catches a naive "print the config" regression.
			if strings.Contains(form, "aGFzaGhhc2g") {
				t.Errorf("%s tier leaked the access secret hash", tier)
			}
		}
	}
}

func TestFullTierDescribesSecretsWithoutCarryingThem(t *testing.T) {
	cfg := testConfig(t)
	r := Collect(context.Background(), Options{Cfg: cfg, Tier: TierFull})

	found := map[string]ConfigEntry{}
	for _, e := range r.Config.Entries {
		found[e.Key] = e
	}

	if got := found["llm_api_key"]; !got.Redacted || !strings.HasPrefix(got.Value, "set,") {
		t.Errorf("llm_api_key should be described, got %+v", got)
	}
	if got := found["access_secret"]; got.Value != SecretArgon2id {
		t.Errorf("access_secret format = %q, want %q", got.Value, SecretArgon2id)
	}
}

// TestSecretWhitespaceIsFlagged covers the paste error that produces a key
// which looks right and authenticates as garbage.
func TestSecretWhitespaceIsFlagged(t *testing.T) {
	e := secret("llm_api_key", "sk-abc123\n")
	if !strings.Contains(e.Value, "whitespace") {
		t.Errorf("trailing newline in an API key was not flagged: %q", e.Value)
	}
}

func TestDescribeSecretFormats(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", SecretNone},
		{"   ", SecretNone},
		{"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA", SecretArgon2id},
		{"deadbeef:cafebabe", SecretLegacy},
		{"not-a-secret-at-all", "malformed"},
	}
	for _, c := range cases {
		got := describeSecret(c.in)
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("describeSecret(%q) = %q, want prefix %q", c.in, got, c.want)
		}
	}
}

// TestStateCountingToleratesMixedShapes is the regression for the bug this
// collector found in itself the first time it ran: `extensions` is a JSON
// object while every key beside it is an array, and a decoder that assumed
// arrays reported a state file holding 47 messages as empty.
func TestStateCountingToleratesMixedShapes(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.DataDir = dir

	state := `{
      "threads": [
        {"id":"a","messages":[{"role":"user"},{"role":"assistant"}]},
        {"id":"b","messages":[{"role":"user"}]}
      ],
      "characterCards": [{"id":"c1"},{"id":"c2"}],
      "personaCards": [{"id":"p1"}],
      "extensions": {"one":{},"two":{},"three":{}},
      "folders": [],
      "macros": [{"a":1}]
    }`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	d := collectData(cfg, redactor{})
	live := d.Files[0]

	if live.Error != "" {
		t.Fatalf("state.json failed to parse: %s", live.Error)
	}
	if live.Threads != 2 {
		t.Errorf("threads = %d, want 2", live.Threads)
	}
	if live.Messages != 3 {
		t.Errorf("messages = %d, want 3", live.Messages)
	}
	if live.Extensions != 3 {
		t.Errorf("extensions (a JSON object) = %d, want 3", live.Extensions)
	}
	if live.CharacterCards != 2 {
		t.Errorf("characterCards = %d, want 2", live.CharacterCards)
	}
}

// TestStrandedLegacyStateIsReported covers the upgrade case where the whole of
// a user's history sits in a file the Go server never reads.
func TestStrandedLegacyStateIsReported(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.DataDir = dir

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("state.json", `{"threads":[]}`)
	write(legacyStateName, `{"threads":[{"messages":[{"a":1},{"b":2},{"c":3}]}]}`)

	d := collectData(cfg, redactor{})
	if len(d.Findings) == 0 {
		t.Fatal("no finding for a legacy file holding more history than the live one")
	}
	if !strings.Contains(d.Findings[0], "3 messages vs 0") {
		t.Errorf("finding did not quantify the difference: %q", d.Findings[0])
	}
	if !strings.Contains(d.Findings[0], "has not been deleted") {
		t.Errorf("finding should reassure that nothing was lost: %q", d.Findings[0])
	}
}

func TestRedactorRewritesHomeOnly(t *testing.T) {
	r := redactor{home: "/home/someone"}

	if got := r.path("/home/someone/.config/gobbonet"); got != "~/.config/gobbonet" {
		t.Errorf("path = %q", got)
	}
	if got := r.path("/opt/models/foo.gguf"); got != "/opt/models/foo.gguf" {
		t.Errorf("unrelated path was rewritten: %q", got)
	}
	if got := r.text("failed to open /home/someone/m.gguf: no such file"); !strings.Contains(got, "~/m.gguf") {
		t.Errorf("free text not rewritten: %q", got)
	}
	if (redactor{}).path("/home/someone/x") != "/home/someone/x" {
		t.Error("disabled redactor must not rewrite anything")
	}
}

func TestNonASCIIPathCarriesItsBytes(t *testing.T) {
	dir := t.TempDir()
	// A directory name that is valid UTF-8 but not ASCII, which is the shape of
	// the reports this field exists for.
	nonASCII := filepath.Join(dir, "モデル")
	if err := os.Mkdir(nonASCII, 0o755); err != nil {
		t.Fatal(err)
	}

	p := examinePath("model_dir", nonASCII, redactor{})
	if !p.NonASCII {
		t.Fatal("non-ASCII path not flagged")
	}
	if p.Bytes == "" {
		t.Fatal("no hex dump for a non-ASCII path — this is the field that " +
			"distinguishes a display artefact from real corruption")
	}
	if !p.ValidUTF8 {
		t.Error("valid UTF-8 path reported as invalid")
	}
	if !p.Exists || !p.IsDir || !p.Writable {
		t.Errorf("path facts wrong: %+v", p)
	}

	ascii := examinePath("config", filepath.Join(dir, "config.toml"), redactor{})
	if ascii.NonASCII || ascii.Bytes != "" {
		t.Error("an ASCII path should carry no hex dump")
	}
}

func TestClassifyOrigin(t *testing.T) {
	cases := []struct {
		name string
		in   Build
		want string
	}{
		{"stamped release", Build{Version: "1.7.3-go-abc1234"}, OriginRelease},
		{"clean checkout", Build{Version: "dev", VCSRevision: "abc1234"}, OriginClean},
		{"modified tree", Build{Version: "dev", VCSRevision: "abc1234", VCSModified: true}, OriginModified},
		{"no vcs", Build{Version: "dev"}, OriginUnknown},
	}
	for _, c := range cases {
		if got, _ := classifyOrigin(c.in); got != c.want {
			t.Errorf("%s: origin = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStampMismatchDetected(t *testing.T) {
	mismatched := Build{Version: "1.7.3-go-abc1234", VCSRevision: "9999999deadbeef"}
	if stampMismatch(mismatched) == "" {
		t.Error("a version stamp naming a different commit than the build should be flagged")
	}

	agreeing := Build{Version: "1.7.3-go-abc1234", VCSRevision: "abc1234def5678"}
	if got := stampMismatch(agreeing); got != "" {
		t.Errorf("agreeing stamps flagged as a mismatch: %q", got)
	}
}

// TestMarkdownSurvivesPipesAndNewlines guards the render against error text
// that would otherwise break the table it is printed in. llama.cpp error bodies
// are multi-line and do contain pipes.
func TestMarkdownSurvivesPipesAndNewlines(t *testing.T) {
	r := Report{
		Schema: SchemaVersion,
		Tier:   TierFull,
		Build:  Build{Version: "dev", Origin: OriginClean},
		Network: &Network{
			Verdict: "upstream did not answer",
			Hops: []Hop{{
				Name:  "llm /health",
				URL:   "http://x/health",
				Error: "line one | with a pipe\nline two",
			}},
		},
	}
	md := r.Markdown()
	if strings.Contains(md, "pipe\nline two") {
		t.Error("a newline inside a table cell would end the row")
	}
	if !strings.Contains(md, `\|`) {
		t.Error("a pipe inside a table cell was not escaped")
	}
}

func TestCollectNotesMissingSections(t *testing.T) {
	r := Collect(context.Background(), Options{Cfg: testConfig(t), Tier: TierFull})

	if r.Runtime != nil {
		t.Error("no RuntimeSource was supplied, so Runtime must be nil")
	}
	if len(r.Notes) == 0 {
		t.Fatal("a missing section must leave a note saying why — a silent gap " +
			"reads as 'nothing wrong here'")
	}
	joined := strings.Join(r.Notes, "\n")
	for _, want := range []string{"runtime", "network"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no note explaining the missing %s section: %v", want, r.Notes)
		}
	}
}

func TestRuntimeNoteIsOverridable(t *testing.T) {
	const custom = "runtime: a server IS listening but was not asked"
	r := Collect(context.Background(), Options{
		Cfg:         testConfig(t),
		Tier:        TierFull,
		RuntimeNote: custom,
	})
	if !strings.Contains(strings.Join(r.Notes, "\n"), custom) {
		t.Errorf("RuntimeNote was ignored: %v", r.Notes)
	}
}
