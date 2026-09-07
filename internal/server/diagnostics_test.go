package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ElodineOfficial/GobboNet/internal/auth"
	"github.com/ElodineOfficial/GobboNet/internal/debugreport"
)

// lockedServer is a server with the password gate on and no valid session, the
// state the pre-login half of /diagnostics.json exists to explain.
func lockedServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServer(t)
	secret, err := auth.NewSecret("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	srv.cfg.RequireAuth = true
	srv.secret = secret
	return srv
}

// TestDiagnosticsAnswersBeforeTheGate is the whole point of the route's
// position in ServeHTTP: a report explaining why someone cannot sign in is
// worthless if reading it requires signing in.
func TestDiagnosticsAnswersBeforeTheGate(t *testing.T) {
	srv := lockedServer(t)

	rec := do(t, srv, http.MethodGet, "/diagnostics.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — the diagnostics route must answer while locked out", rec.Code)
	}

	var r debugreport.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Tier != debugreport.TierPublic {
		t.Errorf("tier = %q, want %q", r.Tier, debugreport.TierPublic)
	}
	if !r.Auth.Required {
		t.Error("auth.required should be true on a gated server")
	}
	if r.Auth.Session != debugreport.SessionNone {
		t.Errorf("session = %q, want %q for a request with no cookie", r.Auth.Session, debugreport.SessionNone)
	}
}

// TestDiagnosticsPublicTierWithholdsTheInstallation checks the boundary that
// makes answering pre-gate acceptable at all.
func TestDiagnosticsPublicTierWithholdsTheInstallation(t *testing.T) {
	srv := lockedServer(t)
	rec := do(t, srv, http.MethodGet, "/diagnostics.json", nil)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"system", "config", "runtime", "network", "data", "models"} {
		if _, present := got[forbidden]; present {
			t.Errorf("pre-login payload carried %q", forbidden)
		}
	}

	// The upstream address is the single most sensitive thing in the report for
	// someone who has not signed in: it names an internal host.
	if strings.Contains(rec.Body.String(), srv.cfg.LLMURL) {
		t.Error("pre-login payload named the upstream URL")
	}
}

// TestDiagnosticsDistinguishesNoCookieFromStaleCookie is the distinction the
// user actually needs: "you were never signed in here" versus "you were, and
// something ended it" — the latter being what a server restart looks like from
// a browser holding a cookie for a secret that no longer exists.
func TestDiagnosticsDistinguishesNoCookieFromStaleCookie(t *testing.T) {
	srv := lockedServer(t)

	req := newReq(http.MethodGet, "/diagnostics.json", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "a-token-from-a-previous-run"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var r debugreport.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Auth.Session != debugreport.SessionStale {
		t.Errorf("session = %q, want %q for a rejected cookie", r.Auth.Session, debugreport.SessionStale)
	}
}

// TestDiagnosticsFullTierWhenAuthenticated covers the authenticated path,
// including the runtime section that only a live process can supply.
func TestDiagnosticsFullTierWhenAuthenticated(t *testing.T) {
	// newTestServer leaves RequireAuth false, which makes every request
	// authenticated — the same shape as a signed-in session for this route.
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/diagnostics.json?probe=0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	var r debugreport.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Tier != debugreport.TierFull {
		t.Fatalf("tier = %q, want %q", r.Tier, debugreport.TierFull)
	}
	for name, present := range map[string]bool{
		"system":  r.System != nil,
		"config":  r.Config != nil,
		"data":    r.Data != nil,
		"models":  r.Models != nil,
		"runtime": r.Runtime != nil,
	} {
		if !present {
			t.Errorf("full tier is missing the %s section", name)
		}
	}
	if r.Runtime.PID == 0 {
		t.Error("runtime section carries no pid")
	}
	// probe=0 was passed, so the absence must be explained rather than silent.
	if r.Network != nil {
		t.Error("probe=0 should suppress the network section")
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "network") {
		t.Error("suppressed network section left no note explaining why")
	}
}

// TestDiagnosticsSecretsAreNeverServed is the guarantee that makes this report
// safe to paste into a public issue.
func TestDiagnosticsSecretsAreNeverServed(t *testing.T) {
	srv, _ := newTestServer(t)
	const apiKey = "sk-this-must-never-be-served"
	srv.cfg.LLMAPIKey = apiKey

	secret, err := auth.NewSecret("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	srv.secret = secret

	rec := do(t, srv, http.MethodGet, "/diagnostics.json?probe=0", nil)
	body := rec.Body.String()

	if strings.Contains(body, apiKey) {
		t.Error("the report served the upstream API key")
	}
	if strings.Contains(body, secret) {
		t.Error("the report served the access secret hash")
	}
	if !strings.Contains(body, debugreport.SecretArgon2id) {
		t.Error("the report should still name the secret FORMAT — that is the diagnostic")
	}
}

// TestDiagnosticsLiveTestRequiresPOST keeps a browser prefetch, a link preview
// or a crawler from spending tokens against a metered upstream.
func TestDiagnosticsLiveTestRequiresPOST(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/diagnostics.json", nil)
	var r debugreport.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Network == nil {
		t.Fatal("GET should still probe")
	}
	if r.Network.TestCompletion != nil {
		t.Error("a GET must never send a live test message")
	}

	rec = do(t, srv, http.MethodPost, "/diagnostics.json", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Network == nil || r.Network.TestCompletion == nil {
		t.Fatal("a POST should attempt the live test")
	}
	// Nothing is listening on the test upstream, so the attempt must be
	// recorded as a failure with the transport error kept intact.
	if r.Network.TestCompletion.OK {
		t.Error("test reported OK against a dead upstream")
	}
	if r.Network.TestCompletion.Error == "" {
		t.Error("a failed test message must carry the reason")
	}
}

func TestDiagnosticsRejectsOtherMethods(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodDelete, "/diagnostics.json", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE got %d, want 405", rec.Code)
	}
}

// TestReportConfigTracksTheLiveSecret covers the drift that would otherwise
// make the report lie about the one thing it is asked about most: after a
// legacy secret is upgraded in place, the loaded config still holds the old
// one, and a report built from it would claim the migration never happened.
func TestReportConfigTracksTheLiveSecret(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.AccessSecret = "deadbeef:cafebabe" // legacy, as loaded

	upgraded, err := auth.NewSecret("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	srv.secret = upgraded // as rewritten by a successful login

	if got := srv.reportConfig().AccessSecret; got != upgraded {
		t.Error("reportConfig used the stale loaded secret instead of the live one")
	}
}
