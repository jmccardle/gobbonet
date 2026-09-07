# Connection diagnostics — a must-gather report

## The problem

`Error: 502` is true and useless. It says a proxy did not get an answer; it does
not say which of the links between the user and the model is broken, and there
are three of them. The information to tell them apart already existed and none
of it was ever shown:

- The supervisor has captured llama-server's stderr since it replaced
  `Get-LlamaStartupError`, and `ringBuffer.LastError` has always mined it for an
  actionable line — "out of memory", "no such file", "address already in use".
  Nothing published it.
- The job manager records why each generation ended, including llama.cpp's own
  error body (`jobs.go:465`). It reached one message bubble and was discarded.
- `/health-fileserver` knew the mode, the upstream, the bind and the LAN
  addresses. `checkConnection()` reduced all of it to one word in the header
  pill and threw the rest away.
- `applyGenerationOutcome(..., 'diagnostic')` rendered a triage string *into the
  assistant message*, so the structured error did not survive the render.

The result was a support loop: a screenshot, then four rounds of "what does X
say", then a guess. This change replaces that with one paste.

## What was added

**`internal/debugreport`** — one collector, no HTTP and no printing, feeding
three surfaces:

```
GET/POST /diagnostics.json     internal/server/diagnostics.go
gobbonet debug-report          cmd/gobbonet/debugreport.go
the status-label modal         js/24-diagnostics.js
```

`Report` serialises to JSON for the UI and `Report.Markdown()` for pasting into
an issue. Live process state — supervisor phase, captured stderr, recent jobs,
the listener's real bind — arrives through the `RuntimeSource` interface rather
than an import, because `internal/server` imports this package.

### What the report carries, and why

| section | the failure it names |
|---|---|
| build origin | release binary vs. clean local build vs. **a tree with uncommitted edits** — the last one explains a symptom nobody else can reproduce. Cross-checks the ldflags stamp against the toolchain's own VCS record. |
| locale / code page | On a Japanese-locale Windows the ANSI code page is 932, where byte `0x5C` — the ASCII backslash — renders as `¥`. A user reporting "my paths are full of Y symbols" is looking at normal backslashes. `console_cp: 932` settles that in a glance instead of a thread. |
| path forensics | exists, writable (tested by writing, not inferred from mode bits), absolute, **raw bytes when non-ASCII**, valid UTF-8, free space, and on Windows the volume type. A `model_dir` on a `network` drive explains "The system cannot find the drive specified" against a path that plainly exists: drive mappings are per-logon-session, so a service cannot see `F:` even though Explorer can. |
| config diff | every setting with its default and a `changed` flag. The changed list is short and is almost always where the bug is. |
| connectivity | per hop, with status, latency and the error body verbatim. `/props` is included: it reports the loaded model path, the context size actually in force, and the chat template — three things users are otherwise asked to check by hand. A 404 from `/health` is annotated, because Ollama does not implement it and is healthy anyway (#27). |
| supervisor stderr | llama.cpp's own account, at last. |
| saved data | thread and message counts per state file, and whether a **legacy `.gobbonet-state.json` holds more history than the live `state.json`** — the upgrade case where a user's whole history sits in a file the Go server never reads, which from their side looks like the upgrade deleted everything. |

### Live test message

`POST /diagnostics.json`, or answering `y` at the CLI prompt, sends one message
with `max_tokens: 1` and reports exactly what came back. The prompt is a
constant in `probe.go`; nothing from any conversation is sent.

It is opt-in and defaults to no. It is an outbound request, and against a
metered endpoint it is somebody's money. A `GET` never triggers it, so a browser
prefetch or a link preview cannot spend tokens.

## Tiering

`/diagnostics.json` answers on **both sides of the auth gate** — a report whose
purpose is explaining why someone cannot sign in is worthless if reading it
requires signing in. The pre-login payload is version, mode, and the auth state;
it is built by a separate path rather than by stripping fields off the full one,
and `TestPublicTierKeysAreExactlyTheAllowlist` fails if a new field appears
there without a deliberate edit. It runs no probes, so an anonymous request can
never make the server dial out.

`auth.session` is `none` (nothing presented) or `stale` (presented and
rejected), and deliberately not split further. Distinguishing expired from
unknown from fingerprint-mismatch would tell whoever holds a copied cookie that
their token is live and only the client fingerprint is missing. "Stale" carries
the whole of the actionable meaning — sign in again — and a server restart, an
expiry and a changed password are indistinguishable from outside regardless.

## Redaction

Narrow on purpose. Two values are secret and are removed at the source: the
access secret (reported as a format name — `argon2id` or `legacy-sha256`, which
is itself the diagnostic) and the upstream API key (reported as "set, 51 chars",
which catches the truncated paste and the stray trailing newline).

**Paths keep the username by default.** For the whole class of bug where a
non-ASCII username breaks path handling, the username *is* the evidence.
`--redact-home` / a query parameter rewrites it to `~` when the user wants that,
and the report states which mode produced it.

## Notes

- `js/24-boot.js` is now `js/25-boot.js`. Boot must stay last; the new module
  needs `escapeHtml` from `18-utils.js` and `IS_SERVED` from `01-config.js`.
- The state format carries **no schema version**. The report says so rather than
  inferring one, and fingerprints by key set instead. Adding a `schemaVersion`
  is worth doing separately.
- Building the collector found a bug in itself on first run: `extensions` is a
  JSON object while every key beside it is an array, and a decoder that assumed
  arrays reported a state file holding 47 messages as empty. Counting is now
  shape-tolerant, with a regression test.
