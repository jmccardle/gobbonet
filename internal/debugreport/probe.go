package debugreport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ElodineOfficial/GobboNet/internal/config"
)

// Network is the connectivity picture, reported hop by hop.
//
// "Error: 502" is true and useless: it says the proxy did not get an answer,
// not which link failed. There are three links — browser to gobbonet, gobbonet
// to llama.cpp, gobbonet to the optional search and embedding services — and
// the browser-to-gobbonet one is proved by the report arriving at all. So this
// section exists to distinguish the remaining two, and to say which of several
// upstream shapes is on the other end.
type Network struct {
	Hops []Hop `json:"hops"`

	// Verdict is the one-line reading of the hops, in the terms a user would
	// use. Derived, never a substitute for the hops themselves.
	Verdict string `json:"verdict"`

	TestCompletion *TestCompletion `json:"test_completion,omitempty"`
}

// Hop is one probe.
type Hop struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	// Body is the response, truncated. Kept because llama.cpp's error bodies
	// name the actual problem — a missing GGUF, an unusable chat template, a
	// context size larger than the model allows — and a status code does not.
	Body string `json:"body,omitempty"`
	// Note is the interpretation where a bare result would mislead. A 404 from
	// /health is the headline case: Ollama does not implement it, so a 404 there
	// with a working /v1/models is a healthy server, not a broken one.
	Note string `json:"note,omitempty"`
}

// TestCompletion is the live end-to-end check.
//
// One token, a fixed prompt, and no conversation history: it proves the whole
// path works without touching anything the user has written. Off unless asked
// for — it is an outbound request, and against a metered endpoint it costs
// real money.
type TestCompletion struct {
	Requested bool   `json:"requested"`
	URL       string `json:"url"`
	Model     string `json:"model,omitempty"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	// Reply is the single token that came back, if any. The prompt is a
	// constant in this file, so nothing here can carry user content.
	Reply string `json:"reply,omitempty"`
}

// testPrompt is fixed and boring on purpose: it must not resemble anything the
// user wrote, and it must be cheap to answer.
const testPrompt = "Reply with the single word: ok"

const (
	probeTimeout  = 5 * time.Second
	testTimeout   = 60 * time.Second
	maxBodyBytes  = 2000
	maxReplyBytes = 200
)

func probe(ctx context.Context, cfg config.Config, withTest bool) Network {
	n := Network{}
	client := &http.Client{Timeout: probeTimeout}

	health := getHop(ctx, client, "llm /health", cfg.LLMURL+"/health", cfg.LLMAPIKey)
	if health.Status == http.StatusNotFound {
		health.Note = "not implemented by every backend — Ollama returns 404 here and is " +
			"still healthy. Read /v1/models below before concluding anything."
	}
	n.Hops = append(n.Hops, health)

	models := getHop(ctx, client, "llm /v1/models", cfg.LLMURL+"/v1/models", cfg.LLMAPIKey)
	ids := modelIDs(models.Body)
	if len(ids) > 0 {
		models.Note = fmt.Sprintf("%d model(s) offered: %s", len(ids), strings.Join(ids, ", "))
	}
	n.Hops = append(n.Hops, models)

	// /props is llama.cpp's own account of what it loaded. It answers three
	// questions users are routinely asked to check by hand — which file is
	// loaded, what context size is actually in force, and which chat template
	// is applied — and a mismatch between its n_ctx and config's ctx_size
	// explains a whole family of "it forgets everything" reports.
	props := getHop(ctx, client, "llm /props", cfg.LLMURL+"/props", cfg.LLMAPIKey)
	summarizeProps(&props)
	n.Hops = append(n.Hops, props)

	if cfg.SearchURL != "" {
		n.Hops = append(n.Hops, getHop(ctx, client, "search", cfg.SearchURL, ""))
	}
	if cfg.EmbedURL != "" {
		n.Hops = append(n.Hops, getHop(ctx, client, "embed /health", cfg.EmbedURL+"/health", ""))
	}

	n.Verdict = verdict(health, models, len(ids))

	if withTest {
		tc := testCompletion(ctx, cfg, firstOr(ids, ""))
		n.TestCompletion = &tc
	}
	return n
}

func getHop(ctx context.Context, client *http.Client, name, url, apiKey string) Hop {
	h := Hop{Name: name, URL: url}
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.Error = err.Error()
		return h
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	h.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		// The transport error is the diagnosis here — "connection refused" and
		// "no route to host" and "i/o timeout" mean three different things and
		// point at three different fixes — so it is kept verbatim rather than
		// flattened into "unreachable".
		h.Error = err.Error()
		return h
	}
	defer resp.Body.Close()

	h.Status = resp.StatusCode
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	h.Body = truncateString(string(body), maxBodyBytes)
	return h
}

// modelIDs pulls the ids out of an OpenAI-shaped /v1/models body. Returns
// nothing on any parse failure, which the caller reads as "could not tell"
// rather than "no models".
func modelIDs(body string) []string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// summarizeProps replaces the /props body with the handful of fields worth
// reading. The raw body is dropped because it embeds the full chat template,
// which can run to several kilobytes of Jinja and would dominate the report.
func summarizeProps(h *Hop) {
	if h.Status != http.StatusOK || h.Body == "" {
		return
	}
	var p struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots   int    `json:"total_slots"`
		ModelPath    string `json:"model_path"`
		ChatTemplate string `json:"chat_template"`
	}
	if err := json.Unmarshal([]byte(h.Body), &p); err != nil {
		h.Body = truncateString(h.Body, 300)
		return
	}

	parts := []string{}
	if p.ModelPath != "" {
		parts = append(parts, "model_path="+p.ModelPath)
	}
	if p.DefaultGenerationSettings.NCtx > 0 {
		parts = append(parts, fmt.Sprintf("n_ctx=%d", p.DefaultGenerationSettings.NCtx))
	}
	if p.TotalSlots > 0 {
		parts = append(parts, fmt.Sprintf("slots=%d", p.TotalSlots))
	}
	parts = append(parts, fmt.Sprintf("chat_template=%d chars", len(p.ChatTemplate)))

	h.Body = ""
	h.Note = strings.Join(parts, "  ")
}

// verdict reads the two llm hops the way a maintainer would.
func verdict(health, models Hop, modelCount int) string {
	switch {
	case health.Status == http.StatusOK && modelCount > 0:
		return "upstream reachable and serving models"
	case health.Status == http.StatusOK:
		return "upstream reachable; /health is OK but no model list was returned — " +
			"a model may still be loading"
	case modelCount > 0:
		return "upstream reachable via /v1/models but /health did not answer OK — " +
			"normal for Ollama and other non-llama.cpp backends"
	case health.Error != "" && models.Error != "":
		return "upstream unreachable: " + health.Error
	case health.Status >= 400:
		return fmt.Sprintf("upstream answered %d on /health and returned no model list — "+
			"something is listening but it is not serving an LLM API", health.Status)
	default:
		return "upstream did not answer"
	}
}

// testCompletion sends one token through the whole path.
func testCompletion(ctx context.Context, cfg config.Config, model string) TestCompletion {
	url := strings.TrimRight(cfg.LLMURL, "/") + "/v1/chat/completions"
	tc := TestCompletion{Requested: true, URL: url, Model: model}

	payload := map[string]any{
		"messages":   []map[string]string{{"role": "user", "content": testPrompt}},
		"max_tokens": 1,
		"stream":     false,
	}
	// Omitted entirely when unknown: llama.cpp ignores the field and serves
	// whatever it has loaded, while an OpenAI-compatible gateway rejects an
	// empty string with a validation error that would look like a real failure.
	if model != "" {
		payload["model"] = model
	}
	body, err := json.Marshal(payload)
	if err != nil {
		tc.Error = err.Error()
		return tc
	}

	client := &http.Client{Timeout: testTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		tc.Error = err.Error()
		return tc
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.LLMAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	}

	start := time.Now()
	resp, err := client.Do(req)
	tc.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		tc.Error = err.Error()
		return tc
	}
	defer resp.Body.Close()

	tc.Status = resp.StatusCode
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if resp.StatusCode != http.StatusOK {
		tc.Error = truncateString(string(raw), maxBodyBytes)
		return tc
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		tc.Error = "reply was not valid JSON: " + truncateString(string(raw), 300)
		return tc
	}
	if out.Model != "" {
		tc.Model = out.Model
	}
	if len(out.Choices) > 0 {
		tc.Reply = truncateString(out.Choices[0].Message.Content, maxReplyBytes)
	}
	tc.OK = true
	return tc
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

func truncateString(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… (%d bytes total)", len(s))
}
