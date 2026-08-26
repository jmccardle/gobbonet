#!/usr/bin/env bash
#
# Download the GGUF the contract tests run against, and print its path.
#
#   .github/scripts/fetch-model.sh [DEST_DIR]
#
# SmolLM2-135M-Instruct at Q4_K_M: 105 MB, loads on a CI runner in about a
# second, and -- the part that matters -- it is a real instruct model with a
# real chat template embedded in the GGUF. A tiny non-instruct model would make
# /apply-template and /v1/chat/completions test a fallback path no user is on.
#
# Its architecture is llama, so it also exercises models.IdentifyFile's header
# read and the llama branch of the classifier. That is a happy side effect, not
# the coverage plan: family classification is unit-tested in classify_test.go
# and needs no engine at all.
#
# Size and SHA-256 are pinned. A silently truncated download is the specific
# failure this guards: an interrupted fetch here produced a 17 MB "GGUF" that
# curl reported as a success and that only failed much later, inside the
# engine, as an unhelpful parse error.
set -euo pipefail

REPO="bartowski/SmolLM2-135M-Instruct-GGUF"
FILE="SmolLM2-135M-Instruct-Q4_K_M.gguf"
SIZE=105454432
SHA256="2e8040ceae7815abe0dcb3540b9995eaa1fa0d2ca9e797d0a635ae4433c68c2d"

DEST="${1:-.ci/models}"
mkdir -p "$DEST"
path="$DEST/$FILE"

sha_of() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
    else shasum -a 256 "$1" | cut -d' ' -f1    # macOS
    fi
}
size_of() {
    if stat -c%s "$1" >/dev/null 2>&1; then stat -c%s "$1"
    else stat -f%z "$1"                        # macOS / BSD
    fi
}

if [ -f "$path" ] && [ "$(size_of "$path")" = "$SIZE" ]; then
    printf '%s\n' "$path"
    exit 0
fi

url="https://huggingface.co/$REPO/resolve/main/$FILE"
curl -fsSL --retry 3 --retry-all-errors -o "$path.part" "$url"

got_size="$(size_of "$path.part")"
if [ "$got_size" != "$SIZE" ]; then
    rm -f "$path.part"
    echo "ERROR: $FILE downloaded $got_size bytes, expected $SIZE." >&2
    echo "       Truncated transfer, or the upstream file changed." >&2
    exit 1
fi

got_sha="$(sha_of "$path.part")"
if [ "$got_sha" != "$SHA256" ]; then
    rm -f "$path.part"
    echo "ERROR: $FILE has SHA-256 $got_sha, expected $SHA256." >&2
    echo "       The upstream file was replaced. Verify the new one by hand and" >&2
    echo "       update the pin in this script -- do not just take what arrives." >&2
    exit 1
fi

mv "$path.part" "$path"
printf '%s\n' "$path"
