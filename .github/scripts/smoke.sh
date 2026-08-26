#!/usr/bin/env bash
#
# Assert every interface gobbonet depends on is still where it was.
#
#   .github/scripts/smoke.sh http://127.0.0.1:9066
#   .github/scripts/smoke.sh http://127.0.0.1:9066 --local --model foo.gguf
#
# Run it against any gobbonet -- a CI job, a container, the one on your desk.
# It only needs curl and a shell, and it takes the server's word for nothing:
# every check names the contract it is pinning and where that contract is
# consumed, so a failure says what broke rather than that something did.
#
# WHY THIS EXISTS: gobbonet talks to exactly one thing, llama.cpp, over two
# surfaces -- an HTTP API and (in local mode) a command line. Both belong to a
# project that ships several builds a day. The Go tests pin OUR side of those
# contracts; nothing pinned llama.cpp's side, so an upstream rename would reach
# users before it reached us. See CI_TESTING.md.
#
# Checks run to completion rather than aborting at the first failure. One
# upstream rename usually breaks several routes at once, and the useful
# artifact is the whole list, not the first line of it.
set -uo pipefail

BASE="${1:?usage: smoke.sh BASE_URL [--local] [--model FILE.gguf]}"
shift
BASE="${BASE%/}"

MODE=remote
MODEL=""
while [ $# -gt 0 ]; do
    case "$1" in
        --local)  MODE=local ;;
        --model)  MODEL="${2:?--model needs a filename}"; shift ;;
        *)        echo "unknown argument: $1" >&2; exit 2 ;;
    esac
    shift
done

JOB_TIMEOUT="${JOB_TIMEOUT:-90}"
SWAP_TIMEOUT="${SWAP_TIMEOUT:-120}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
failures=""

# --- plumbing ---------------------------------------------------------------

# req METHOD PATH [BODY] -> prints the status code, leaves the body in $TMP/body
req() {
    local method="$1" path="$2" body="${3:-}"
    if [ -n "$body" ]; then
        curl -sS -o "$TMP/body" -w '%{http_code}' -X "$method" \
            -H 'Content-Type: application/json' -d "$body" "$BASE$path" 2>"$TMP/err" || echo 000
    else
        curl -sS -o "$TMP/body" -w '%{http_code}' -X "$method" "$BASE$path" 2>"$TMP/err" || echo 000
    fi
}

ok()   { pass=$((pass+1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad()  {
    fail=$((fail+1))
    failures="$failures
  - $1: $2"
    printf '  \033[31mFAIL\033[0m %s\n       %s\n' "$1" "$2"
    if [ -s "$TMP/body" ]; then
        printf '       body: %s\n' "$(head -c 300 "$TMP/body" | tr '\n' ' ')"
    fi
}

# check NAME METHOD PATH BODY EXPECTED_CODE [SUBSTRING ...]
check() {
    local name="$1" method="$2" path="$3" body="$4" want="$5"; shift 5
    local code; code="$(req "$method" "$path" "$body")"
    if [ "$code" != "$want" ]; then
        bad "$name" "$method $path returned $code, expected $want"
        return 1
    fi
    local needle
    for needle in "$@"; do
        if ! grep -q -- "$needle" "$TMP/body"; then
            bad "$name" "response is missing $needle"
            return 1
        fi
    done
    ok "$name"
    return 0
}

# Field extraction without a JSON parser. Go's encoder emits compact,
# sorted-key output, and llama.cpp's fields here are flat scalars -- so these
# two are sufficient and keep the script's only dependency at curl.
jstr() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" "$TMP/body" | head -1; }
jnum() { sed -n "s/.*\"$1\":\([0-9][0-9]*\).*/\1/p" "$TMP/body" | head -1; }

echo
echo "smoke: $BASE  (mode=$MODE)"
echo

# --- our own surface --------------------------------------------------------

echo "gobbonet:"

# upstream_ok is the check that makes the rest meaningful: without it a healthy
# file server reports "ok" while proxying into a void.
check "health-fileserver" GET /health-fileserver "" 200 \
    '"status":"ok"' "\"mode\":\"$MODE\"" '"upstream_ok":true'

# The route whose absence motivated the whole port: the wildcard swallowed it,
# the client parsed the empty body without complaint, and boot-time conflict
# detection just stopped firing.
#
# Two shapes are correct, and which one you get depends only on whether a state
# file exists yet: 404 with that exact envelope, or 200 carrying mtime and size
# (both pinned in conformance_test.go). What must never happen is a reply from
# the static handler, which is what being swallowed looks like -- so this
# accepts either shape and rejects anything that is neither.
code="$(req GET /state/info "")"
if [ "$code" = "404" ] && grep -q '"no state on server"' "$TMP/body"; then
    ok "state/info (no state yet)"
elif [ "$code" = "200" ] && grep -q '"mtime"' "$TMP/body" && grep -q '"size"' "$TMP/body"; then
    ok "state/info"
else
    bad "state/info" "got $code with a body the state handler would not produce"
fi

# A model identified as "(llama.cpp server unreachable)" means /props answered
# with something we could not read -- generation may still work, so nothing
# else here would notice.
if check "active-model.json" GET /active-model.json "" 200 '"family"'; then
    if grep -q 'unreachable' "$TMP/body"; then
        bad "active-model.json" "model unidentified -- /props answered but was not understood"
    fi
fi

# --- llama.cpp's HTTP surface, through the proxy ----------------------------
#
# Reached via /llm/* on purpose. That is the path chat.html uses, so it tests
# the proxy's prefix stripping at the same time as the upstream's routes.

echo
echo "llama.cpp HTTP (via /llm):"

# supervisor.go:548 gates every start and every hot-swap on this; server.go:338
# reports it as upstream_ok; js/11-search.js:199 polls it.
check "GET /health" GET /llm/health "" 200

# models/info.go:85 reads chat_template, model_path and
# default_generation_settings.n_ctx out of this. A rename here silently
# downgrades every model to the unidentified branch.
check "GET /props" GET /llm/props "" 200 '"chat_template"' '"default_generation_settings"'

# js/06-state-sync.js:758. No OpenAI equivalent exists -- this is one of the
# two routes that make "point it at a hosted API" impossible by construction.
check "POST /tokenize" POST /llm/tokenize '{"content":"hello world"}' 200 '"tokens"'

# js/06-state-sync.js:668. The other one.
check "POST /apply-template" POST /llm/apply-template \
    '{"messages":[{"role":"user","content":"hi"}]}' 200 '"prompt"'

# jobs.go:362 and four frontend call sites.
check "POST /v1/chat/completions" POST /llm/v1/chat/completions \
    '{"messages":[{"role":"user","content":"Say hi."}],"max_tokens":16,"temperature":0}' \
    200 '"choices"' '"content"'

# --- detached generation ----------------------------------------------------
#
# The reason a reply survives a locked phone. Its wire format is inherited from
# fileserver.ps1: base64 in chunk_b64, a plain byte window, no character
# alignment. js/03-generation.js reads chunk_b64 and nothing else -- and does
# not error on an unrecognised payload, it just polls a healthy job forever
# with nothing to show. That silence is what these two checks are for.

echo
echo "jobs:"

job_body='{"messages":[{"role":"user","content":"Count to three."}],"max_tokens":24,"temperature":0,"stream":true}'

# 202, not 200: the job is accepted and runs detached. Pinned here because the
# frontend treats any non-2xx as a failed send.
code="$(req POST /llm/jobs "$job_body")"
if [ "$code" != "202" ]; then
    bad "POST /llm/jobs" "returned $code, expected 202"
else
    id="$(jstr id)"
    if ! printf '%s' "$id" | grep -qE '^[0-9a-f]{32}$'; then
        bad "POST /llm/jobs" "no 32-hex job id in the response"
    else
        ok "POST /llm/jobs"
        off=0
        saw_chunk=0
        status=""
        deadline=$(( $(date +%s) + JOB_TIMEOUT ))
        while [ "$(date +%s)" -lt "$deadline" ]; do
            code="$(req GET "/llm/jobs/$id?from=$off")"
            [ "$code" = "200" ] || { bad "poll /llm/jobs/{id}" "returned $code"; break; }
            grep -q '"chunk_b64"' "$TMP/body" && saw_chunk=1
            next="$(jnum next)"; [ -n "$next" ] && off="$next"
            status="$(jstr status)"
            case "$status" in done|error) break ;; esac
            sleep 1
        done
        case "$status" in
            done)  ok "job reaches done" ;;
            error) bad "job reaches done" "job ended in error: $(jstr error)" ;;
            *)     bad "job reaches done" "job still '$status' after ${JOB_TIMEOUT}s" ;;
        esac
        if [ "$saw_chunk" = 1 ]; then
            ok "job streams chunk_b64"
        else
            bad "job streams chunk_b64" "no chunk_b64 in any poll -- the frontend would hang silently"
        fi
    fi
fi

# Cancellation is a context that propagates into the upstream request, so this
# also asserts the slot is released rather than merely marked.
# 202 while the upstream connection is still being torn down, 200 once the job
# is gone. Both are success; which one you get is a timing detail.
code="$(req POST /llm/jobs "$job_body")"
if [ "$code" = "202" ]; then
    id="$(jstr id)"
    code="$(req POST "/llm/jobs/$id/cancel")"
    if { [ "$code" = "200" ] || [ "$code" = "202" ]; } &&
       grep -qE '"status":"(cancelling|deleted)"' "$TMP/body"; then
        ok "cancel a running job"
    else
        bad "cancel a running job" "cancel returned $code"
    fi
else
    bad "cancel a running job" "could not create a job to cancel ($code)"
fi

# --- local mode only --------------------------------------------------------
#
# Everything below needs gobbonet to own the llama-server process. Remote mode
# answers 503 here by design and reports hotswap:false so the frontend adapts.

if [ "$MODE" = "local" ]; then
    echo
    echo "supervision (local mode):"

    if check "models-list.json" GET /models-list.json "" 200 '"models"'; then
        if [ -n "$MODEL" ] && ! grep -q "$MODEL" "$TMP/body"; then
            bad "models-list.json" "$MODEL missing -- the model_dir scan did not find it"
        fi
    fi

    if [ -n "$MODEL" ]; then
        code="$(req POST /swap-model "{\"file\":\"$MODEL\"}")"
        if [ "$code" != "202" ]; then
            bad "POST /swap-model" "returned $code, expected 202"
        else
            ok "POST /swap-model"
            phase=""
            deadline=$(( $(date +%s) + SWAP_TIMEOUT ))
            while [ "$(date +%s)" -lt "$deadline" ]; do
                req GET /swap-status "" >/dev/null
                phase="$(jstr phase)"
                case "$phase" in ready|error) break ;; esac
                sleep 1
            done
            case "$phase" in
                ready) ok "swap reaches ready" ;;
                error) bad "swap reaches ready" "swap failed: $(jstr message)" ;;
                *)     bad "swap reaches ready" "phase stuck at '$phase' after ${SWAP_TIMEOUT}s" ;;
            esac
        fi

        # The engine has to still be serving after a swap. A supervisor that
        # reports ready while the process is gone is the exact failure the
        # process-group sweep exists to prevent.
        check "generation survives the swap" POST /llm/v1/chat/completions \
            '{"messages":[{"role":"user","content":"hi"}],"max_tokens":8,"temperature":0}' \
            200 '"choices"'
    fi
else
    echo
    echo "supervision (remote mode -- hot-swap must be refused, not faked):"
    code="$(req POST /swap-model '{"file":"anything.gguf"}')"
    if [ "$code" = "503" ]; then
        ok "swap-model answers 503"
    else
        bad "swap-model answers 503" "returned $code"
    fi
fi

# --- verdict ----------------------------------------------------------------

echo
if [ "$fail" -eq 0 ]; then
    echo "PASS: $pass checks"
    exit 0
fi
echo "FAIL: $fail of $((pass+fail)) checks$failures"
exit 1
