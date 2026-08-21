// Package apidocs serves the OpenAPI description of this server, and a Swagger
// UI page that renders it.
//
// Three routes:
//
//	/openapi.json     the document
//	/docs             the page
//	/docs/<asset>     the two Swagger UI files the page loads
//
// Everything is compiled in with go:embed. A CDN <script> tag would be smaller,
// and would leave /docs blank on a machine with no internet — which is the
// normal case for this app and the exact situation someone would be reading the
// API docs in. Swagger UI's provenance, version and licence are in
// swagger-ui/VENDOR.md.
//
// This has no counterpart in fileserver.ps1. It is an addition in the Go build,
// and it is additive: no existing client reads either route.
package apidocs

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jmccardle/gobbonet/internal/httpx"
	"github.com/jmccardle/gobbonet/internal/version"
)

//go:embed openapi.json
var rawSpec []byte

//go:embed index.html
var indexHTML []byte

//go:embed swagger-ui/swagger-ui-bundle.js swagger-ui/swagger-ui.css
var uiFiles embed.FS

// assets are the only files /docs/* will serve. A map rather than a directory
// walk: the embedded FS also holds LICENSE, NOTICE and VENDOR.md, which belong
// in the repository and not on the wire.
var assets = map[string]string{
	"swagger-ui-bundle.js": "swagger-ui/swagger-ui-bundle.js",
	"swagger-ui.css":       "swagger-ui/swagger-ui.css",
}

// spec is rawSpec with info.version replaced by the running binary's version.
// Built once at init.
var spec []byte

func init() {
	var err error
	if spec, err = stampVersion(rawSpec, version.String()); err != nil {
		// The document is a compiled-in constant, so this can only fail if the
		// file committed alongside this package is broken. That is a build
		// defect, and shipping a server that quietly serves a spec it could not
		// parse would hide it until someone tried to use the docs.
		panic(fmt.Sprintf("apidocs: embedded openapi.json is unusable: %v", err))
	}
}

// versionPlaceholder is what the committed openapi.json carries in info.version.
//
// Storing a real version in the file would be a second place to bump on every
// release, and the first release that forgot would leave the document quietly
// claiming to describe a build it does not describe.
const versionPlaceholder = `"@BUILD_VERSION@"`

// stampVersion substitutes the running binary's version for the placeholder.
//
// A byte substitution rather than unmarshal-edit-remarshal, because Go's map
// encoding sorts keys: a round trip would reorder the whole document and put
// `components` above `openapi`. Same content, but the first thing anyone does
// with this endpoint is pipe it through `head`.
func stampVersion(doc []byte, v string) ([]byte, error) {
	if n := bytes.Count(doc, []byte(versionPlaceholder)); n != 1 {
		return nil, fmt.Errorf("expected exactly one %s, found %d", versionPlaceholder, n)
	}
	stamped, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := bytes.Replace(doc, []byte(versionPlaceholder), stamped, 1)
	// Parse the result, not the input: this is the only thing standing between
	// a malformed document and a /docs page that renders nothing and says
	// nothing about why.
	if !json.Valid(out) {
		return nil, fmt.Errorf("document is not valid JSON after substitution")
	}
	return out, nil
}

// Spec is the served document, for tests and for callers that want it in
// process rather than over HTTP.
func Spec() []byte { return spec }

// Handle serves /openapi.json, /docs and /docs/*.
func Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "GET only")
		return
	}

	switch path := r.URL.Path; {
	case path == "/openapi.json":
		httpx.WriteBytes(w, r, http.StatusOK, "application/json; charset=utf-8", spec)

	case path == "/docs" || path == "/docs/":
		httpx.WriteBytes(w, r, http.StatusOK, "text/html; charset=utf-8", indexHTML)

	default:
		name := strings.TrimPrefix(path, "/docs/")
		embedded, ok := assets[name]
		if !ok {
			// No fallback to the page: a mistyped asset must look like the 404
			// it is, not like a working request that renders nothing.
			httpx.WriteJSON(w, r, http.StatusNotFound, map[string]string{
				"error": "no such docs asset",
				"path":  path,
			})
			return
		}
		body, err := uiFiles.ReadFile(embedded)
		if err != nil {
			httpx.ErrorDetail(w, r, http.StatusInternalServerError, "read failed", err.Error())
			return
		}
		httpx.WriteBytes(w, r, http.StatusOK, httpx.MimeType(name), body)
	}
}
