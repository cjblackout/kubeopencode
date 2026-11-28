// Copyright Contributors to the KubeOpenCode project

package controller

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
)

const (
	// KubeadpClientVolumeName is the volume that mounts the Agent's kubeadp
	// Secret (API token only) into Task Pods that submit jobs to the worker.
	KubeadpClientVolumeName = "kubeadp-client"

	// KubeadpClientMountPath is where the API token is mounted in the Task Pod.
	KubeadpClientMountPath = "/etc/kubeadp-client"

	// TaskProjectAnnotation selects the project a Task job runs in. The value is
	// the name of a git repository directory directly under the Agent workspace
	// directory (for example the mount directory of an Agent git context).
	TaskProjectAnnotation = "kubeopencode.io/project"

	// TaskModelAnnotation selects the model (provider/model) a Task job uses
	// instead of the engine default from the Agent configuration.
	TaskModelAnnotation = "kubeopencode.io/model"

	// DefaultTaskProject is the project (git repository directory under the
	// worker workspace root) a Task job runs in when the Task does not select
	// one and the Agent has no git context. The server Pod creates it once.
	DefaultTaskProject = "workspace"
)

// taskAgentName returns the name of the Agent a Task references, or "" for a
// Task without agentRef.
func taskAgentName(task *kubeopenv1alpha1.Task) string {
	if task.Spec.AgentRef == nil {
		return ""
	}
	return task.Spec.AgentRef.Name
}

// buildKubeadpClientVolume returns the read-only Secret volume that gives a
// Task Pod the bearer token of the Agent's kubeadp-agent worker. Only the API
// token key is projected; the jobset root key never leaves the server Pod.
func buildKubeadpClientVolume(agentName string) corev1.Volume {
	mode := int32(0o400)
	return corev1.Volume{
		Name: KubeadpClientVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  KubeadpSecretName(agentName),
				DefaultMode: &mode,
				Items: []corev1.KeyToPath{
					{Key: KubeadpAPITokenKey, Path: KubeadpAPITokenKey},
				},
			},
		},
	}
}

// taskProject returns the project a Task job runs in: the explicit annotation,
// else the first git mount below the workspace directory (Agent git contexts are
// listed first and are cloned into the server Pod at the same path), else the
// default project.
func taskProject(task *kubeopenv1alpha1.Task, workspaceDir string, gitMounts []gitMount) string {
	if p := strings.TrimSpace(task.Annotations[TaskProjectAnnotation]); p != "" {
		return path.Base(path.Clean(p))
	}
	for _, gm := range gitMounts {
		mp := gm.mountPath
		if mp == "" {
			continue
		}
		if !path.IsAbs(mp) {
			mp = path.Join(workspaceDir, mp)
		}
		rel := strings.TrimPrefix(path.Clean(mp), path.Clean(workspaceDir)+"/")
		if rel == path.Clean(mp) || rel == "" {
			continue // not below the workspace directory
		}
		return strings.SplitN(rel, "/", 2)[0]
	}
	return DefaultTaskProject
}

// buildTaskSubmitCommand returns the command of the Task Pod container:
// `kubeopencode task-submit` with the worker URL, the prompt file written by
// the context init, the token file, the project and the Task timeout.
func buildTaskSubmitCommand(task *kubeopenv1alpha1.Task, cfg agentConfig, serverURL, project string) []string {
	cmd := []string{
		"/kubeopencode", "task-submit",
		"--url", serverURL,
		"--task-file", cfg.workspaceDir + "/task.md",
		"--token-file", KubeadpClientMountPath + "/" + KubeadpAPITokenKey,
		"--project", project,
	}
	if model := strings.TrimSpace(task.Annotations[TaskModelAnnotation]); model != "" {
		cmd = append(cmd, "--model", model)
	}
	if task.Spec.Timeout != nil && task.Spec.Timeout.Duration > 0 {
		// Pass the timeout in whole seconds so the worker enforces max_runtime
		// even if the Pod is not deleted on time.
		secs := int64(task.Spec.Timeout.Duration.Seconds())
		if secs < 1 {
			secs = 1
		}
		cmd = append(cmd, "--timeout", strconv.FormatInt(secs, 10)+"s")
	}
	return cmd
}

// workspaceInitScript returns the shell snippet that creates the default
// project repository once (idempotent) and runs the worker command. The job API
// creates a git worktree of a project, so the project must be a repository with
// at least one commit. When one or more homeDirs are given (e.g. HOME on a
// persistent session volume), each is created first so the shell and the engine
// have a writable home.
func workspaceInitScript(workspaceDir, workerCommand string, homeDirs ...string) string {
	repo := path.Join(workspaceDir, DefaultTaskProject)
	init := fmt.Sprintf(
		`mkdir -p %[1]s && if [ ! -d %[1]s/.git ]; then git -C %[1]s init -q -b main && git -C %[1]s -c user.name=kubeopencode -c user.email=kubeopencode@localhost commit -q --allow-empty -m "kubeopencode: initial commit"; fi; %[2]s`,
		shellEscape(repo), workerCommand)
	for _, home := range homeDirs {
		init = "mkdir -p " + shellEscape(home) + " && " + init
	}
	return init
}
