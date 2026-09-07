package debugreport

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// TestEveryConfigFieldIsAccountedFor holds the explicit entry list in
// configEntries against the struct it describes.
//
// The list is written out by hand so that including a field is a decision
// rather than a default — a future setting holding a credential must not reach
// everybody's pasted report because reflection swept it up. The cost of that
// choice is that a new field can be forgotten instead, and it would then be
// missing from every report with nothing to say so. This test pays that cost:
// add a toml-tagged field to config.Config and it fails until the field is
// either reported or listed below as deliberately omitted.
func TestEveryConfigFieldIsAccountedFor(t *testing.T) {
	// Omitted on purpose, with the reason. Nothing else may be absent.
	deliberatelyOmitted := map[string]string{
		"access_secret": "reported by secretFormat as a format name, never as a value",
		"llm_api_key":   "reported by secret as a shape, never as a value",
	}

	var tagged []string
	rt := reflect.TypeOf(config.Config{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		tagged = append(tagged, tag)
	}

	reported := map[string]bool{}
	for _, e := range configEntries(config.Default(), config.Default(), redactor{}) {
		// The require_auth row is annotated with its flag name; match on the
		// key itself.
		key, _, _ := strings.Cut(e.Key, " ")
		reported[key] = true
	}

	var missing []string
	for _, tag := range tagged {
		if reported[tag] {
			continue
		}
		if _, ok := deliberatelyOmitted[tag]; ok {
			continue
		}
		missing = append(missing, tag)
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("config fields present in config.toml but absent from the debug report: %v\n\n"+
			"Add them to configEntries, or record why they are omitted in "+
			"deliberatelyOmitted above. A setting nobody reports is a setting "+
			"nobody can debug.", missing)
	}
}

// TestChangedFlagIsSetAgainstDefaults is what makes the "changed from defaults"
// table worth leading with: a stock config must produce an empty one.
func TestChangedFlagIsSetAgainstDefaults(t *testing.T) {
	def := config.Default()

	for _, e := range configEntries(def, def, redactor{}) {
		switch e.Key {
		case "access_secret", "llm_api_key":
			continue // both are unset in Default(), and their Changed flag means "is set"
		case "require_auth (--no-auth)":
			continue // Default() leaves RequireAuth true only after normalise
		}
		if e.Changed {
			t.Errorf("%s reported as changed on a stock config: value=%q default=%q",
				e.Key, e.Value, e.Default)
		}
	}

	modified := def
	modified.LLMURL = "http://192.168.0.5:8080"
	modified.CtxSize = 4096

	changed := map[string]bool{}
	for _, e := range configEntries(modified, def, redactor{}) {
		if e.Changed {
			changed[e.Key] = true
		}
	}
	for _, want := range []string{"llm_url", "ctx_size"} {
		if !changed[want] {
			t.Errorf("%s was edited but not reported as changed", want)
		}
	}
}

func TestCollectAuthReflectsTheGate(t *testing.T) {
	cfg := config.Default()
	cfg.RequireAuth = true
	cfg.AccessSecret = "deadbeef:cafebabe"

	a := collectAuth(cfg, SessionStale)
	if !a.Required {
		t.Error("a configured secret with RequireAuth should report required")
	}
	if a.SecretFormat != SecretLegacy {
		t.Errorf("secret format = %q, want %q", a.SecretFormat, SecretLegacy)
	}
	if a.Session != SessionStale {
		t.Errorf("session = %q", a.Session)
	}

	// --no-auth: the gate is off even though a secret is stored, and the report
	// has to say so or the user is debugging a lock they do not have.
	cfg.RequireAuth = false
	if collectAuth(cfg, SessionValid).Required {
		t.Error("--no-auth must report required=false")
	}
}
