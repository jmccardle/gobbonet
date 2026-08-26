#!/usr/bin/env bash
#
# Download a llama.cpp release build and print the path to its llama-server.
#
#   .github/scripts/fetch-engine.sh            # the build launch.bat pins
#   .github/scripts/fetch-engine.sh master     # whatever is newest (canary)
#   .github/scripts/fetch-engine.sh b9294 /tmp/engines
#
# Deliberately the *release archive* and not the container image: this is
# byte-for-byte what launch.bat hands a user, so a release that stops building,
# renames an asset, or drops a platform fails here rather than in somebody's
# install. Local mode needs a real executable on the same filesystem anyway.
#
# CPU builds only. The compute backend is invisible to gobbonet -- what it
# depends on is llama-server's CLI and HTTP surface, and those are identical
# across backends. See CI_TESTING.md.
set -euo pipefail

cd "$(dirname "$0")/../.."

TAG="${1:-}"
[ -n "$TAG" ] || TAG="$(.github/scripts/llama-pin.sh)"
DEST="${2:-.ci/engine}"

# "master" means the newest build, whatever it is. Resolving it to a real tag up
# front matters: the canary run has to be able to say WHICH build broke us.
#
# Explicitly NOT /releases/latest. Upstream also cuts semver releases (v0.3.0
# and friends) and GitHub marks one of those "latest" -- but they carry no
# binary assets at all, so that endpoint hands you a tag whose downloads 404.
# The builds we want are the bNNNN tags, newest first in the plain listing.
if [ "$TAG" = "master" ] || [ "$TAG" = "latest" ]; then
    TAG="$(curl -fsSL 'https://api.github.com/repos/ggml-org/llama.cpp/releases?per_page=30' \
        | sed -n 's/.*"tag_name": *"\(b[0-9][0-9]*\)".*/\1/p' | head -1)"
    [ -n "$TAG" ] || { echo "ERROR: could not resolve a bNNNN llama.cpp build" >&2; exit 1; }
    echo "resolved newest build -> $TAG" >&2
fi

case "$(uname -s)" in
    Linux)  asset="llama-$TAG-bin-ubuntu-x64.tar.gz" ;;
    Darwin)
        case "$(uname -m)" in
            arm64) asset="llama-$TAG-bin-macos-arm64.tar.gz" ;;
            *)     asset="llama-$TAG-bin-macos-x64.tar.gz" ;;
        esac ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
        # The CPU build, not the win-vulkan-x64 one launch.bat ships. A CI
        # runner has no Vulkan device, and this script's job is to exercise
        # llama-server's interfaces, not its backends. The vulkan asset is
        # still checked -- by its pinned SHA, in the workflow.
        asset="llama-$TAG-bin-win-cpu-x64.zip" ;;
    *)  echo "ERROR: no llama.cpp release asset known for $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

url="https://github.com/ggml-org/llama.cpp/releases/download/$TAG/$asset"
mkdir -p "$DEST"
archive="$DEST/$asset"

if [ ! -f "$archive" ]; then
    # --fail so an HTML error page never lands on disk looking like an archive,
    # and --retry because a truncated download otherwise presents as a corrupt
    # engine much further downstream.
    curl -fsSL --retry 3 --retry-all-errors -o "$archive.part" "$url"
    mv "$archive.part" "$archive"
fi

work="$DEST/$TAG"
if [ ! -d "$work" ]; then
    mkdir -p "$work.part"
    case "$asset" in
        *.tar.gz) tar xzf "$archive" -C "$work.part" ;;
        *.zip)
            # Git Bash on a Windows runner does not always carry unzip, and the
            # alternatives that are always there differ. Try each, and say so
            # plainly if none exists rather than leaving an empty directory
            # that fails later as "no llama-server in the archive".
            if command -v unzip >/dev/null 2>&1; then
                unzip -q "$archive" -d "$work.part"
            elif command -v 7z >/dev/null 2>&1; then
                7z x -y -o"$work.part" "$archive" >/dev/null
            elif command -v powershell >/dev/null 2>&1; then
                powershell -NoProfile -Command \
                    "Expand-Archive -LiteralPath '$archive' -DestinationPath '$work.part' -Force"
            else
                echo "ERROR: no unzip, 7z or powershell available to extract $asset" >&2
                exit 1
            fi ;;
    esac
    mv "$work.part" "$work"
fi

server="$(find "$work" -type f \( -name llama-server -o -name llama-server.exe \) | head -1)"
[ -n "$server" ] || { echo "ERROR: $asset contains no llama-server" >&2; exit 1; }
chmod +x "$server" 2>/dev/null || true

# Confirm the binary IS the build we asked for. A tag that silently resolves to
# different bits -- a re-cut release, a stale cache, an asset copied from the
# wrong run -- would otherwise produce a green suite describing the wrong
# engine, which is the whole failure this pin exists to prevent.
# Two formats in the wild, and the change happened between the build this tree
# pins and today's:
#
#   b9294   version: 9294 (0f3cb3fc8)
#   b10639  version: 0.3.0-dev (build 10639, commit 5e6a37cb1)
#
# Read the build number out of either. This is the smallest possible example of
# why the canary job exists: a cosmetic upstream change to a string nobody
# thinks of as an interface, which breaks anything parsing it.
ver="$("$server" --version 2>&1 | head -1)"
got="$(printf '%s' "$ver" | sed -n 's/.*build \([0-9][0-9]*\).*/\1/p')"
[ -n "$got" ] || got="$(printf '%s' "$ver" | sed -n 's/^version: \([0-9][0-9]*\).*/\1/p')"
want="${TAG#b}"
if [ -n "$got" ] && [ "$got" != "$want" ]; then
    echo "ERROR: $asset reports build $got, expected $want" >&2
    exit 1
fi

printf '%s\n' "$server"
