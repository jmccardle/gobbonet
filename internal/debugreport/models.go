package debugreport

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/config"
	"github.com/ElodineOfficial/GobboNet/internal/modelfetch"
	"github.com/ElodineOfficial/GobboNet/internal/models"
)

// Models is the local model library as the classifier sees it.
//
// The classifier's verdict matters more than the file listing. Whether a GGUF
// runs with --jinja, which chat template it gets, and what context it claims
// are all decided here, and a model that classifies wrongly produces a server
// that starts fine and then answers in a garbled or repetitive way — a symptom
// nobody thinks to blame on classification. Reporting the verdict alongside the
// filename makes that visible.
type Models struct {
	Dir       string      `json:"dir"`
	Usable    bool        `json:"usable"`
	Error     string      `json:"error,omitempty"`
	FreeBytes int64       `json:"free_bytes,omitempty"`
	Files     []ModelFile `json:"files"`
	// Partial lists leftovers from an interrupted download. A .part beside a
	// missing model is the answer to "the download said it finished".
	Partial []string `json:"partial,omitempty"`
}

// ModelFile is one GGUF and what the classifier made of it.
type ModelFile struct {
	File     string `json:"file"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`

	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Family   string `json:"family,omitempty"`
	MaxCtx   int    `json:"max_ctx,omitempty"`
	UseJinja int    `json:"use_jinja"`
	// ChatTemplate is the built-in template NAME, ChatTemplateFile a sidecar
	// path. They are not interchangeable — passing a path where a name is
	// expected makes llama-server treat the path text as the template body —
	// so both are reported rather than merged.
	ChatTemplate     string `json:"chat_template,omitempty"`
	ChatTemplateFile string `json:"chat_template_file,omitempty"`
	ThinkingFormat   string `json:"thinking_format,omitempty"`
	Active           bool   `json:"active"`
}

func collectModels(cfg config.Config, red redactor) Models {
	m := Models{
		Dir:    red.path(cfg.ModelDir),
		Usable: cfg.ModelDirUsable(),
	}

	if cfg.ModelDir == "" {
		m.Error = "model_dir is not set"
		return m
	}
	if !m.Usable {
		// Not fatal in remote mode, where the models live on somebody else's
		// machine and an empty local directory is correct.
		m.Error = "model_dir does not exist or is not a directory"
	}

	m.FreeBytes = modelfetch.FreeBytes(cfg.ModelDir)

	for _, rec := range models.ScanDir(cfg.ModelDir) {
		f := ModelFile{
			File:             rec.File,
			ID:               rec.ID,
			Name:             rec.Name,
			Family:           rec.Family,
			MaxCtx:           rec.MaxCtx,
			UseJinja:         rec.UseJinja,
			ChatTemplate:     rec.ChatTemplate,
			ChatTemplateFile: red.path(rec.ChatTemplateFile),
			ThinkingFormat:   rec.ThinkingFormat,
			Active:           rec.Active,
		}
		if st, err := os.Stat(filepath.Join(cfg.ModelDir, rec.File)); err == nil {
			f.Size = st.Size()
			f.Modified = st.ModTime().Format(time.RFC3339)
		}
		m.Files = append(m.Files, f)
	}

	m.Partial = partialDownloads(cfg.ModelDir)
	return m
}

// partialDownloads finds the temporary files a download leaves behind.
func partialDownloads(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".tmp") ||
			strings.HasSuffix(lower, ".download") {
			out = append(out, name)
		}
	}
	return out
}
