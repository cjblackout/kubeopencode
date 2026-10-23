# Roadmap

This file tracks changes planned for this fork (`cjblackout/kubeopencode`), which
is run in the homelab cluster and built by `.github/workflows/build.yaml`.

## 1. Per-task worktrees and per-task memory banks

Status: specified, not started.

### 1.1 Goal

A Task declares a Hindsight bank and a git context. For a Server-mode Task the
system then:

1. creates a git worktree of that repository on the agent's persistent volume,
2. starts the opencode session in that worktree (`opencode run --attach … --dir <worktree>`),
3. makes the session read and write the declared Hindsight bank, and
4. keeps the worktree after the Task finishes, and removes it when the Task
   object is deleted from Kubernetes.

### 1.2 Non-goals

- No change for Pod-mode Tasks (no `agentRef`): they keep their own git-init flow.
- No pushing: the worktree is a scratch workspace; reviewers extract patches.
- No new Kubernetes privileges for agent pods or Task pods. In particular the
  controller does **not** get `pods/exec`, and the agent ServiceAccount stays
  token-less.
- No Hindsight bank management (banks are created by Hindsight on first write;
  keeping the set of banks small is an operations concern, see 1.9).

### 1.3 Design decision

Rejected alternatives:

| Alternative | Why not |
|---|---|
| Controller runs `git` in the server pod through `pods/exec` | Gives the operator an exec privilege on the agent pods. |
| Task pod creates the worktree | The session runs in the *server* pod; the Task pod has no access to the server's volume, and the attach image has neither git nor credentials. |
| opencode's own `POST /experimental/worktree` | Experimental, path and branch naming not controllable, no repository bootstrap, no cleanup tied to a Task. |

Chosen: a small **worktree helper** that runs as a sidecar of the agent server
pod, shares the `/workspace` volume with the opencode server, and exposes a
narrow HTTP API. The controller calls it the same way it already calls the
opencode server (`internal/controller/opencode_client.go`).

### 1.4 API changes

`TaskSpec` (API group `kubeopencode.io/v1alpha1`):

```yaml
spec:
  agentRef: {name: qwen-3-8-27b-nested}
  bankId: nested-operator          # Hindsight bank for this session
  worktree:
    contextName: nested-full       # name of a git context in spec.contexts or the Agent
    ref: main                      # optional, default: the context's ref, else the default branch
    branch: ""                     # optional, default task/<namespace>-<name>
  contexts:
    - name: nested-full
      type: Git
      git: {repository: https://github.com/cjblackout/nested-operator.git, ref: main}
```

Validation (CEL on the CRD, plus controller re-check):

- `bankId`: `^[a-z0-9][a-z0-9-]{0,62}$`; immutable after creation.
- `worktree` requires `bankId` and `agentRef` (Server mode); immutable.
- `worktree.contextName` must resolve to a Git context; `ref` and `branch` match
  `^[A-Za-z0-9._/-]{1,200}$` and may not start with `-` or contain `..`.
- A Task with `bankId` but no `worktree` is allowed (see 1.7, default directory).

`TaskStatus` additions:

```yaml
status:
  worktree:
    phase: Pending | Ready | Failed | Removing
    path: /workspace/worktrees/<task-uid>/<bankId>
    branch: task/<namespace>-<name>
    commit: <sha the worktree was created at>
    message: ""
  conditions:
    - type: WorktreeReady   # True once the worktree exists and the pod may start
```

`Agent.spec` additions (all optional; absent means the feature is off for that Agent):

```yaml
spec:
  worktrees:
    enabled: true
    helperImage: ghcr.io/cjblackout/kubeopencode-worktree-helper:<tag>
    allowedRepositories: ["https://github.com/cjblackout/*"]
    defaultBankId: homelab        # bank used by Tasks that set no bankId (see 1.7)
    maxWorktrees: 20
    minFreeBytes: 5Gi             # refuse new worktrees below this free space
    credentialsSecretRef: {name: gh-kubeoc-token}   # read-only token, keys username/password
```

### 1.5 Worktree helper

New binary and image `kubeopencode-worktree-helper` (Go, in `cmd/worktree-helper`,
built by `.github/workflows/build.yaml`, multi-arch). The controller injects it as
a sidecar into the agent server Deployment when `spec.worktrees.enabled`.

Runtime:

- Mounts the agent's `/workspace` volume (same PVC as the server) and the
  credentials Secret; listens on `0.0.0.0:4097` inside the pod (the Service gets
  a second port `worktree`), requires `Authorization: Bearer <token>`.
- The token is a per-Agent random Secret created by the controller
  (`<agent>-worktree-token`), mounted into the helper and read by the controller.
- Runs as the same uid/gid as the server container; read-only root filesystem;
  all capabilities dropped; no shell, `git` invoked with an argument vector only.
- Writes only below `/workspace/repos` and `/workspace/worktrees`.

Layout on the volume:

```
/workspace/repos/<repo-key>.git            bare clone, <repo-key> = sha256(repository URL)[:12]
/workspace/worktrees/<task-uid>/<bankId>/  the worktree (basename == bankId, see 1.7)
/workspace/banks/<bankId>/                 empty default directory (see 1.7)
```

HTTP API (JSON, all idempotent):

| Request | Behaviour |
|---|---|
| `PUT /v1/worktrees/<task-uid>` body `{repository, ref, branch, bankId, namespace, name}` | Validate input against `allowedRepositories` and the patterns in 1.4. Take a per-repo lock. Clone the bare repo if absent (`git clone --bare --filter=blob:none`), else `git fetch --prune`. Resolve `ref` to a commit. `git worktree add -b <branch> <path> <commit>` (or reuse the existing worktree and branch when it already exists at that path). Returns `{path, branch, commit}`. |
| `GET /v1/worktrees/<task-uid>` | Returns state or 404. |
| `DELETE /v1/worktrees/<task-uid>` | `git worktree remove --force`, `git branch -D`, remove the directory, `git worktree prune`; 200 also when nothing exists. |
| `GET /v1/worktrees` | List (uid, path, branch, size, created) for garbage collection. |
| `GET /healthz` | Liveness. |

Safety rules:

- Refuses when the number of worktrees is at `maxWorktrees` or free space is below
  `minFreeBytes`; the controller reports this in `status.worktree.message` and
  retries with backoff.
- Credentials are passed to git through `GIT_ASKPASS` for the one invocation, never
  written to `.git/config`, never logged. Clone URL must be `https`.
- Every path is derived from the task UID and validated bankId, never from raw
  user input; symlinks and `..` are rejected.
- Orphan GC: the controller calls `GET /v1/worktrees` once per agent reconcile
  period and deletes helper entries whose Task UID no longer exists.

### 1.6 Controller changes

In `internal/controller/task_controller.go` (Server mode only, and only when
`spec.worktree` is set):

1. **Finalizer** `kubeopencode.io/worktree` is added before any worktree request.
   Today Tasks have no finalizer; it is added only for Tasks that use worktrees.
2. **Create phase**: before creating the Task pod, resolve the context, call
   `PUT /v1/worktrees/<uid>` on the Agent's helper (service DNS of the agent, port
   `worktree`), set `status.worktree` and the `WorktreeReady` condition. Transient
   errors (helper not ready, lock busy, low disk) requeue with exponential backoff
   (30 s … 5 min); a validation error (400) fails the Task with a clear message.
   The Task pod is not created until `WorktreeReady` is true.
3. **Pod**: the attach command becomes
   `opencode run --attach <serverURL> --dir <worktree path> --title <title> "$(cat task.md)"`.
   Session lookup by title is unchanged.
4. **Completion**: nothing is removed. `status.worktree.phase` stays `Ready`.
5. **Deletion** (`deletionTimestamp` set): call `DELETE /v1/worktrees/<uid>`. On
   success, or 404, or when the Agent no longer exists, remove the finalizer. If the
   helper is unreachable the controller retries for up to 10 minutes and then removes
   the finalizer anyway and emits a Warning event `WorktreeLeaked` (the helper's orphan
   GC removes it later). A Task must never be stuck in `Terminating`.
6. **Agent controller**: when `spec.worktrees.enabled`, add the sidecar, the helper
   port on the Service, the token Secret, and the environment in 1.7 to the server
   Deployment; remove them again when disabled.

### 1.7 Bank per session

The Hindsight plugin (`@vectorize-io/opencode-hindsight`) already supports dynamic
bank IDs: with `HINDSIGHT_DYNAMIC_BANK_ID=true` and granularity `["project"]` the
bank is the **basename of the session's working directory**. The worktree path
therefore ends in the bank ID, which makes the session use that bank with no
plugin change:

- Agent server environment (set by the controller when worktrees are enabled):
  `HINDSIGHT_DYNAMIC_BANK_ID=true`, `HINDSIGHT_BANK_GRANULARITY=project`
  (or the equivalent plugin options in `opencode.json`).
- Tasks **with** a worktree run in `/workspace/worktrees/<uid>/<bankId>`.
- Tasks **without** a worktree run in `/workspace/banks/<bankId>` (created empty by
  the helper) when they set `bankId`, else in `/workspace/banks/<defaultBankId>`.
  This also covers Tasks that predate the feature, so no session can fall back to a
  directory like `/workspace` and create an unintended bank named `workspace`.
- Because the bank is derived from the directory, `bankId` is immutable and validated
  to be a plain DNS-style label.

Fallback if the plugin's dynamic mode proves unsuitable (to be decided in phase 1):
patch the plugin to read the bank from a marker file in the session directory
(`.hindsight-bank`, written by the helper). The marker approach needs a plugin fork.

### 1.8 Agent and homelab configuration

- Agents that use the feature set `spec.worktrees` (above) and drop the static
  `bankId` from their plugin options in favour of dynamic mode.
- `CiliumNetworkPolicy`: allow the controller to reach the helper port of the agent
  server pods (ingress), and the helper to reach `github.com` (egress).
- Velero keeps excluding the `agents` namespace; worktrees are scratch and not backed up.
- Disk: worktrees live on the node-local agent volume (30 Gi). A full clone of a
  large repository is avoided by the blobless bare clone; `minFreeBytes` and
  `maxWorktrees` guard the rest.

### 1.9 Banks policy (operations)

Hindsight creates a bank on the first write for an unknown bank ID. The set of banks
is kept small and deliberate (homelab: `nested-operator`, `homelab`, `hlrs`, `kubeopencode`). The controller
rejects `bankId` values that are not in an optional `Agent.spec.worktrees.allowedBankIds`
list (empty list = no restriction); the homelab sets it to those four.

### 1.10 Tests

- Unit: validation (CEL and controller), path derivation, finalizer logic, command
  construction, helper input validation, per-repo locking, disk and count guards.
- envtest: Task lifecycle (create → Ready → Completed → delete → finalizer removed),
  helper down at create time, helper down at delete time, Agent deleted before Task.
- Helper integration: real `git` against a local repository (clone, fetch, add, remove,
  repeat calls, concurrent calls on one repo, corrupt worktree directory).
- e2e (kind): one Task with a worktree against an echo agent; assert the directory,
  the attach `--dir`, and removal on delete.
- Live (homelab): a Qwen Task with `bankId: homelab` and a worktree; check that
  Hindsight retains into that bank only and that no extra bank appears.

### 1.11 Rollout

1. Helper binary, image and API with its tests.
2. CRD fields, validation, controller flow behind `Agent.spec.worktrees.enabled`.
3. Agent controller sidecar wiring; network policy and homelab values.
4. Live test on one Agent (Qwen), then the others.
5. Document in `docs/` and `charts/kubeopencode/README.md`.

Estimated effort: 2–3 days implementation plus 1 day for the live test and hardening.

### 1.12 Open questions

- Whether the plugin's `project` granularity with an explicit `bankId` basename is
  stable across opencode releases (see the fallback in 1.7).
- Whether Pod-mode Tasks should get the same feature later (they currently clone via
  git-init into an ephemeral volume).
- Retention: whether finished worktrees should also expire after N days even if the
  Task object is kept (currently: only on Task deletion, plus orphan GC).

## 2. Supervisor deployment and UI page

Status: specified, not started. The supervisor itself (REST API, OAuth flow, removal of
code-server from the ai-server image, cutover) is specified in the homelab repository,
`ROADMAP.md`, section "Supervisor: REST-driven session supervisor". This section covers
what belongs to this fork: the Helm deployment and the UI page. Estimated effort:
6-8 working days (chart 3-4, UI and server 3-4).

### 2.1 Goal

KubeOpenCode ships a **supervisor** Deployment (Claude Code sessions per project) that is
configured entirely through Helm values, and a **Supervisor** page in the UI (left
navigation) that shows the sessions per project, their logs, and an OAuth button for
the Claude login.

### 2.2 Helm chart

New templates under `charts/kubeopencode/templates/supervisor/`, all gated by
`supervisor.enabled` (default `false`):

- **Workload**: a single-replica StatefulSet (stable volume names, `Recreate`-like
  semantics, no rolling overlap on the RWO volumes), container image
  `supervisor.image` (the image is built by the homelab repository, the chart only
  references it by value), `runtimeClassName` configurable (`supervisor.runtimeClassName`),
  `tolerations`, `affinity`, `nodeSelector`, `resources`, `terminationGracePeriodSeconds`
  (sessions are drained gracefully).
- **Volumes**: `supervisor.persistence.home` (Claude credentials, transcripts, supervisor
  config; default 50Gi) and `supervisor.persistence.workspace` (project clones under
  `/workspace/sessions/<project>`; default 50Gi), each with `existingClaim` support so the
  current `ai-server-home` / `ai-server-workspace` volumes can be adopted.
  `supervisor.extraVolumes` / `extraVolumeMounts` carry the shared tools image volume
  (`/opt/tools`) and anything else.
- **Projects** (initial seed, runtime changes go through the API):

  ```yaml
  supervisor:
    projects:
      - name: homelab
        repository: https://github.com/cjblackout/homelab.git
        ref: main
        sessionId: 10000000-0000-4000-8000-000000000001
        remoteControlName: ai-server-homelab
        memory: |                      # content of the project CLAUDE.md, or memoryFrom: {configMapKeyRef: ...}
          ...
        claudeArgs: ["--dangerously-skip-permissions"]
  ```

- **Credentials**: `supervisor.env` / `envFrom` and `supervisor.secrets` (GitHub write
  token, GitLab token, SSH keys with `defaultMode`, kubeconfig) are plain references to
  existing Secrets; the chart never creates secret material. The API token Secret
  (`supervisor.api.tokenSecretRef`) is generated once when absent (lookup + keep).
- **Access**: ServiceAccount plus optional Role/RoleBinding lists (`supervisor.rbac.rules`)
  so the sessions keep their namespaced kubectl rights; `automountServiceAccountToken`
  configurable. A NetworkPolicy (Kubernetes or Cilium, selectable) admits ingress to the
  API port only from the KubeOpenCode server pod; egress is left to the cluster's own
  policies and extendable through `supervisor.networkPolicy.extraEgress`.
- **Service**: ClusterIP for the API (default 8090), not exposed through any Ingress.
- **Probes**: liveness/readiness on `/healthz`.
- **Homelab overlay**: anything specific to the homelab stays in the homelab repository
  next to the Argo application (ExternalSecrets for the tokens, Cilium egress rules,
  Kata/`/dev/shm` anti-affinity, git-sync sidecars for the read-only checkouts that
  agent-run and agents consume), applied on top of the chart values.
- **Tests**: `helm lint`, `helm template` golden files for the default and a full
  values set, a chart unit test (helm-unittest or kubeconform) in CI; an upgrade check that
  disabling the supervisor removes only its objects.

### 2.3 Server (Go)

- New handlers under `internal/server/handlers/supervisor*.go` that proxy to the
  supervisor API with the shared bearer token: `GET /api/v1/supervisor/projects`,
  `GET .../projects/{name}`, `POST .../projects/{name}/start|stop|restart`,
  `GET .../projects/{name}/logs` (+ SSE stream passthrough),
  `GET /api/v1/supervisor/auth/status`, `POST .../auth/login`,
  `POST .../auth/login/{id}/code`, `GET .../auth/login/{id}`.
- Authorization: the existing server middleware stays the gate (in the homelab,
  Authelia admins only at the edge); mutating calls additionally require the server's
  admin role when its own auth is enabled.
- Streaming: SSE is forwarded without buffering and with the request context so a closed
  browser tab closes the upstream stream; per-user concurrent stream limit.
- The supervisor URL and token come from the chart (`SUPERVISOR_URL`, token file); when
  `supervisor.enabled` is false the endpoints answer 404 and the UI hides the page.

### 2.4 UI (React)

- **Navigation**: a new left-nav item "Supervisor" (route `/supervisor`), shown only when
  the server reports the feature enabled (`GET /api/v1/config`).
- **Page layout**: a toggle (segmented control / tabs) at the top lists the projects
  (`homelab`, `nested-operator`, `kubeopencode`, ...) and switches the whole page between
  them; the selection is kept in the URL (`?project=`) and local storage.
- **Panels** for the selected project:
  - status card (status, uptime, restarts, last crash, transcript, readiness) with
    Start / Stop / Restart buttons (confirmation for Stop and Restart);
  - **Logs**: live stream (SSE) with follow/pause, a tail length selector, search/filter,
    ANSI colours rendered (or stripped on toggle), download of the buffered text.
- **OAuth**: an account banner ("Signed in as ..." or "Not signed in") from
  `auth/status`; the button "Sign in with Claude" calls `auth/login`, shows the returned
  URL (open in a new tab) and a code field; submitting the code calls `auth/login/{id}/code`
  and polls the result; errors show the sanitized supervisor output. Only one login at a time.
- Reuse the existing list/detail page components, the log viewer used by Task detail
  pages if suitable, and the i18n setup; add dark-mode-safe styling like the other pages.
- **Tests**: Vitest component tests with the existing mocks (`ui/src/mocks`), a Go handler
  test with a fake supervisor server (auth, proxying, SSE closing), and a page smoke test.

### 2.5 Rollout

1. Chart templates and values with a fake supervisor image (kind e2e), behind `supervisor.enabled`.
2. Server handlers and the UI page against the real supervisor API from the homelab roadmap
   (needs its Part A and B).
3. Homelab: deploy next to the existing `ai-server` pod using adopted volumes only after the
   cutover steps in the homelab roadmap.

### 2.6 Open questions

- StatefulSet versus Deployment with `Recreate` for the single-replica supervisor.
- Whether the per-project memory file (project `CLAUDE.md`) is stored in values, in a
  ConfigMap per project, or edited through the API and persisted (the homelab keeps them in
  git today).
- Where the log retention lives if a longer history than the ring buffer is wanted
  (currently: none, by design, because logs can contain sensitive data).
