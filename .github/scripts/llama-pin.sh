#!/usr/bin/env bash
#
# Print the llama.cpp build this tree is pinned to.
#
#   .github/scripts/llama-pin.sh          -> b9294
#   .github/scripts/llama-pin.sh --sha    -> the win-vulkan-x64 zip's SHA-256
#
# The pin lives in launch.bat and nowhere else. CI reads it from there rather
# than carrying its own copy, for the same reason build-release.sh and
# installer/build-installer.sh both read VERSION: two literals saying the same
# thing is two literals that drift, and the drift is silent. If CI tested b9294
# while the installer shipped b9400, every green run would be describing an
# engine no user is running.
#
# A pin that cannot be parsed is a hard error, not a default. Guessing a build
# number here would produce a test suite that passes against something nobody
# ships.
set -euo pipefail

cd "$(dirname "$0")/../.."

BAT="launch.bat"
[ -f "$BAT" ] || { echo "ERROR: $BAT not found; run this from the repo" >&2; exit 1; }

want="LLAMA_PIN_TAG"
[ "${1:-}" = "--sha" ] && want="LLAMA_PIN_SHA256"

# Matches:  set "LLAMA_PIN_TAG=b9294"
value="$(sed -n "s/^[[:space:]]*set[[:space:]]*\"${want}=\([^\"]*\)\".*/\1/p" "$BAT" | head -1)"

if [ -z "$value" ]; then
    echo "ERROR: could not read $want from $BAT." >&2
    echo "       The line it expects looks like:  set \"$want=b9294\"" >&2
    echo "       If launch.bat moved the pin, update this script -- do not" >&2
    echo "       hardcode a build number in the workflows." >&2
    exit 1
fi

printf '%s\n' "$value"
