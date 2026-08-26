# Testing

gobbonet talks to exactly one thing: llama.cpp. Everything this document
describes follows from that.

## What the grid is, and what it is not

The instinct is to build a matrix of compute backends — CUDA, ROCm, Metal,
Vulkan — because that is what varies between the machines people run this on.
It is the wrong axis. **gobbonet never sees a backend.** It sees llama.cpp's
HTTP API and, in local mode, llama-server's command line. A CUDA build and a
CPU build expose the same routes, take the same flags, and answer `/props` with
the same shape. Testing five backends tests llama.cpp five times and gobbonet
once.

What does vary is **who owns the llama-server process**:

|  | pinned build | newest build (canary) |
|---|---|---|
| **local** — gobbonet spawns and supervises it | linux, windows, macos | nightly, informational |
| **remote** — someone else runs it | linux | nightly, informational |

Local mode carries five contracts remote mode simply does not have: the argv,
llama-server's stderr text, its default port, process-group teardown, and GGUF
headers read from disk. That asymmetry is the entire grid. Remote mode is an
HTTP client, so it is not multiplied across operating systems — `internal/
supervisor` holds the only per-OS code in the tree.

Everything here runs on CPU, on GitHub's standard runners, free for a public
repository.

### The four things a GPU would actually test

Not nothing — but not a matrix either:

1. `--cache-type-k/v` acceptance per backend (`q8_0` needs flash-attn on some;
   rejected means llama-server exits at launch, and `perf.toml`'s validator
   cannot know the backend).
2. That `gpu_layers` offloads rather than quietly running on CPU.
3. That VRAM is *released* on swap and stop — the orphaned-helper case
   `process_unix.go`'s group sweep exists for. Invisible on CPU by
   construction.
4. The rollback path when a model does not fit.

Four checks, one model, five minutes, on hardware you already own. A
pre-release smoke test, not CI.

## The contract inventory

Every interface gobbonet depends on, and where it is consumed. This list is the
test plan; `.github/scripts/smoke.sh` asserts one line per row.

| Surface | Consumed at | Mode | Breaks when |
|---|---|---|---|
| `GET /health` | `supervisor.go:548` (start + swap gate), `server.go:338` (`upstream_ok`), `js/11-search.js:199` | both | the route moves |
| `GET /props` | `models/info.go:85`, `js/06-state-sync.js:640` | both | `chat_template`, `model_path` or `default_generation_settings.n_ctx` is renamed |
| `POST /v1/chat/completions` (SSE) | `jobs.go:362` + four frontend sites | both | stream framing changes |
| `POST /apply-template` | `js/06-state-sync.js:668` | both | llama.cpp-only route |
| `POST /tokenize` | `js/06-state-sync.js:758` | both | llama.cpp-only route |
| argv, 10 flags | `supervisor.go:266-285` | local | a flag is renamed, removed, or changes default |
| stderr text | `ring.go:85`'s allowlist | local | upstream rewords a fatal line |
| default port | — | local | **announced: 8080 → 9931** |
| GGUF header | `models.IdentifyFile` | local | metadata keys change |
| `POST /v1/embeddings` | `js/08-rag.js:189` | both (second instance) | — |
| ollama.com `/web_search` | `js/11-search.js:136` | neither | the only non-llama.cpp API in the tree |

Two of those rows are why "just point `llm_url` at a hosted API" is not a
supported configuration and cannot become one: `/apply-template` and
`/tokenize` have no equivalent anywhere else. `/props` and `/health` have none
either, so a hosted endpoint would generate text while reporting
`upstream_ok:false` and identifying every model as `family=custom`. It half
works, which is worse than not working.

## Running it

One command, the same one CI runs:

```sh
.github/scripts/run-contracts.sh --mode local
.github/scripts/run-contracts.sh --mode remote
.github/scripts/run-contracts.sh --mode local --llama master   # canary
```

It fetches the engine and the model, writes a config, starts everything, runs
the suite, and tears it down — printing both logs if anything failed. Nothing
about the test lives in YAML, so reproducing a CI failure does not involve
reading a workflow.

Against a server that is already running — a container, the one on your desk:

```sh
.github/scripts/smoke.sh http://127.0.0.1:9066
.github/scripts/smoke.sh http://127.0.0.1:9066 --local --model my-model.gguf
```

It needs curl and a shell. Checks run to completion rather than stopping at the
first failure: one upstream rename usually breaks several routes, and the
useful artifact is the whole list.

The suite runs with `--no-auth`. The password gate is orthogonal to every
contract above, `set-password` needs a terminal by design, and the gate itself
is covered by `conformance_test.go` without needing a process at all.

## The pin

`launch.bat` holds `LLAMA_PIN_TAG` and `LLAMA_PIN_SHA256`, and it is the only
place either lives. `.github/scripts/llama-pin.sh` reads them; nothing in the
workflows hardcodes a build number. This is the same rule as `VERSION`: two
literals saying the same thing is two literals that drift, and CI testing an
engine no user runs would be green and meaningless.

`installer-asset` in `contracts.yml` re-downloads the pinned Windows zip and
checks it against that SHA, because both halves rot silently — an asset can be
re-cut or dropped, and today the first person to find out is someone
installing.

`fetch-engine.sh` pulls the **release archive**, not the container image: that
is byte-for-byte what a user gets, and local mode needs a real executable
anyway. It then confirms the binary reports the build number that was asked
for, so a re-cut release or a stale cache fails loudly instead of producing a
green suite that describes the wrong engine.

## The canary

Same suite, newest upstream build, nightly, `continue-on-error`. It is
reporting on someone else's commit, so it must never gate a merge — but it
turns "a user updated their engine and it broke" into a week of warning.

Two things found while building it, both exactly the class of drift it exists
to catch:

- **`/releases/latest` is a trap.** Upstream now also cuts semver releases
  (`v0.3.0`), GitHub marks one of those "latest", and they carry no binary
  assets — so that endpoint hands you a tag whose downloads 404. The builds are
  the `bNNNN` tags.
- **`--version` changed format.** `version: 9294 (0f3cb3fc8)` became
  `version: 0.3.0-dev (build 10639, commit 5e6a37cb1)`. A cosmetic change to a
  string nobody thinks of as an interface, which breaks anything parsing it.

As of this writing the tree pins `b9294` and upstream is at `b10639` — about
1,345 builds — and the full suite passes against both, in both modes. The
contracts have held; the canary is how you find out when they stop.

## What the free tier covers

`ci.yml`, every push and PR:

- `go test`, `-race`, `vet`, `gofmt` on linux, windows and macOS — both
  supervisor variants, both config-discovery layouts, `conformance_test.go`
- cross-compilation of all five release targets
- `stage-web.sh`'s frontend-completeness check

`contracts.yml` adds the grid above. `container.yml`, where the container work
is present, checks the image and `compose.yaml` on the same runners; it is a
separate workflow because it and the files it checks travel together.

Note the `fetch-depth: 0` on checkout: `version_test.go` compares `VERSION`
against the nearest upstream release tag, and without full history *and* tags
it does not fail — it skips, naming what it could not verify, which reads as
green.

## Known gaps

- **The Windows setup half.** `hardware-probe.ps1`, `hw-recommend.ps1`,
  `launch.bat` and the NSIS installer are what turn a binary into a working
  install, and nothing here touches them. They are also the best available
  return on effort: the probe is fixture-driven by nature — feed it
  `hardware.json` files and you test the recommendation logic for twelve GPUs
  nobody owns, on a free Windows runner.
- **`models.IdentifyFile` against real headers.** `classify_test.go` is
  entirely synthetic `ClassifyInput`; there is no `testdata/`. A handful of
  truncated real GGUF headers would cover the local-mode metadata path, mmproj
  exclusion and `sanitiseArch`. No engine required.
- **Job superseding under contention.** Covered by `jobs_test.go` at the unit
  level. Racing it against a real engine that answers in under a second is
  flaky, so the smoke suite does not try.
- **Embeddings end to end.** `/embed/v1/embeddings` works, but the suite does
  not assert it — a second engine and a 146 MB download on every run, for one
  route whose absence already degrades visibly (502, and the client falls back
  to weighted tags).
