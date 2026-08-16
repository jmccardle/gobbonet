#!/usr/bin/env python3
"""A keyless web-search backend for GobboNet. One file, no account, no API key.

    pip install ddgs
    python search_backend.py            # listens on 127.0.0.1:11435

Then set GobboNet's SEARCH_URL to:

    http://127.0.0.1:11435/web_search

That is the whole thing. It speaks the contract the UI already uses --
POST {"query","max_results"} -> {"results":[{"title","url","content"}]} -- so
nothing in GobboNet changes.

WHY A PACKAGE INSTEAD OF A SCRAPER
----------------------------------
The obvious version of this file scrapes DuckDuckGo's HTML endpoint and has no
dependencies at all. Don't. Measured 2026-08-16: that endpoint answers a plain
HTTP client with a bot interstitial containing no results, and every other
keyless option a bare client can reach is worse -- searx.be?format=json returns
HTML (public instances disable the JSON API), Mojeek 403s, Marginalia 302s.

Worse than not working, a scraper THINS. When DuckDuckGo last changed their
markup, the title selector still matched and the snippet selector stopped
matching, so a scraper kept returning results with titles, URLs and empty text.
Nothing failed. Nothing logged. The model just started answering as though the
web had nothing useful on it. A maintained client tracks the markup so you
don't have to, and that is worth one `pip install`.

THE FIELD NAME MATTERS
----------------------
GobboNet's UI reads `.content`. `ddgs` calls that field `body`. Map it, or you
get the empty-text failure above from a working search client -- which is
indistinguishable, from the UI, from the web having nothing to say.

Public domain / MIT-compatible: do whatever you like with this file, including
vendoring it into GobboNet.
"""

from __future__ import annotations

import argparse
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DEFAULT_PORT = 11435
MAX_BODY = 1 << 20  # 1 MiB; a search query is never larger


def search(query: str, max_results: int = 5) -> list[dict]:
    """Return [{title, url, content}]. Raises RuntimeError if unavailable."""
    ddgs_cls = None
    for mod in ("ddgs", "duckduckgo_search"):  # the package was renamed
        try:
            ddgs_cls = __import__(mod, fromlist=["DDGS"]).DDGS
            break
        except ImportError:
            continue
    if ddgs_cls is None:
        raise RuntimeError(
            "no search client installed. Run: pip install ddgs\n"
            "(A scraper is not a substitute -- see the note at the top of this file.)"
        )

    rows = list(ddgs_cls().text(query, max_results=max_results))
    out = []
    for r in rows:
        title = (r.get("title") or "").strip()
        url = (r.get("href") or r.get("url") or "").strip()
        # `body` is this client's name for the snippet. Reading only "snippet"
        # here is what produces results with no text.
        content = (r.get("body") or r.get("snippet") or "").strip()
        if title and url:
            out.append({"title": title, "url": url, "content": content})
    return out


class Handler(BaseHTTPRequestHandler):
    server_version = "GobboNetSearch/1.0"

    def _send(self, code: int, payload: dict) -> None:
        blob = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(blob)))
        # The UI is served from a different port, so it is a cross-origin call.
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()
        self.wfile.write(blob)

    def do_OPTIONS(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler naming
        self._send(204, {})

    def do_GET(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0] in ("/health", "/"):
            self._send(200, {"ok": True, "service": "gobbonet-search"})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0] != "/web_search":
            self._send(404, {"error": "not found"})
            return

        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            self._send(400, {"error": "bad Content-Length"})
            return
        if length > MAX_BODY:
            self._send(413, {"error": "request too large"})
            return

        try:
            req = json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError as e:
            self._send(400, {"error": f"invalid JSON: {e}"})
            return

        query = (req.get("query") or "").strip()
        if not query:
            self._send(400, {"error": "missing 'query'"})
            return
        try:
            limit = max(1, min(int(req.get("max_results") or 5), 25))
        except (TypeError, ValueError):
            limit = 5

        try:
            results = search(query, limit)
        except Exception as e:  # noqa: BLE001 - the reason must reach the caller
            # 502 WITH a reason, never an empty result list. "Not configured"
            # and "found nothing" must not look alike, or a broken backend
            # impersonates a quiet web.
            self._send(502, {"error": str(e), "results": []})
            return

        self._send(200, {"query": query, "results": results, "count": len(results)})

    def log_message(self, fmt: str, *a) -> None:
        sys.stderr.write("[search] " + (fmt % a) + "\n")


def _bind(host: str, port: int) -> tuple[ThreadingHTTPServer, int]:
    """Bind, falling back to a free port if the requested one cannot be used.

    On Windows this is not the rare case it looks like. Hyper-V, WSL and Docker
    reserve whole BLOCKS of ports, and a bind inside one fails with
    `WinError 10013 ... forbidden by its access permissions` while netstat shows
    nothing listening -- so the port looks free, the error says "permission",
    and neither points at the real cause. Measured on a dev box: 11497 refused
    exactly that way.

    Left unhandled, the user sees a stack trace mentioning access permissions
    and concludes the tool is broken. So: try what was asked, then let the OS
    pick, and always PRINT the port actually bound -- a fallback that does not
    say where it landed just moves the confusion.

    Check your own reserved ranges with:  netsh interface ipv4 show excludedportrange protocol=tcp
    """
    try:
        return ThreadingHTTPServer((host, port), Handler), port
    except (PermissionError, OSError) as e:
        print(f"could not bind port {port} ({e.__class__.__name__}: {e})")
        srv = ThreadingHTTPServer((host, 0), Handler)  # 0 = let the OS choose
        chosen = srv.server_address[1]
        print(f"using port {chosen} instead")
        print("  (on Windows, Hyper-V/WSL/Docker reserve port blocks; see")
        print("   netsh interface ipv4 show excludedportrange protocol=tcp)")
        return srv, chosen


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--port", type=int, default=DEFAULT_PORT)
    ap.add_argument("--host", default="127.0.0.1", help="default: loopback only")
    ap.add_argument("--self-test", action="store_true",
                    help="run one real search and print the result")
    args = ap.parse_args(argv if argv is not None else sys.argv[1:])

    if args.self_test:
        try:
            hits = search("gguf quantization", 2)
        except Exception as e:  # noqa: BLE001
            print(f"FAIL: {e}")
            return 1
        if not hits:
            print("FAIL: search returned nothing")
            return 1
        empty = [h for h in hits if not h["content"]]
        for h in hits:
            print(f"  {h['title'][:60]}\n    {h['url']}\n    {h['content'][:100]}")
        if empty:
            # The exact failure this file exists to prevent.
            print(f"FAIL: {len(empty)}/{len(hits)} results have EMPTY content")
            return 1
        print(f"OK: {len(hits)} results, all with content")
        return 0

    srv, port = _bind(args.host, args.port)
    print(f"keyless search on http://{args.host}:{port}/web_search")
    print(f"set GobboNet's SEARCH_URL to http://{args.host}:{port}/web_search")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")
    finally:
        srv.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
