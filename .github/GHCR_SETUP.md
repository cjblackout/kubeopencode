# GHCR (GitHub Container Registry) Setup for GitHub Actions

## Overview

KubeOpenCode uses GitHub Container Registry (ghcr.io) for all container images and Helm charts.
GitHub Actions authenticates automatically using `GITHUB_TOKEN` — no external secrets needed.

## Repositories

All images are published under the `ghcr.io/kubeopencode/` namespace:

| Image | Description |
|-------|-------------|
| `ghcr.io/kubeopencode/kubeopencode` | Unified binary: controller, git-init, context-init |
| `ghcr.io/cjblackout/kubeopencode/kubeadp-agent` | Agent server image: kubeadp-agent worker + bundled opencode CLI |
| `ghcr.io/kubeopencode/kubeopencode-agent-devbox` | Universal development environment |
| `ghcr.io/kubeopencode/helm-charts/kubeopencode` | Helm chart (OCI) |

## GitHub Actions Authentication

CI workflows use the built-in `GITHUB_TOKEN` — no manual secret setup required:

```yaml
- name: Login to GHCR
  uses: docker/login-action@v4
  with:
    registry: ghcr.io
    username: ${{ github.actor }}
    password: ${{ secrets.GITHUB_TOKEN }}
```

Jobs that push images need these permissions:

```yaml
permissions:
  contents: read
  packages: write
```

## Ensure Packages Are Public

After the first push, go to the GitHub org → **Packages** and set each package's visibility to **Public**.
This allows anonymous pulls without authentication (important for Kubernetes clusters).

## Helm OCI Registry

```bash
# Login (for manual push)
echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u USERNAME --password-stdin

# Pull chart
helm pull oci://ghcr.io/kubeopencode/helm-charts/kubeopencode --version <version>

# Install from OCI
helm install kubeopencode oci://ghcr.io/kubeopencode/helm-charts/kubeopencode \
  --namespace kubeopencode-system --create-namespace
```
