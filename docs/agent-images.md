# Agent Images

kubeadp provides a **server image** (`kubeadp-agent`) that runs the Agent's worker process and bundles the `opencode` engine, plus **template executor images** (`devbox`, `echo`) that serve as starting points for building your own customized agents.

## Container Pattern

An Agent Deployment uses:

1. **Server Container** (`kubeadpImage` by default, or `executorImage` when explicitly set): Runs `kubeadp-agent worker` (persistent multi-session worker). By default this is the `kubeadp-agent` image itself, which already contains `kubeadp-agent`, `agent-run` **and** the bundled `opencode` CLI under `/usr/local/bin` — no init container is needed.
2. **Kubeadp Init Container** (`kubeadpImage`, only when an explicit `executorImage` is set): Copies `kubeadp-agent`, `agent-run` and `opencode` to the shared `/tools` volume so a custom executor image without those binaries can still run the worker.
3. **OpenCode Init Container** (`agentImage`): **Deprecated and ignored.** Kept only for CRD compatibility; the opencode CLI is bundled into the `kubeadp-agent` image instead.
4. **Attach Container** (`attachImage`): **Deprecated and ignored.** Task Pods that reference an Agent run `kubeopencode task-submit` from the system image.

## Available Images

| Image | Type | Description |
|-------|------|-------------|
| `kubeadp-agent` | Server/Worker | Runs `kubeadp-agent worker`; bundles `kubeadp-agent`, `agent-run`, `opencode` (see `docker/kubeadp-agent/`) |
| `devbox` | Executor | Universal development environment with Go, Node, Python, kubectl, helm |
| `echo` | Testing | Minimal Alpine image for E2E testing |

## Image Resolution

When configuring an Agent, the controller resolves the server container as follows:

| Configuration | Server Container | Init Container |
|--------------|------------------|------------------|
| Only `kubeadpImage` set (default) | `kubeadpImage` (runs `kubeadp-agent worker`) | None (binaries come from the image) |
| Explicit `executorImage` set | `executorImage` (runs `kubeadp-agent worker`) | `kubeadpImage` (copies binaries to `/tools`) |
| Neither set | Default kubeadp-agent image | None |

`agentImage` and `attachImage` are **ignored**.

### Default Images

- Server: `ghcr.io/cjblackout/kubeopencode/kubeadp-agent:2026.11.64`
- Devbox executor: `ghcr.io/cjblackout/kubeopencode-agent-devbox:latest`

## Building Agent Images

### Server Image (kubeadp-agent)

The `kubeadp-agent` image is built from the repo root (build context must include `src/kubeadp-agent`):

```bash
docker build -f docker/kubeadp-agent/Dockerfile --build-arg BUILD_SHA=$(git rev-parse HEAD) -t kubeadp-agent .
```

The opencode CLI's version is pinned by `OPENCODE_VERSION` in `agents/Makefile` (single source of truth) and passed as a build arg by `.github/workflows/build-kubeadp-agent.yml`.

### Executor Images (Local Development / Kind Clusters)

```bash
# Build executor containers (devbox, echo)
make agent-build AGENT=devbox

# Customize registry and version
make agent-build AGENT=devbox IMG_REGISTRY=docker.io IMG_ORG=myorg VERSION=v1.0.0
```

### Remote/Production Clusters

For remote clusters (OpenShift, EKS, GKE, etc.), use multi-arch build and push:

```bash
make agent-buildx AGENT=devbox
```

## Creating Custom Agent Images

For detailed guidance on building custom agent images, see the [Agent Developer Guide](../agents/README.md).

### Example Custom Executor

```dockerfile
FROM ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest

# Add your custom tools
RUN apt-get update && apt-get install -y \
    your-custom-tool \
    another-tool

# Add custom configuration
COPY .bashrc /home/agent/.bashrc
```

## Next Steps

- [Getting Started](getting-started.md) - Installation and basic usage
- [Features](features.md) - Context system, concurrency, and more
- [Security](security.md) - RBAC, credential management, and best practices
- [Agent Developer Guide](../agents/README.md) - Detailed agent development
