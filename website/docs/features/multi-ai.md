# Multi-AI Support

Use different Agents with different executorImages for various use cases:

```yaml
# Standard OpenCode agent with devbox
apiVersion: kubeopencode.io/v1alpha1
kind: Agent
metadata:
  name: opencode-devbox
spec:
  profile: "Standard OpenCode agent with devbox environment"
  executorImage: ghcr.io/kubeopencode/kubeopencode-agent-devbox:latest
  workspaceDir: /workspace
  serviceAccountName: kubeopencode-agent
---
# Task sent to a running Agent
apiVersion: kubeopencode.io/v1alpha1
kind: Task
metadata:
  name: task-with-opencode
spec:
  agentRef:
    name: opencode-devbox
  description: "Update dependencies and create a PR"
```

Tasks can also reference an AgentTemplate for ephemeral execution without a running Agent:

```yaml
apiVersion: kubeopencode.io/v1alpha1
kind: Task
metadata:
  name: ephemeral-task
spec:
  templateRef:
    name: team-config
  description: "Run linting and formatting checks"
```
