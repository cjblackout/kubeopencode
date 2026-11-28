# Agent Plugins

OpenCode has a rich plugin ecosystem with 60+ community plugins. kubeadp's `spec.plugins` field makes it easy to declaratively load plugins for your Agents — no manual JSON editing required. The controller creates a **plugin-init** init container that runs `npm install` to download plugins into a shared volume; the executor container loads them from `/plugins/node_modules/` without needing npm.

This guide walks through two real-world plugins: a **server plugin** for security guardrails and a **TUI plugin** for interactive session enhancements.

## Overview

| Plugin | npm Package | Target | What It Does |
|--------|-------------|--------|--------------|
| Safety Net | `cc-safety-net` | server | Intercepts dangerous git/filesystem commands before execution |
| Gentleman | `plugin-gentleman` | tui | Animated ASCII mascot during busy states for interactive sessions |

## Server Plugin: Safety Net

[cc-safety-net](https://github.com/kenryu42/claude-code-safety-net) intercepts destructive commands via the `tool.execute.before` hook. It blocks operations like:

- `git reset --hard`, `git push --force`
- `rm -rf /`, `rm -rf ~`
- `git checkout -- .` (discard all changes)
- `git clean -fdx` (remove untracked files)

It understands shell wrappers (`bash -c '...'`), sudo, piped commands, and more.

### Agent Configuration

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: safe-agent
spec:
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent

  plugins:
    - name: "cc-safety-net"

  config:
    model: anthropic/claude-sonnet-4-20250514
```

The controller installs the plugin via `npm install` in the plugin-init container and merges the plugin path into the generated config:

```yaml
model: anthropic/claude-sonnet-4-20250514
plugin:
  - "file:///plugins/node_modules/cc-safety-net"
```

Any task that tries to execute a destructive command will be blocked.

### Strict Mode

For higher security, use environment variables via `spec.credentials`:

```yaml
spec:
  plugins:
    - name: "cc-safety-net"
  credentials:
    - name: safety-net-strict
      secretRef:
        name: safety-net-config
        key: strict
      env: "SAFETY_NET_STRICT"
```

| Variable | Effect |
|----------|--------|
| `SAFETY_NET_STRICT=1` | Fail-closed on unparseable commands |
| `SAFETY_NET_PARANOID=1` | Enable all paranoid checks |
| `SAFETY_NET_PARANOID_RM=1` | Block non-temp `rm -rf` even within cwd |

## TUI Plugin: Gentleman

[plugin-gentleman](https://github.com/IrrealV/plugin-gentleman) is a TUI plugin that provides an animated ASCII mascot during busy states for interactive sessions.

TUI plugins are loaded when users interact with the Agent via an interactive session (`kubeoc agent attach`, which opens a shell in the agent's server pod, or the web terminal) — they have no effect on headless worker operation.

### Agent Configuration

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: interactive-agent
spec:
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent

  plugins:
    - name: "plugin-gentleman"
      target: tui

  config:
    model: anthropic/claude-sonnet-4-20250514
```

The controller installs the plugin via npm and generates a separate TUI config:

```yaml
plugin:
  - "file:///plugins/node_modules/plugin-gentleman"
```

And sets `OPENCODE_TUI_CONFIG=/tools/tui.json` on the Agent container.

## Combining Server and TUI Plugins

The real power is combining both targets on a single Agent:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: production-agent
spec:
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent

  plugins:
    # Server plugins — always running in opencode serve
    - name: "cc-safety-net"
    - name: "@nicholasgriffintn/opencode-plugin-otel"
      options:
        endpoint: "http://otel-collector:4318"

    # TUI plugins — loaded during interactive attach sessions
    - name: "plugin-gentleman"
      target: tui

  config:
    model: anthropic/claude-sonnet-4-20250514
```

This produces:

- Server config with `cc-safety-net` and OTel plugin (`file://` paths)
- TUI config with `plugin-gentleman` (`file://` path)
- `OPENCODE_CONFIG` and `OPENCODE_TUI_CONFIG` environment variables set automatically

## Plugins in Templates

Define standard plugins in an AgentTemplate so all derived Agents inherit them:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: AgentTemplate
metadata:
  name: secure-base
spec:
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  plugins:
    - name: "cc-safety-net"
---
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: team-agent
spec:
  templateRef:
    name: secure-base
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
  # Inherits cc-safety-net from template
  # Can add more plugins (but replaces template list if specified)
```

## Verifying Plugins Work

### Verifying Server Plugins

To verify a server plugin like `cc-safety-net` is loaded and working, create a Task that asks the agent to execute a command the plugin should block:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Task
metadata:
  name: test-safety-net
spec:
  agentRef:
    name: safe-agent
  prompt: "Run git reset --hard in the workspace"
```

If `cc-safety-net` is working, the agent will report the command was blocked:

```
BLOCKED by Safety Net
Reason: git reset --hard destroys all uncommitted changes permanently.
        Use 'git stash' first.
Command: git reset --hard
If this operation is truly needed, ask the user for explicit permission
and have them run the command manually.
```

You can also verify the plugin configuration is correctly injected by inspecting the pod:

```bash
# Check the plugin-init container installed packages successfully
kubectl logs -n <namespace> <pod-name> -c plugin-init

# Verify the config contains the plugin path
kubectl exec -n <namespace> <pod-name> -c kubeadp-agent -- cat /tools/opencode.json
# Should contain the plugin path: file:///plugins/node_modules/cc-safety-net

# Verify the package exists in the shared volume
kubectl exec -n <namespace> <pod-name> -c kubeadp-agent -- ls /plugins/node_modules/cc-safety-net/
```

### Verifying TUI Plugins

TUI plugins are active during interactive sessions. Attach to the agent and observe the plugin's effect:

```bash
kubeoc agent attach <agent-name> -n <namespace>
```

For `plugin-gentleman`, you should see an animated ASCII mascot during busy states. You can also verify the TUI config:

```bash
kubectl exec -n <namespace> <pod-name> -c kubeadp-agent -- cat /tools/tui.json
# Should contain the plugin path: file:///plugins/node_modules/plugin-gentleman
```

## Version Pinning

Pin specific versions for reproducible builds:

```yaml
plugins:
  - name: "cc-safety-net@0.8.2"
  - name: "@aexol/opencode-tui@^1.0.0"
```

## Popular Server Plugins

| npm Package | Description |
|-------------|-------------|
| `cc-safety-net` | Block destructive git/filesystem commands |
| `@nicholasgriffintn/opencode-plugin-otel` | OpenTelemetry telemetry (traces, metrics) |
| `@anthropics/agent-memory` | Persistent memory via local vector database |
| `opencode-dcp` | Dynamic context pruning to save tokens |

## Popular TUI Plugins

| npm Package | Description |
|-------------|-------------|
| `plugin-gentleman` | Animated ASCII mascot during busy states |
