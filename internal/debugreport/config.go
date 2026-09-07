package debugreport

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/auth"
	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// ConfigSection is the effective configuration, with the two secrets removed.
//
// Entries carry their default alongside the live value and a Changed flag. The
// changed subset is short — usually three or four keys — and it is almost
// always where the bug is, so the render leads with it rather than making a
// reader diff two dozen lines by eye.
type ConfigSection struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Modified string `json:"modified,omitempty"`

	// Mode is "local" (this process supervises llama-server and hot-swap works)
	// or "remote" (llama.cpp is somebody else's, we only proxy). Nearly every
	// question about model switching has a different answer in each.
	Mode      string `json:"mode"`
	ModeError string `json:"mode_error,omitempty"`

	// PerfOverridden reports that a perf.toml was found and applied on top of
	// config.toml, which is how a value in the file can differ from the value
	// in force. Auto* below are the pre-overlay baseline.
	PerfOverridden  bool   `json:"perf_overridden"`
	AutoCtxSize     int    `json:"auto_ctx_size,omitempty"`
	AutoGPULayers   int    `json:"auto_gpu_layers,omitempty"`
	AutoKVCacheType string `json:"auto_kv_cache_type,omitempty"`

	Entries []ConfigEntry `json:"entries"`

	// Deferred are misconfigurations recorded at load time that would stop the
	// server starting. On a report from a machine where `serve` refuses to run,
	// this is the answer, and it is otherwise only visible in a terminal the
	// user has already closed.
	Deferred []string `json:"deferred,omitempty"`
}

// ConfigEntry is one setting.
type ConfigEntry struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Default string `json:"default,omitempty"`
	Changed bool   `json:"changed"`
	// Redacted marks a value replaced by a description of itself. The
	// description is deliberately informative — "set, 51 chars" distinguishes a
	// key that is present from one that is empty, which is the actual question,
	// without carrying the key.
	Redacted bool `json:"redacted,omitempty"`
}

func collectConfig(cfg config.Config, red redactor) ConfigSection {
	def := config.Default()

	sec := ConfigSection{
		Path:            red.path(cfg.Path),
		PerfOverridden:  cfg.PerfOverridden,
		AutoCtxSize:     cfg.AutoCtxSize,
		AutoGPULayers:   cfg.AutoGPULayers,
		AutoKVCacheType: cfg.AutoKVCacheType,
	}

	if st, err := os.Stat(cfg.Path); err == nil {
		sec.Exists = true
		sec.Modified = st.ModTime().Format(time.RFC3339)
	}

	mode, err := cfg.Mode()
	sec.Mode = string(mode)
	if err != nil {
		sec.ModeError = red.text(err.Error())
	}

	for _, e := range cfg.Deferred {
		sec.Deferred = append(sec.Deferred, red.text(e.Error()))
	}

	sec.Entries = configEntries(cfg, def, red)
	return sec
}

// configEntries lists the settings explicitly rather than by reflection.
//
// Reflection would pick up every field automatically, which sounds like an
// advantage until a future field holding a credential is added and appears in
// everybody's pasted report without anyone deciding it should. An explicit list
// makes inclusion a choice; the test in config_test.go holds this list against
// the struct's toml tags so a new field cannot be silently forgotten either.
func configEntries(cfg, def config.Config, red redactor) []ConfigEntry {
	str := func(s string) string { return s }
	path := red.path
	num := func(i int) string { return strconv.Itoa(i) }
	boolean := func(b bool) string { return strconv.FormatBool(b) }
	list := func(v []string) string { return strings.Join(v, ", ") }

	e := []ConfigEntry{
		entry("llm_url", str(cfg.LLMURL), str(def.LLMURL)),
		entry("search_url", str(cfg.SearchURL), str(def.SearchURL)),
		entry("embed_url", str(cfg.EmbedURL), str(def.EmbedURL)),

		secret("llm_api_key", cfg.LLMAPIKey),
		entry("llm_api_key_file", path(cfg.LLMAPIKeyFile), ""),

		entry("listen_host", str(cfg.ListenHost), str(def.ListenHost)),
		entry("listen_port", num(cfg.ListenPort), num(def.ListenPort)),
		entry("allowed_hosts", list(cfg.AllowedHosts), ""),

		entry("server_exe", path(cfg.ServerExe), ""),
		entry("gpu_layers", num(cfg.GPULayers), num(def.GPULayers)),
		entry("ctx_size", num(cfg.CtxSize), num(def.CtxSize)),
		entry("kv_cache_type", str(cfg.KVCacheType), str(def.KVCacheType)),

		entry("model_dir", path(cfg.ModelDir), ""),
		entry("web_root", path(cfg.WebRoot), ""),
		entry("data_dir", path(cfg.DataDir), ""),

		secretFormat("access_secret", cfg.AccessSecret),

		entry("model_catalog_url", str(cfg.ModelCatalogURL), str(def.ModelCatalogURL)),
		entry("model_catalog_remote", boolean(cfg.ModelCatalogRemote), boolean(def.ModelCatalogRemote)),
		entry("require_checksum", boolean(cfg.RequireChecksum), boolean(def.RequireChecksum)),

		entry("chat_template_name", str(cfg.ChatTemplateName), ""),
		entry("chat_template_file", path(cfg.ChatTemplateFile), ""),

		entry("session_ttl_hours", num(cfg.SessionTTLHours), num(def.SessionTTLHours)),
		entry("job_max_concurrent", num(cfg.JobMaxConcurrent), num(def.JobMaxConcurrent)),
		entry("job_max_age_hours", num(cfg.JobMaxAgeHours), num(def.JobMaxAgeHours)),

		// Not from the file, but it changes whether anything is gated at all,
		// so it belongs next to the settings that are.
		entry("require_auth (--no-auth)", boolean(cfg.RequireAuth), "true"),
	}
	return e
}

func entry(key, value, def string) ConfigEntry {
	return ConfigEntry{
		Key:     key,
		Value:   value,
		Default: def,
		Changed: value != def,
	}
}

// secret replaces a credential with its shape. Length is included because a
// key with a stray newline or a truncated paste is a real and common failure,
// and the length is the only way to see it without seeing the key.
func secret(key, value string) ConfigEntry {
	v := "unset"
	if value != "" {
		v = fmt.Sprintf("set, %d chars", len(value))
		if strings.TrimSpace(value) != value {
			v += " (has leading or trailing whitespace — likely a paste error)"
		}
	}
	return ConfigEntry{Key: key, Value: v, Changed: value != "", Redacted: true}
}

// secretFormat reports which hashing scheme the stored password uses.
//
// The format matters and the hash does not. A legacy salt:hash still verifies
// and rewrites itself as Argon2id on the next successful login, so a report
// showing "legacy-sha256" after several logins means something is stopping that
// migration — which is a bug, and invisible any other way.
func secretFormat(key, value string) ConfigEntry {
	return ConfigEntry{
		Key:      key,
		Value:    describeSecret(value),
		Changed:  value != "",
		Redacted: true,
	}
}

func describeSecret(secret string) string {
	switch {
	case strings.TrimSpace(secret) == "":
		return SecretNone
	case strings.HasPrefix(strings.TrimSpace(secret), "$argon2id$"):
		return SecretArgon2id
	case auth.SecretConfigured(secret):
		return SecretLegacy
	default:
		// Unreachable on a running server — ensurePassword refuses to start on
		// a malformed secret — but reachable from `gobbonet debug-report` on an
		// install that will not start, which is exactly when it needs saying.
		return "malformed (neither salt:hash nor Argon2id — run: gobbonet set-password)"
	}
}

// collectAuth builds the gate summary shared by both tiers.
func collectAuth(cfg config.Config, session string) Auth {
	format := describeSecret(cfg.AccessSecret)
	required := cfg.RequireAuth && auth.SecretConfigured(cfg.AccessSecret)
	return Auth{
		Required:     required,
		SecretFormat: format,
		Session:      session,
		Login:        "/login",
	}
}
