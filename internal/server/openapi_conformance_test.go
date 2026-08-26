// Conformance tests for the OpenAPI document served at /openapi.json.
//
// A hand-written spec rots. It rots quietly, because nothing executes it: a
// route can be added, renamed or deleted and the document stays exactly as
// wrong as it was, while /docs keeps rendering and keeps looking authoritative.
// That is the same failure shape as the /state/info regression these tests
// exist for — a plausible answer with nothing behind it.
//
// So the document is checked from both ends:
//
//	TestOpenAPICoversEveryDispatchedRoute  every path the dispatcher matches is
//	                                      accounted for in the spec
//	TestOpenAPIPathsAreRouted             every path in the spec reaches a real
//	                                      handler, not the static fallthrough
//	TestOpenAPIRefsResolve                every $ref points at something
//
// The first reads server.go's own syntax tree rather than a list kept by hand,
// so a new `case` in ServeHTTP fails the build's tests until somebody decides
// what it is called in the document.
package server

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jmccardle/gobbonet/internal/apidocs"
	"github.com/jmccardle/gobbonet/internal/auth"
	"github.com/jmccardle/gobbonet/internal/config"
	"github.com/jmccardle/gobbonet/internal/version"
)

// routeDocs maps every path literal ServeHTTP matches on to the spec paths that
// document it.
//
// This table is deliberately written out rather than derived. Deriving it would
// mean inventing a rule for how "/llm/" relates to "/llm/{subpath}", and any
// such rule is loose enough to let a real omission through. Keeping it explicit
// puts the decision where it belongs: when you add a route, you edit this table,
// and editing it is the moment you notice the document needs a new entry.
//
// An empty list is allowed, and means "matched here but deliberately not a
// documented path of its own". There are none today; put the reason on the
// entry if you add one.
var routeDocs = map[string][]string{
	"/login":             {"/login"},
	"/logout":            {"/logout"},
	"/favicon.ico":       {"/favicon.ico"},
	"/health-fileserver": {"/health-fileserver"},
	"/openapi.json":      {"/openapi.json"},
	"/docs":              {"/docs"},
	"/docs/":             {"/docs/{asset}"},
	"/active-model.json": {"/active-model.json"},
	"/models-list.json":  {"/models-list.json"},
	"/state":             {"/state"},
	"/state/":            {"/state/info"},
	"/perf":              {"/perf"},
	"/swap-model":        {"/swap-model"},
	"/swap-status":       {"/swap-status"},
	"/llm/jobs":          {"/llm/jobs"},
	"/llm/jobs/":         {"/llm/jobs/{id}", "/llm/jobs/{id}/cancel"},
	"/llm":               {"/llm/{subpath}", "/llm/v1/chat/completions"},
	"/llm/":              {"/llm/{subpath}", "/llm/v1/chat/completions"},
	"/search":            {"/search/{subpath}", "/search/health", "/search/web_search"},
	"/search/":           {"/search/{subpath}", "/search/health", "/search/web_search"},
	"/embed":             {"/embed/{subpath}", "/embed/v1/embeddings"},
	"/embed/":            {"/embed/{subpath}", "/embed/v1/embeddings"},
}

// pathParams are the substitutions that turn a templated spec path into a
// request the server will actually route.
var pathParams = map[string]string{
	"{id}":      "0f1e2d3c4b5a69788796a5b4c3d2e1f0", // well-formed, and no such job
	"{subpath}": "props",
	"{asset}":   "swagger-ui.css",
}

// dispatchLiterals returns every "/..." string constant in ServeHTTP.
func dispatchLiterals(t *testing.T) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ServeHTTP" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(value, "/") {
				return true
			}
			found[value] = true
			return true
		})
	}
	if len(found) == 0 {
		t.Fatal("found no path literals in ServeHTTP — the parse is wrong, not the code")
	}
	return found
}

// specPaths returns the served document's paths, and the methods on each.
func specPaths(t *testing.T) map[string][]string {
	t.Helper()

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(apidocs.Spec(), &doc); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("spec has no paths")
	}

	known := map[string]string{
		"get": http.MethodGet, "post": http.MethodPost,
		"put": http.MethodPut, "delete": http.MethodDelete,
		"patch": http.MethodPatch, "head": http.MethodHead,
	}
	out := map[string][]string{}
	for path, item := range doc.Paths {
		methods := []string{}
		for key, method := range known {
			if _, ok := item[key]; ok {
				methods = append(methods, method)
			}
		}
		sort.Strings(methods)
		out[path] = methods
	}
	return out
}

// Every path ServeHTTP matches on must be accounted for in the document.
func TestOpenAPICoversEveryDispatchedRoute(t *testing.T) {
	literals := dispatchLiterals(t)
	paths := specPaths(t)

	for literal := range literals {
		documented, listed := routeDocs[literal]
		if !listed {
			t.Errorf("ServeHTTP matches %q but routeDocs does not mention it.\n"+
				"Add it to openapi.json and to routeDocs, or map it to an empty "+
				"list with a comment saying why it is not a documented path.", literal)
			continue
		}
		for _, path := range documented {
			if _, ok := paths[path]; !ok {
				t.Errorf("routeDocs says %q is documented at %q, but the spec has no such path",
					literal, path)
			}
		}
	}

	for literal := range routeDocs {
		if !literals[literal] {
			t.Errorf("routeDocs mentions %q but ServeHTTP no longer matches it — "+
				"the route was renamed or removed and the document still describes it",
				literal)
		}
	}
}

// Every path in the document must reach a handler. A path that has been renamed
// falls through to the static file server, which answers a 404 the frontend
// would parse without complaint — the failure mode this whole file exists for.
func TestOpenAPIPathsAreRouted(t *testing.T) {
	srv, cfg := newTestServer(t)

	// /favicon.ico is documented and routes to the static handler, so it needs
	// a file to find. Without one it would 404 for a real reason and this test
	// could not tell that apart from a routing hole.
	if err := os.WriteFile(filepath.Join(cfg.WebRoot, "favicon.ico"), []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}

	for path, methods := range specPaths(t) {
		if len(methods) == 0 {
			t.Errorf("%s: documented with no operations", path)
			continue
		}
		target := path
		for placeholder, value := range pathParams {
			target = strings.ReplaceAll(target, placeholder, value)
		}
		if strings.ContainsAny(target, "{}") {
			t.Errorf("%s: no substitution for its path parameter — add one to pathParams", path)
			continue
		}

		for _, method := range methods {
			rec := do(t, srv, method, target, nil)
			if rec.Code != http.StatusNotFound {
				continue
			}
			// The static fallthrough is the one 404 that means "this path is
			// not routed". Every other 404 here is a handler answering.
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				continue
			}
			if body["error"] == "not found" {
				t.Errorf("%s %s fell through to the static file server — "+
					"the document describes a route that no longer exists",
					method, target)
			}
		}
	}
}

// Every $ref must resolve. Swagger UI answers a dangling one by rendering that
// section empty and logging to the browser console, which nobody watching a
// server is looking at.
func TestOpenAPIRefsResolve(t *testing.T) {
	var doc any
	if err := json.Unmarshal(apidocs.Spec(), &doc); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}

	refs := map[string]bool{}
	var collect func(node any)
	collect = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "$ref" {
					if s, ok := child.(string); ok {
						refs[s] = true
						continue
					}
					t.Errorf("$ref is not a string: %v", child)
				}
				collect(child)
			}
		case []any:
			for _, child := range value {
				collect(child)
			}
		}
	}
	collect(doc)

	if len(refs) == 0 {
		t.Fatal("no $refs found — the walk is wrong, not the document")
	}

	for ref := range refs {
		if !strings.HasPrefix(ref, "#/") {
			t.Errorf("%s is not a local reference; this document has no external parts", ref)
			continue
		}
		if err := resolvePointer(doc, strings.TrimPrefix(ref, "#/")); err != nil {
			t.Errorf("%s does not resolve: %v", ref, err)
		}
	}
}

// resolvePointer walks a slash-separated path into the decoded document.
func resolvePointer(doc any, pointer string) error {
	node := doc
	for _, segment := range strings.Split(pointer, "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return fmt.Errorf("%q is not inside an object", segment)
		}
		if node, ok = object[segment]; !ok {
			return fmt.Errorf("no %q here", segment)
		}
	}
	return nil
}

// The served document must describe the binary serving it.
func TestOpenAPIReportsBuildVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/openapi.json", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	var doc struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}

	// Swagger UI 4.15.5 is what is vendored, and 4.x does not render 3.1. If
	// this is ever raised, upgrade the vendored UI in the same commit.
	if !strings.HasPrefix(doc.OpenAPI, "3.0.") {
		t.Errorf("openapi = %q; the bundled Swagger UI cannot render anything above 3.0.x", doc.OpenAPI)
	}
	if doc.Info.Version != version.String() {
		t.Errorf("info.version = %q, want the build version %q", doc.Info.Version, version.String())
	}
}

func TestDocsPageAndAssets(t *testing.T) {
	srv, _ := newTestServer(t)

	page := do(t, srv, http.MethodGet, "/docs", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("/docs status = %d, want 200", page.Code)
	}
	// The page is useless if the two files it names are not the two that are
	// served, and a typo in either direction produces a blank page with no
	// error anywhere.
	for _, asset := range []string{"/docs/swagger-ui-bundle.js", "/docs/swagger-ui.css"} {
		if !strings.Contains(page.Body.String(), asset) {
			t.Errorf("/docs does not reference %s", asset)
		}
		rec := do(t, srv, http.MethodGet, asset, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", asset, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", asset)
		}
	}
}

// An unknown asset is a 404, not a fallback to the page. Serving index.html for
// /docs/swagger-ui-bundel.js would render a blank tab and log nothing.
func TestDocsUnknownAssetIsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/docs/swagger-ui-bundel.js", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decode(t, rec)["error"]; got != "no such docs asset" {
		t.Errorf("error = %v, want %q", got, "no such docs asset")
	}
}

// The document is behind the auth gate like everything else it describes.
func TestDocsRequireAuth(t *testing.T) {
	_, cfg := newTestServer(t)
	secret, err := auth.NewSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RequireAuth = true
	cfg.AccessSecret = secret
	gated, err := New(cfg, config.ModeRemote, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/openapi.json", "/docs", "/docs/swagger-ui.css"} {
		rec := do(t, gated, http.MethodGet, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, rec.Code)
		}
	}
}
