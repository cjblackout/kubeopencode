# Enterprise (Proxy, CA, Registry)

## Custom CA Certificates

When accessing private Git servers or internal HTTPS services that use self-signed or private CA certificates, configure `caBundle` on the Agent to mount custom CA certificates into all containers.

### ConfigMap Example (trust-manager Compatible)

If you use [cert-manager trust-manager](https://cert-manager.io/docs/trust/trust-manager/), it can automatically populate a ConfigMap with your organization's CA bundle. kubeadp's default key (`ca-bundle.crt`) matches trust-manager's convention.

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: internal-agent
spec:
  profile: "Agent with custom CA for internal services"
  executorImage: ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  caBundle:
    configMapRef:
      name: custom-ca-bundle       # ConfigMap containing the CA certificate
      key: ca-bundle.crt           # Optional, defaults to "ca-bundle.crt"
```

### Secret Example

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: internal-agent
spec:
  profile: "Agent with custom CA from Secret"
  executorImage: ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  caBundle:
    secretRef:
      name: custom-ca-secret       # Secret containing the CA certificate
      key: ca.crt                  # Optional, defaults to "ca.crt"
```

### How It Works

- The CA certificate is mounted at `/etc/ssl/certs/custom-ca/tls.crt` in **all** containers (init containers and the worker container)
- The `CUSTOM_CA_CERT_PATH` environment variable is set in all containers
- **git-init**: Concatenates the custom CA with system CAs and sets `GIT_SSL_CAINFO` so `git clone` trusts the private server
- **url-fetch**: Appends the custom CA to Go's x509 system certificate pool for HTTPS URL fetching

This is the recommended approach for private HTTPS servers. Avoid disabling TLS verification (`InsecureSkipTLSVerify`) in favor of proper CA bundle configuration.

### Multiple CA Certificates

The `caBundle` field accepts a single ConfigMap or Secret reference, but PEM format supports multiple certificates in one file. To trust multiple CAs, concatenate them into a single bundle:

```bash
# Combine multiple CA certificates into one PEM bundle
cat internal-ca.crt partner-ca.crt > combined-ca-bundle.crt
kubectl create configmap custom-ca-bundle --from-file=ca-bundle.crt=combined-ca-bundle.crt
```

If you use [cert-manager trust-manager](https://cert-manager.io/docs/trust/trust-manager/), it handles multi-source aggregation automatically:

```yaml
apiVersion: trust.cert-manager.io/v1alpha1
kind: Bundle
metadata:
  name: custom-ca-bundle
spec:
  sources:
    - useDefaultCAs: true          # Include public CAs
    - secret:
        name: internal-ca
        key: ca.crt
    - configMap:
        name: partner-ca
        key: ca-bundle.crt
  target:
    configMap:
      key: ca-bundle.crt           # Matches kubeadp's default key
```

> **Note**: `git-init` automatically concatenates the custom CA bundle with the container's system CAs, so public HTTPS (e.g., github.com) continues working even when `caBundle` is configured. You do not need to include public CAs in your bundle unless you want to explicitly control the full trust chain.

## HTTP/HTTPS Proxy Configuration

Enterprise networks often require all outbound traffic to pass through a corporate proxy server. kubeadp supports proxy configuration at both the Agent level and the cluster level via `KubeOpenCodeConfig`.

### Agent-Level Proxy

Configure proxy settings directly on an Agent. These settings are injected as environment variables into all init containers and the worker container.

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: enterprise-agent
spec:
  profile: "Agent with corporate proxy configuration"
  executorImage: ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  proxy:
    httpProxy: "http://proxy.corp.example.com:8080"
    httpsProxy: "http://proxy.corp.example.com:8080"
    noProxy: "localhost,127.0.0.1,10.0.0.0/8,.corp.example.com"
```

### Cluster-Level Proxy

For organizations where all agents should use the same proxy, configure it once in `KubeOpenCodeConfig`:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: KubeOpenCodeConfig
metadata:
  name: cluster
spec:
  proxy:
    httpProxy: "http://proxy.corp.example.com:8080"
    httpsProxy: "http://proxy.corp.example.com:8080"
    noProxy: "localhost,127.0.0.1,10.0.0.0/8,.corp.example.com"
```

### How Proxy Works

- Both uppercase and lowercase environment variables are set: `HTTP_PROXY`/`http_proxy`, `HTTPS_PROXY`/`https_proxy`, `NO_PROXY`/`no_proxy`
- The `.svc` and `.cluster.local` suffixes are always appended automatically to `noProxy` to prevent proxying in-cluster traffic
- **Agent-level proxy overrides cluster-level proxy**: If an Agent has `proxy` configured, it takes precedence over the `KubeOpenCodeConfig` proxy settings
- Proxy environment variables are injected into all containers (init containers and the worker container)

## Private Registry Authentication

When using container images from private registries (e.g., Harbor, AWS ECR, GCR), configure `imagePullSecrets` on the Agent to provide registry authentication credentials.

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: private-registry-agent
spec:
  profile: "Agent using images from private registries"
  executorImage: registry.corp.example.com/kubeopencode/agent-devbox:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  imagePullSecrets:
    - name: harbor-registry-secret
    - name: gcr-secret
```

### Prerequisites

1. The referenced Secrets must exist in the **same namespace** as the Agent
2. Secrets must be of type `kubernetes.io/dockerconfigjson`

Create registry credentials:

```bash
kubectl create secret docker-registry harbor-registry-secret \
  --docker-server=registry.corp.example.com \
  --docker-username=myuser \
  --docker-password=mypassword \
  -n kubeopencode-system
```

The `imagePullSecrets` are added to the Pod spec of all generated Pods, enabling Kubernetes to authenticate when pulling `executorImage` or `kubeadpImage` from private registries.
