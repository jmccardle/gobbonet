# Container image for the Go server.
#
#   podman build -t gobbonet .
#
# WHAT THIS IMAGE IS: the runtime half only -- the web UI, the auth gate, state
# sync, generation jobs and the proxy to llama.cpp. It is the "remote mode"
# server described in GO_SERVER.md, and that is deliberate:
#
#   * Local mode means this process starts and supervises llama-server, which
#     means the engine has to be inside the image. A CPU-only engine is the one
#     build we already refuse to ship (see the installer), and a CUDA/ROCm one
#     is a per-vendor image plus device passthrough -- a different piece of work
#     with a different failure surface.
#   * So llama.cpp stays somebody else's process. Point llm_url at it, on the
#     host or on another container, exactly as a remote-mode install does.
#
# compose.yaml starts an upstream llama.cpp container alongside this one and
# wires the two together, which is the shortest path to a working stack. What
# follows is the standalone flow, for an engine you already run.
#
# Hot-swap is therefore unavailable here and /health-fileserver says so; every
# other runtime feature has full parity. First-run setup (hardware probe, guided
# model download) is Windows-only and is not in this image either.
#
# FIRST RUN, in the order the server asks for it. All three commands share one
# config because XDG_CONFIG_HOME is set below, so discovery lands on the same
# file every time:
#
#   podman volume create gobbonet-config
#   podman volume create gobbonet-data
#
#   # 1. Writes the commented config.toml into the volume and stops. This is the
#   #    designed behaviour, not a failure to work around: llm_url defaults to
#   #    127.0.0.1:11437, which inside a container points at the container.
#   podman run --rm -v gobbonet-config:/config gobbonet
#
#   # 2. Point it at the engine. host.containers.internal is the host from
#   #    inside a rootless podman container.
#   podman run --rm -v gobbonet-config:/config gobbonet \
#       config set llm_url http://host.containers.internal:11437
#
#   # 3. Set the password. Needs a TTY -- the server refuses to start without a
#   #    secret rather than quietly serving the chat to the whole LAN.
#   podman run --rm -it -v gobbonet-config:/config gobbonet set-password
#
#   # 4. Serve.
#   podman run -d --name gobbonet -p 9066:9066 \
#       -v gobbonet-config:/config -v gobbonet-data:/data gobbonet
#
# Bind-mounting a directory instead of using named volumes works too, but the
# directory has to be writable by uid 1000 -- the image drops to a non-root user
# before it ever touches /config.

# --- Go binary ---------------------------------------------------------------

FROM docker.io/library/golang:1.25-bookworm AS build

WORKDIR /src

# Dependencies first so a frontend or source edit doesn't re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# The build identity, stamped the same way build-release.sh stamps it:
#
#   podman build -t gobbonet \
#       --build-arg GOBBONET_VERSION="$(cat VERSION)-go-$(git rev-parse --short HEAD)" .
#
# Left empty the binary reports "dev", which is the point: an unstamped build
# must never be mistakable for a distributed one, and inventing a number here
# from a context that may have no .git at all would do exactly that.
ARG GOBBONET_VERSION=""

# CGO off and -trimpath to match the release build: one static file, no runtime
# dependencies, no build paths baked into it.
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w ${GOBBONET_VERSION:+-X github.com/jmccardle/gobbonet/internal/version.Version=$GOBBONET_VERSION}" \
        -o /out/gobbonet ./cmd/gobbonet

# --- Web root ----------------------------------------------------------------
#
# Staged by the same script the release build uses, rather than COPYing the
# frontend files into place by hand. stage-web.sh checks that js/ and css/ hold
# as many files as chat.html actually asks for, so an upstream merge that drops
# a module fails the image build instead of producing a blank page with console
# errors. Its output (web/) is generated and gitignored, so it cannot be copied
# from the context either way.

FROM docker.io/library/golang:1.25-bookworm AS web

WORKDIR /src
COPY chat.html default-characters.json gobbonet.ico stage-web.sh ./
COPY js ./js
COPY css ./css
RUN ./stage-web.sh

# --- Runtime -----------------------------------------------------------------

FROM docker.io/library/alpine:3.22

# ca-certificates is a real dependency, not hygiene: /search proxies to
# https://ollama.com/api, the one route that leaves the machine. Without roots
# every web search fails TLS verification.
RUN apk add --no-cache ca-certificates

# uid/gid pinned so a bind-mounted host directory can be chowned to match.
RUN addgroup -g 1000 gobbonet \
 && adduser -D -u 1000 -G gobbonet -h /home/gobbonet gobbonet \
 && mkdir -p /config /data \
 && chown gobbonet:gobbonet /config /data

# Config and data stay separate here for the same reason they do on disk: the
# data volume holds the state backup, downloaded models and logs, and nothing
# large is ever written beside config.toml.
#
# XDG_CONFIG_HOME rather than GOBBONET_CONFIG: an explicit config path is
# treated as a promise that the file exists, so a missing one is an error
# instead of the first-run write that bootstraps step 1 above.
ENV XDG_CONFIG_HOME=/config \
    XDG_DATA_HOME=/data

COPY --from=build /out/gobbonet /app/gobbonet
COPY --from=web /src/web /app/web

# detectWebRoot() looks beside the binary first, so /app/web is found without
# any config; WORKDIR keeps a relative path in a hand-edited config sane.
WORKDIR /app

VOLUME ["/config", "/data"]

EXPOSE 9066

USER gobbonet

# /favicon.ico is the only route served without auth, so it is the only one that
# answers 200 to a probe holding no session. GOBBONET_LISTEN_PORT is the same
# variable the server reads, so overriding the port keeps the check pointed at
# it; changing listen_port in config.toml alone does not, and shows up as a
# container that serves fine but reports unhealthy.
#
# Podman's default OCI image format has nowhere to record this, and drops it
# with a warning at build time. `podman build --format docker` keeps it; without
# that, attach the same probe at run time instead:
#
#   podman run ... --health-cmd 'wget -q --spider http://127.0.0.1:9066/favicon.ico'
#
# Docker builds keep it either way.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -q --spider "http://127.0.0.1:${GOBBONET_LISTEN_PORT:-9066}/favicon.ico" || exit 1

# Entrypoint is the binary itself so every subcommand is reachable --
# set-password, config get/set, check, version -- with `serve` as the default.
ENTRYPOINT ["/app/gobbonet"]
CMD ["serve"]
