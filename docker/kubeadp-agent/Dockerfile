# kubeadp-agent image: ONE image replacing the two old per-pod images
# (docker/ai-server/Dockerfile for the orchestrator pod, docker/agent-run-api/
# Dockerfile for the worker pod), built from the one cargo package
# src/kubeadp-agent. Nothing consumes this image yet (test pods first); the two
# old Dockerfiles, their workflows and their manifests stay untouched until the
# cut-over (HL-70 phase 3).
#
# What it contains (superset of both old images, one role per pod via env):
#   - the `kubeadp-agent` binary (role chosen by KUBEADP_ROLE=orchestrator|worker
#     or subcommand; unset/other exits 2) and the CLI agent-run
#   - BOTH harness engines: claude-code (+ its node runtime) as docker/ai-server/
#     builds it, and agy at the core's default agy_path /usr/local/libexec/agy as
#     docker/agent-run-api/ installs it
#   - ttyd (pinned release + checksum, as in docker/ai-server/) so the
#     orchestrator pod can run its interactive OAuth shell as a sidecar container
#     from this same image
#   - the runtime packages both old images carry, plus tini (PID 1, subreaper via
#     the ENTRYPOINT) supporting the worker role's documented child-session
#     behaviour under an init
# The tools volume (/opt/tools, read-only OCI image volume docker/modules) stays
# external as today; CA settings and the /opt/tools PATH are the pod manifests'
# concern (the image ENV is not read by pods), so they are kept as the old images
# had them for LOCAL/dev runs.
#
# BASE IMAGE reconciliation (ai-server used codercom/code-server:4.138.0,
# agent-run-api debian:bookworm-slim): one base is required. debian:bookworm-slim
# is chosen: it is what the worker image runs today, it is the common root of all
# three bases (code-server itself is debian:bookworm-slim + code-server), and it
# lets the final stage be one self-owned layer instead of a passive overlay
# on a code-server image whose owned files (code-server, its libraries, its
# default ENV/ENTRYPOINT/shell profile) the new image does not use. Nothing the
# orchestrator pod needs comes from code-server itself: ttyd (baked below)
# replaces code-server in the orchestrator pod, claude-code/node are baked from
# the node base (as in docker/ai-server/), the tools come from /opt/tools, and
# the home/workspace volumes are the pod's. The code-server UID (1000, group of
# the same name) is reproduced exactly, so the PVC fsGroup 1000/runAsUser 1000
# ownership model is byte-identical for the orchestrator pod.
#
# Stage: CI checks (fmt, clippy across the crate's feature sets, tests).
# Built via `docker build --target check` in CI (as build-info-hub-mcp does for
# its crate). Not in the default final-image dependency chain.
FROM rust:1.99.0-slim-bookworm AS check

RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
    gcc libc6-dev binutils git python3 \
    && rm -rf /var/lib/apt/lists/* \
    && rustup component add clippy rustfmt

# The crate's tests drive real git repositories (worktrees, commits) and a python3 child (pty tests): git, a commit identity and python3 are needed.
ENV GIT_AUTHOR_NAME=ci GIT_AUTHOR_EMAIL=ci@localhost GIT_COMMITTER_NAME=ci GIT_COMMITTER_EMAIL=ci@localhost

WORKDIR /build/kubeadp-agent
COPY src/kubeadp-agent /build/kubeadp-agent

ENV CARGO_BUILD_JOBS=2
ENV CARGO_INCREMENTAL=0
ENV CARGO_PROFILE_RELEASE_DEBUG=0
ENV CARGO_NET_RETRY=10
ENV CARGO_HTTP_TIMEOUT=60

RUN for i in 1 2 3 4 5 6; do \
      [ "$i" -gt 1 ] && { sleep 10; echo "cargo fetch retry $i" >&2; }; \
      cargo fetch --locked && break; \
    done \
    && cargo fmt --check \
    && cargo clippy --locked --all-targets -- -D warnings \
    && cargo clippy --locked --all-targets --features a2a,test-hooks -- -D warnings \
    && cargo clippy --locked --all-targets --no-default-features --features cli -- -D warnings \
    && cargo clippy --locked --all-targets --no-default-features --features server -- -D warnings \
    && cargo test --locked --features a2a,test-hooks

# Stage 1: tini, the pod's PID 1. tini is invoked WITHOUT -g (as the ai-server
# manifest documents): the orchestrator entrypoint installs its own TERM trap and
# signals only the children it started by PID. The worker role's agent-run-api
# server also sets PR_SET_CHILD_SUBREAPER itself at startup (source comment in
# src/kubeadp-agent/src/worker/entry.rs), so under tini orphaned job children
# reparent to the subreaper server exactly as they did when the server was its
# own PID 1 (tini only reaps what the server does not: the server's own direct
# children that exit before being wait()ed; the documented agent-run-api
# behaviour -- agy in its own session, PR_SET_CHILD_SUBREAPER at startup -- is
# unchanged).
FROM debian:bookworm-slim AS cli-tools
RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      curl \
      ca-certificates \
      && rm -rf /var/lib/apt/lists/* \
      && mkdir -p /payload/usr/local/bin \
      && curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o /payload/usr/local/bin/tini \
         "https://github.com/krallin/tini/releases/download/v0.19.0/tini-amd64" \
      && chmod 755 /payload/usr/local/bin/tini

# Stage 2: build the binaries of the one agent package (src/kubeadp-agent).
# rust:1-slim-bookworm is the pinned slim variant both old images use. The
# default feature set is cli+server, which is what the image needs: the server
# binary requires `server`, agent-run requires cli.
# BUILD_SHA is compiled in from AGENT_RUN_BUILD_SHA exactly as the old
# agent-run-builder/api-builder stages did for the agent-run-api binary.
# Released binaries are stripped, as today. Only src/kubeadp-agent is copied.
FROM rust:1.99.0-slim-bookworm AS agent-builder
# gcc + libc6-dev: the only link packages this build needs (rusqlite's bundled
# sqlite is compiled from C via the cc crate); binutils supplies `strip`. Same
# as docker/ai-server/Dockerfile's agent-run-builder stage; the agent-run-api
# builder additionally adds clippy/rustfmt for the pod's own lint steps (jobs do
# that from the tools image today: /opt/tools has clippy+rustfmt, so the image's
# own builder does not need them here).
RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
    gcc libc6-dev binutils \
    && rm -rf /var/lib/apt/lists/*
COPY src/kubeadp-agent /build/kubeadp-agent
WORKDIR /build/kubeadp-agent
# CARGO_BUILD_JOBS=2: on the 16 vCPU runner the default codegen wave (16 rustc
# processes) OOM-killed the gvisor_sentry at the 8Gi memcg limit (see
# docker/ai-server/Dockerfile for the full history). Capping to 2 keeps the peak
# under the limit; the runner is memory-bandwidth-bound, so wall time is barely
# affected.
ENV CARGO_BUILD_JOBS=2
# No incremental metadata, no debuginfo in the release profile: lower per-rustc
# memory peak. The binaries are stripped either way.
ENV CARGO_INCREMENTAL=0
ENV CARGO_PROFILE_RELEASE_DEBUG=0
# CARGO_NET_RETRY=10 + CARGO_HTTP_TIMEOUT=60: the ARC runner pods resolve DNS
# through the cluster resolver over a VPN that intermittently times out (run
# 36633797174 history in docker/ai-server/Dockerfile).
ENV CARGO_NET_RETRY=10
ENV CARGO_HTTP_TIMEOUT=60
ARG BUILD_SHA=unknown
ENV AGENT_RUN_BUILD_SHA=$BUILD_SHA
# Dependencies are fetched in the SAME RUN as the build with up to 6 attempts
# (10s apart): one failed fetch must not kill the RUN, but all 6 failing MUST
# fail the build. The fetch is deliberately the LAST command of the loop body,
# so the for-loop's exit status is 0 when `break` ran after a successful fetch,
# or cargo's error when attempt 6 fails (commentary in docker/ai-server).
RUN for i in 1 2 3 4 5 6; do \
      [ "$i" -gt 1 ] && { sleep 10; echo "cargo fetch retry $i" >&2; }; \
      cargo fetch --locked && break; \
    done \
    && cargo build --release --locked --features a2a --bin kubeadp-agent --bin agent-run \
    && strip target/release/kubeadp-agent target/release/agent-run \
    && mkdir -p /payload/usr/local/bin \
    && cp -a target/release/kubeadp-agent /payload/usr/local/bin/kubeadp-agent \
    && cp -a target/release/agent-run /payload/usr/local/bin/agent-run \
    && test -x /payload/usr/local/bin/kubeadp-agent \
    && test -x /payload/usr/local/bin/agent-run \
    && rm -rf /build/kubeadp-agent/target

# Stage 3: Build & install Node.js v26 and the npm-based claude-code CLI,
# staged as a /payload root like docker/ai-server/Dockerfile's tools-builder
# stage so the final stage picks up claude + node + the npm tree in one
# root-merge COPY. (The tools image docker/modules also ships node+npm, but this
# image keeps the same baked claude-code install the orchestrator image had, so
# the orchestrator pod gets the identical, pinned claude.)
FROM node:26.11.1-bookworm-slim AS tools-builder
# apt + claude + payload staging all in ONE layer (vfs runners: an extra RUN
# layer is a full on-disk re-copy of this stage rootfs).
RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      curl \
      ca-certificates \
      && rm -rf /var/lib/apt/lists/* \
      && npm install -g --fetch-retries=5 --fetch-retry-mintimeout=5000 @anthropic-ai/claude-code \
      && npm cache clean --force \
      && claude --version \
      && mkdir -p /payload/usr/local/bin /payload/usr/local/libexec /payload/usr/local/lib \
      && cp -a /usr/local/bin/node /payload/usr/local/bin/ \
      && cp -a /usr/local/bin/claude /payload/usr/local/bin/ \
      && ln -sf /usr/local/lib/node_modules/npm/bin/npm-cli.js /payload/usr/local/bin/npm \
      && ln -sf /usr/local/lib/node_modules/npm/bin/npx-cli.js /payload/usr/local/bin/npx \
      && cp -a /usr/local/lib/node_modules /payload/usr/local/lib/ \
      && test -x /payload/usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe \
      && test -x /payload/usr/local/bin/node \
      && test -x /payload/usr/local/bin/claude

# Stage 3b: fetch the opencode CLI from GitHub Releases (KA-16). The engine
# binary of the opencode harness is downloaded and verified here on a glibc
# base exactly like the (now removed) agents/opencode downloader stage did, and
# carried into the final runtime stage at /usr/local/bin/opencode. Once this
# image replaces the old opencode init container image, the worker's opencode
# engine needs no separate /tools injection. OPENCODE_VERSION is the single
# source of truth in agents/Makefile; this Dockerfile's ARG default must be
# kept in sync with it (the workflow passes it explicitly on every build).
FROM debian:bookworm-slim AS opencode-downloader
ARG OPENCODE_VERSION=1.19.4
ARG TARGETARCH
RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && set -ex; \
    case "${TARGETARCH}" in \
        amd64) ARCH="x64" ;; \
        arm64) ARCH="arm64" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac; \
    FILENAME="opencode-linux-${ARCH}.tar.gz"; \
    URL="https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/${FILENAME}"; \
    echo "Downloading OpenCode v${OPENCODE_VERSION} from ${URL}..."; \
    curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors "${URL}" -o /tmp/opencode.tar.gz; \
    tar -xzf /tmp/opencode.tar.gz -C /tmp; \
    mkdir -p /payload/usr/local/bin; \
    mv /tmp/opencode /payload/usr/local/bin/opencode; \
    chmod 755 /payload/usr/local/bin/opencode; \
    rm -f /tmp/opencode.tar.gz

# Stage 4: Final minimal runtime on debian:bookworm-slim (see base-image
# reconciliation at the top). As superuser for the apt installs and the
# user/uid layout, then drops to uid 1000 like both old images.
FROM debian:bookworm-slim AS runtime

# Reconciles BOTH old images' runtime packages:
#   docker/ai-server/:    git curl ca-certificates procps rsync gcc libc6-dev   (+ ttyd, node/claude staged below)
#   docker/agent-run-api/: git ca-certificates curl procps gcc libc6-dev make pkg-config
# Union = git curl ca-certificates procps rsync gcc libc6-dev make pkg-config.
# gcc + libc6-dev are the linker/C toolchain rustc, cgo and Python C extensions
# need (both old images kept them for the same reason). make + pkg-config are
# worker-only but harmless and kept in the union so the identical image serves
# both roles. rsync is orchestrator-only (repo-sync workflow over the mounted
# SSH key) and kept for the same reason. python3/jq come from the tools image.
# Orchestrator parity (HL-70 rehearsal): openssh-client (node SSH through the OpenBao CA), patch, zsh (the session
# shell), less, psmisc, wget, nano, vim-tiny, htop, git-lfs and bash-completion were in the old ai-server image.
# No sudo/acl/libcap2-bin (no privilege separation in the agent stack).
RUN apt_update() { for i in 1 2 3 4 5 6; do apt-get -o Acquire::Retries=5 update && [ -n "$(ls /var/lib/apt/lists/*_Packages* /var/lib/apt/lists/*_Packages.* 2>/dev/null)" ] && return 0; echo "apt update retry $i" >&2; sleep 10; done; return 1; }; \
    apt_update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    procps \
    rsync \
    gcc \
    libc6-dev \
    make \
    pkg-config \
    openssh-client \
    patch \
    zsh \
    less \
    psmisc \
    wget \
    nano \
    vim-tiny \
    htop \
    git-lfs \
    bash-completion \
    && rm -rf /var/lib/apt/lists/*

# The uid/gid layout. Both old images ran the container as uid 1000:
#   ai-server: base image's `coder` user (uid 1000, group coder)
#   agent-run-api: `agent` (uid 1000, gid 1000, created by useradd -m)
# This image uses one user of its own, `agent` (uid 1000, gid 1000), so the
# pod-facing runAsUser 1000/runAsGroup 1000/fsGroup 1000 of both manifests is
# reproduced. The primary group is 1000, matching the old agent user; the
# orchestrator pod's fsGroup 1000 mounts make the group bit not matter.
# The shell is /bin/bash so the interactive ttyd OAuth shell in the orchestrator
# pod behaves like today's code-server shell.
RUN groupadd -g 1000 agent && useradd -m -u 1000 -g 1000 -s /bin/bash agent

# agy (antigravity CLI), installed at the core's default agy_path
# /usr/local/libexec/agy exactly as docker/agent-run-api/Dockerfile does (this
# image has no agy shim; both harness engines call the binary directly).
# Bundled in the agent-run-api consumer Dockerfile itself (bookworm-slim and its
# glibc), so debian:bookworm-slim is the right place for it here too.
RUN mkdir -p /usr/local/libexec \
    && curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors https://antigravity.google/cli/install.sh | bash -s -- -d /usr/local/libexec \
    && chmod 755 /usr/local/libexec/agy

# ttyd (web terminal), pinned 1.7.7 with its release checksum, exactly as
# docker/ai-server/Dockerfile does -- the orchestrator pod's interactive OAuth
# shell runs from this image (sidecar).
RUN curl -fsSL -o /usr/local/bin/ttyd https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.x86_64 \
    && echo "8a217c968aba172e0dbf3f34447218dc015bc4d5e59bf51db2f2cd12b7be4f55  /usr/local/bin/ttyd" | sha256sum -c - \
    && chmod 755 /usr/local/bin/ttyd

# HOME layout: /home/agent is this image's home (the worker's /home/agent). The
# orchestrator pod mounts its own persistent home/workspace PVCs over it, so
# this default only matters for pod-less local runs. Working dir like the old
# worker image.
ENV HOME=/home/agent
WORKDIR /home/agent

# Build/cache locations (persistent workspace volume) and the tools-volume PATH
# -- copied from docker/agent-run-api/Dockerfile so the worker role looks at the
# same cache dirs it used when it was its own image. The /opt/tools entries are
# harmless when the volume is not mounted. GOPATH/CARGO_HOME for a
# pod-less/lab role would move to the PVC-mounted cache dirs of the new
# orchestrator deployment; the worker keeps using /workspace/.cache as today.
ENV CARGO_HOME=/workspace/.cache/cargo
ENV GOPATH=/workspace/.cache/go
ENV GOCACHE=/workspace/.cache/go-build
ENV PIP_CACHE_DIR=/workspace/.cache/pip
ENV GOTOOLCHAIN=local
ENV PATH="/opt/tools/bin:/opt/tools/go/bin:/opt/tools/rust/bin:/opt/tools/python/bin:/opt/tools/node/bin:${CARGO_HOME}/bin:${PATH}"

# The binaries of the one agent package (one root-merge COPY, stripped).
COPY --from=agent-builder /payload/. /

# tools-builder payload: node runtime + npm tree + claude-code (orchestrator
# engine) -- one root-merge COPY. Self-contained native binary, staged the same
# way as in docker/ai-server/.
COPY --from=tools-builder /payload/. /

# opencode-downloader payload: the opencode CLI (worker engine) at
# /usr/local/bin/opencode, staged the same way as agents/opencode did.
COPY --from=opencode-downloader /payload/. /

# tini, the pod's PID 1.
COPY --from=cli-tools /payload/. /

# No systemd, no code-server: the image's ENTRYPOINT is tini in front of the
# role binary, and the pod manifest provides the actual command/args (role env
# KUBEADP_ROLE and the per-role listeners/sidecars). `tini` without -g, matching
# the signal contract both old pods documented.
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/kubeadp-agent"]

# Documentation only (no runtime effect): both the worker and orchestrator
# roles listen on 0.0.0.0:8002 by default (DEFAULT_LISTEN in worker and
# orchestrate).
EXPOSE 8002

USER agent
