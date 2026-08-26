#!/usr/bin/env bash
#
# Stand up a gobbonet against a real llama.cpp, run the contract suite, tear it
# all down. One command, same on a runner and on your desk:
#
#   .github/scripts/run-contracts.sh --mode local
#   .github/scripts/run-contracts.sh --mode remote
#   .github/scripts/run-contracts.sh --mode local --llama master   # canary
#
# The workflows call exactly this. Nothing about the test lives in YAML, so
# reproducing a CI failure is one line rather than a reading exercise.
#
#   --mode local    gobbonet spawns and supervises llama-server (server_exe set)
#   --mode remote   we start llama-server; gobbonet only proxies to it
#
# Those two are the whole grid. The compute backend behind llama.cpp is
# invisible to gobbonet -- CPU builds test the same interfaces a CUDA build
# exposes -- so there is no third axis here. See CI_TESTING.md.
set -euo pipefail

cd "$(dirname "$0")/../.."

MODE=local
LLAMA_TAG=""
PORT="${PORT:-19066}"
ENGINE_PORT="${ENGINE_PORT:-19437}"
WORK="${WORK:-.ci}"

while [ $# -gt 0 ]; do
    case "$1" in
        --mode)  MODE="${2:?--mode needs local|remote}"; shift ;;
        --llama) LLAMA_TAG="${2:?--llama needs a tag}"; shift ;;
        --port)  PORT="${2:?}"; shift ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
    shift
done
case "$MODE" in local|remote) ;; *) echo "--mode must be local or remote" >&2; exit 2 ;; esac

mkdir -p "$WORK/run"
LOG_DIR="$WORK/run/$MODE"
rm -rf "$LOG_DIR"; mkdir -p "$LOG_DIR/data"

GOBBONET_PID=""
ENGINE_PID=""

cleanup() {
    local rc=$?
    # Show what the server saw before anything is torn down -- a failed run
    # whose logs vanished with the container is a failed run you cannot read.
    if [ "$rc" -ne 0 ]; then
        echo
        echo "----- gobbonet log -----"; tail -40 "$LOG_DIR/gobbonet.log" 2>/dev/null || true
        echo "----- llama-server log -----"; tail -40 "$LOG_DIR/engine.log" 2>/dev/null || true
    fi
    [ -n "$GOBBONET_PID" ] && kill "$GOBBONET_PID" 2>/dev/null || true
    [ -n "$ENGINE_PID" ]   && kill "$ENGINE_PID"   2>/dev/null || true
    # Local mode's llama-server is gobbonet's child; killing the parent should
    # take the group with it. Give it a moment to actually do that before the
    # next run tries to bind the same port.
    sleep 2
    exit $rc
}
trap cleanup EXIT INT TERM

# Resolve the tag here rather than letting fetch-engine.sh default to it, so
# that an empty --llama can never be mistaken for a positional argument.
[ -n "$LLAMA_TAG" ] || LLAMA_TAG="$(.github/scripts/llama-pin.sh)"
SERVER="$(.github/scripts/fetch-engine.sh "$LLAMA_TAG" "$WORK/engine")"
MODEL_PATH="$(.github/scripts/fetch-model.sh "$WORK/models")"
MODEL_FILE="$(basename "$MODEL_PATH")"
MODEL_DIR="$(cd "$(dirname "$MODEL_PATH")" && pwd)"
SERVER_ABS="$(cd "$(dirname "$SERVER")" && pwd)/$(basename "$SERVER")"

echo "engine: $SERVER_ABS"
echo "model:  $MODEL_PATH"
echo "mode:   $MODE"

BIN="$WORK/run/gobbonet"
[ "${OS:-}" = "Windows_NT" ] && BIN="$BIN.exe"
go build -o "$BIN" ./cmd/gobbonet
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"

# Forward slashes throughout, including on Windows: Go accepts them and it keeps
# this file from needing two versions of every path.
DATA_DIR="$(cd "$LOG_DIR/data" && pwd)"

CONF="$LOG_DIR/config.toml"
{
    echo "listen_host = \"127.0.0.1\""
    echo "listen_port = $PORT"
    echo "data_dir = \"${DATA_DIR//\\//}\""
    echo "ctx_size = 2048"
    echo "gpu_layers = 0"
    echo "kv_cache_type = \"f16\""
    if [ "$MODE" = "local" ]; then
        # The engine's port is gobbonet's to choose in local mode: it launches
        # the process and then proxies to the address it was told to use.
        echo "llm_url = \"http://127.0.0.1:$ENGINE_PORT\""
        echo "server_exe = \"${SERVER_ABS//\\//}\""
        echo "model_dir = \"${MODEL_DIR//\\//}\""
    else
        echo "llm_url = \"http://127.0.0.1:$ENGINE_PORT\""
        echo "server_exe = \"\""
    fi
} > "$CONF"

wait_for() {
    local url="$1" what="$2" secs="${3:-120}"
    local deadline=$(( $(date +%s) + secs ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if curl -fsS -o /dev/null "$url" 2>/dev/null; then return 0; fi
        sleep 1
    done
    echo "ERROR: $what did not answer $url within ${secs}s" >&2
    return 1
}

if [ "$MODE" = "remote" ]; then
    # Externally managed: started here, with the same argv the supervisor would
    # have used, so remote mode is tested against an engine configured the way
    # local mode configures one.
    "$SERVER_ABS" --model "$MODEL_PATH" --port "$ENGINE_PORT" --host 127.0.0.1 \
        --ctx-size 2048 --n-gpu-layers 0 --parallel 1 --jinja \
        --reasoning-format auto > "$LOG_DIR/engine.log" 2>&1 &
    ENGINE_PID=$!
    wait_for "http://127.0.0.1:$ENGINE_PORT/health" "llama-server"
fi

# --no-auth because the password gate is orthogonal to every contract here, and
# set-password needs a terminal by design (it refuses to run unattended rather
# than leave a server open). The gate itself is covered by conformance_test.go,
# which asserts the 401 and the login flow without needing a process at all.
"$BIN_ABS" serve --config "$CONF" --no-auth > "$LOG_DIR/gobbonet.log" 2>&1 &
GOBBONET_PID=$!
wait_for "http://127.0.0.1:$PORT/favicon.ico" "gobbonet" 180

if [ "$MODE" = "local" ]; then
    .github/scripts/smoke.sh "http://127.0.0.1:$PORT" --local --model "$MODEL_FILE"
else
    .github/scripts/smoke.sh "http://127.0.0.1:$PORT"
fi
