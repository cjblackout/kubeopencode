# Docker in Docker (DinD)

AI agents often need to build container images or run containers as part of their workflow — for example, running integration tests with database containers, building and pushing images in CI/CD pipelines, or testing Dockerfiles.

The default devbox executor image already includes the **Docker CLI** (`docker-ce-cli`). To enable full Docker functionality, you need a Docker daemon (`dockerd`) running inside the agent Pod. This guide covers three approaches, each with different security and complexity trade-offs.

## How It Works

kubeadp uses a two-container pattern: an init container copies the OpenCode binary to `/tools`, and the worker container runs it. The controller sets the container's `Command` explicitly (e.g., `sh -c "/tools/opencode serve ..."`), which **overrides any Dockerfile `ENTRYPOINT`**.

This means you cannot simply set `ENTRYPOINT` to a dockerd startup script. Instead, the recommended approach is a **lazy-init wrapper** — a script that replaces the `docker` binary and transparently starts `dockerd` on first use. This works with both Agent Deployments and Task Pods without any controller code changes.

## Comparison

| | Sysbox Runtime | Privileged Mode | Rootless Docker |
|---|---|---|---|
| **Security** | Strong (user namespace isolation) | Weak (root on node) | Moderate |
| **Complexity** | Medium (requires Sysbox on nodes) | Low (config only) | High (many dependencies) |
| **Capabilities needed** | None | All (privileged) | SYS_ADMIN, NET_ADMIN |
| **OpenShift compatible** | Yes (with Sysbox) | No | Partial |
| **Performance overhead** | ~5-10% | None | ~10-15% |
| **Recommendation** | Production | Dev/test only | Fallback |

## Custom Executor Image

All three approaches share a common custom executor image pattern. Build a `devbox-dind` image that extends devbox with Docker daemon and a lazy-init wrapper:

```dockerfile
FROM ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest

USER root

# Install Docker daemon and containerd
# (docker-ce-cli is already installed in devbox)
RUN apt-get update && apt-get install -y --no-install-recommends \
    docker-ce \
    containerd.io \
    && rm -rf /var/lib/apt/lists/*

# Install lazy-init docker wrapper
# This transparently starts dockerd on first `docker` command
RUN mv /usr/bin/docker /usr/bin/docker.real
COPY --chmod=755 docker-lazy-init.sh /usr/bin/docker

# Note: USER is intentionally set to root (0) for DinD.
# For Sysbox: inner root is mapped to unprivileged host user via user namespaces.
# For Privileged mode: root is required to start dockerd.
# This differs from the base devbox image which uses USER 1000:0.
USER 0:0

WORKDIR /workspace
CMD ["/bin/zsh"]
```

Create `docker-lazy-init.sh`:

```bash
#!/bin/bash
# Lazy-init wrapper for Docker CLI.
# Starts dockerd automatically on first docker command.
# Works with kubeadp's command override pattern since it wraps
# the docker binary itself, not the container entrypoint.

DOCKER_REAL="/usr/bin/docker.real"
DOCKERD_LOG="/tmp/dockerd.log"
DOCKERD_PIDFILE="/tmp/dockerd.pid"

# Check if dockerd is already running
if ! ${DOCKER_REAL} info >/dev/null 2>&1; then
    # Acquire lock to prevent concurrent dockerd starts
    exec 200>/tmp/dockerd.lock
    flock -n 200 || {
        # Another process is starting dockerd, wait for it
        flock 200
        exec ${DOCKER_REAL} "$@"
    }

    # Double-check after acquiring lock
    if ! ${DOCKER_REAL} info >/dev/null 2>&1; then
        # Mount tmpfs at /var/lib/docker to avoid overlay-on-overlay issues.
        # Container filesystems use overlayfs (from containerd/CRI), and nested
        # overlayfs is not supported on most kernels. tmpfs provides a clean
        # non-overlay mount point where Docker can use its default storage driver.
        # Note: This means Docker images/containers are stored in memory and
        # lost on pod restart. For persistence, use a PVC mounted at /var/lib/docker.
        if ! mountpoint -q /var/lib/docker 2>/dev/null; then
            mkdir -p /var/lib/docker
            mount -t tmpfs tmpfs /var/lib/docker
        fi

        echo "Starting Docker daemon..." >&2
        dockerd &>"${DOCKERD_LOG}" &
        echo $! > "${DOCKERD_PIDFILE}"

        # Wait for Docker daemon to be ready
        timeout=30
        while ! ${DOCKER_REAL} info >/dev/null 2>&1; do
            timeout=$((timeout - 1))
            if [ $timeout -le 0 ]; then
                echo "ERROR: Docker daemon failed to start. Check ${DOCKERD_LOG}" >&2
                exit 1
            fi
            sleep 1
        done
        echo "Docker daemon ready" >&2
    fi
fi

exec ${DOCKER_REAL} "$@"
```

Build the image:

```bash
docker build -t your-registry/devbox-dind:latest .
docker push your-registry/devbox-dind:latest
```

## Option 1: Sysbox Runtime (Recommended)

[Sysbox](https://github.com/nestybox/sysbox) is an OCI runtime that enables containers to run system-level workloads (like Docker) securely, without requiring privileged mode. It uses Linux user namespaces: the container runs as root internally, but maps to an unprivileged user on the host.

### Prerequisites

**1. Label nodes for Sysbox installation:**

```bash
kubectl label nodes <node-name> sysbox-install=yes
```

**2. Deploy Sysbox on cluster nodes:**

```bash
kubectl apply -f https://raw.githubusercontent.com/nestybox/sysbox/master/sysbox-k8s-manifests/sysbox-install.yaml
```

This deploys a DaemonSet that installs the `sysbox-runc` runtime on labeled nodes. It also creates the `sysbox-runc` RuntimeClass automatically. See [sysbox-deploy-k8s](https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install-k8s.md) for details.

**3. Verify Sysbox is running:**

```bash
# Check the DaemonSet
kubectl get daemonset sysbox-deploy-k8s -n kube-system

# Verify RuntimeClass exists
kubectl get runtimeclass sysbox-runc
```

> **Note**: Sysbox requires upstream Kubernetes (GKE, EKS, AKS, kubeadm). K3s/K0s support is experimental. See [sysbox compatibility](https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install-k8s.md) for details.

### Agent Configuration

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: dind-agent
spec:
  profile: "Agent with Docker-in-Docker support via Sysbox"
  executorImage: your-registry/devbox-dind:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  podSpec:
    runtimeClassName: sysbox-runc
    resources:
      requests:
        memory: "1Gi"
      limits:
        memory: "8Gi"
```

> No special `securityContext` is needed. Sysbox handles isolation at the runtime level. The default restricted security context is compatible with Sysbox — the runtime transparently enables the required kernel features.

### Verification

Create a Task to verify DinD is working:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Task
metadata:
  name: test-dind
spec:
  agentRef:
    name: dind-agent
  prompt: |
    Run the following commands and report the output:
    1. docker info
    2. docker run --rm hello-world
    3. echo 'FROM alpine:latest' | docker build -t test-image -
```

## Option 2: Privileged Mode (Dev/Test Only)

> **Security Warning**: Privileged containers have full access to the host kernel. A container escape grants root access to the node. **Never use this in production or multi-tenant clusters.**

This is the simplest approach — run `dockerd` inside a privileged container with no runtime isolation.

### Agent Configuration

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: dind-privileged-agent
spec:
  profile: "Agent with privileged DinD (dev/test only)"
  executorImage: your-registry/devbox-dind:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  podSpec:
    securityContext:
      privileged: true
    podSecurityContext:
      runAsUser: 0
      runAsGroup: 0
    resources:
      requests:
        memory: "1Gi"
      limits:
        memory: "8Gi"
```

### Namespace Requirements

The namespace must allow privileged Pods. If Pod Security Admission is enabled:

```bash
kubectl label namespace <namespace> \
    pod-security.kubernetes.io/enforce=privileged \
    pod-security.kubernetes.io/warn=privileged
```

## Option 3: Rootless Docker

Rootless Docker runs the Docker daemon without host-level root privileges, using user namespaces and `fuse-overlayfs` for storage. This is a middle ground between Sysbox and privileged mode.

### Custom Executor Image (Rootless Variant)

This variant extends the base `devbox-dind` image with rootless Docker dependencies:

```dockerfile
FROM ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest

USER root

# Install rootless Docker dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    docker-ce \
    docker-ce-rootless-extras \
    containerd.io \
    fuse-overlayfs \
    slirp4netns \
    uidmap \
    && rm -rf /var/lib/apt/lists/*

# Install lazy-init wrapper for rootless Docker
RUN mv /usr/bin/docker /usr/bin/docker.real
COPY --chmod=755 docker-lazy-init-rootless.sh /usr/bin/docker

# Rootless Docker runs as non-root
USER 1000:0

ENV DOCKER_HOST=unix:///tmp/.docker/run/docker.sock
ENV XDG_RUNTIME_DIR=/tmp/.docker/run

WORKDIR /workspace
CMD ["/bin/zsh"]
```

The rootless lazy-init wrapper (`docker-lazy-init-rootless.sh`) is similar but starts `dockerd-rootless.sh` instead of `dockerd`.

### Agent Configuration

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: dind-rootless-agent
spec:
  profile: "Agent with rootless DinD"
  executorImage: your-registry/devbox-dind-rootless:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  podSpec:
    securityContext:
      capabilities:
        add:
          - SYS_ADMIN
          - NET_ADMIN
        drop:
          - ALL
      seccompProfile:
        type: Unconfined
    resources:
      requests:
        memory: "1Gi"
      limits:
        memory: "8Gi"
```

> **Note**: Rootless Docker requires `SYS_ADMIN` for user namespace setup and `NET_ADMIN` for networking. The namespace must allow at least the `baseline` Pod Security Standard.

## Tips

### Overlay-on-Overlay Issue

Container runtimes (containerd, CRI-O) use overlayfs for the container's root filesystem. Docker inside the container also defaults to overlayfs as its storage driver. Nesting overlayfs on top of overlayfs is not supported on most kernels and causes `mount: invalid argument` errors when running containers.

The lazy-init wrapper handles this by mounting a **tmpfs** at `/var/lib/docker` before starting `dockerd`. This gives Docker a non-overlay filesystem to work with. The trade-off is that Docker images and containers are stored in memory and lost on pod restart.

For persistent Docker storage, mount a **PVC** at `/var/lib/docker` instead. PVC-backed volumes use ext4/xfs (not overlayfs), so Docker's overlay storage driver works correctly:

```bash
# In docker-lazy-init.sh, replace the tmpfs mount with:
# (Only mount tmpfs if /var/lib/docker is not already a mount point, e.g., from a PVC)
if ! mountpoint -q /var/lib/docker 2>/dev/null; then
    mkdir -p /var/lib/docker
    mount -t tmpfs tmpfs /var/lib/docker
fi
```

> **Note**: Sysbox does not have this issue — it provides a proper filesystem layer for the inner container.

### Docker Build Cache Persistence

When using DinD with [persistence](persistence.md), Docker's build cache and image layers are stored in tmpfs by default and lost on restart. To persist them, use `--data-root` to point Docker's data directory to a PVC-backed path:

```bash
# Configure in dockerd startup (modify docker-lazy-init.sh)
# Requires a PVC mounted at /workspace
dockerd --data-root /workspace/.docker-data &>"${DOCKERD_LOG}" &
```

> **Note**: When using `--data-root` on a PVC-backed path, you can skip the tmpfs mount for `/var/lib/docker` since Docker won't use it.

### Registry Mirror

If your cluster uses a private registry mirror, configure the Docker daemon:

```bash
# Add to docker-lazy-init.sh before starting dockerd
mkdir -p /etc/docker
cat > /etc/docker/daemon.json <<DEOF
{
  "registry-mirrors": ["https://mirror.example.com"]
}
DEOF
```

### Network Proxy

Docker daemon inherits proxy settings from the environment. You can also configure them via [proxy settings](enterprise.md) in the Agent spec, which are automatically injected as environment variables into the container. For Docker-specific proxy configuration, add to `/etc/docker/daemon.json`.

### Alternative: Image-Build-Only (No Daemon)

If you only need to **build** container images (not run them), consider [Kaniko](https://github.com/GoogleContainerTools/kaniko) or [Buildah](https://github.com/containers/buildah). These tools run entirely in user space with no special privileges and work with the default restricted security context. However, they cannot `docker run` containers.
