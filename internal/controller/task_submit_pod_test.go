// Copyright Contributors to the KubeOpenCode project

//go:build !integration

package controller

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
)

func TestTaskProject(t *testing.T) {
	mk := func(annotations map[string]string) *kubeopenv1alpha1.Task {
		return &kubeopenv1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns", Annotations: annotations}}
	}
	tests := []struct {
		name   string
		task   *kubeopenv1alpha1.Task
		mounts []gitMount
		want   string
	}{
		{"default without git contexts", mk(nil), nil, DefaultTaskProject},
		{"annotation wins", mk(map[string]string{TaskProjectAnnotation: "my-repo"}), []gitMount{{mountPath: "/workspace/other"}}, "my-repo"},
		{"annotation path is reduced to its base name", mk(map[string]string{TaskProjectAnnotation: "../../etc/passwd"}), nil, "passwd"},
		{"first git mount below the workspace", mk(nil), []gitMount{{mountPath: "/workspace/app"}, {mountPath: "/workspace/lib"}}, "app"},
		{"nested mount path uses the first segment", mk(nil), []gitMount{{mountPath: "/workspace/app/sub/dir"}}, "app"},
		{"relative mount path", mk(nil), []gitMount{{mountPath: "app"}}, "app"},
		{"mount outside the workspace is ignored", mk(nil), []gitMount{{mountPath: "/opt/skills"}}, DefaultTaskProject},
		{"mount at the workspace root is ignored", mk(nil), []gitMount{{mountPath: "/workspace"}}, DefaultTaskProject},
		{"empty mount path is skipped", mk(nil), []gitMount{{mountPath: ""}, {mountPath: "/workspace/second"}}, "second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskProject(tt.task, "/workspace", tt.mounts); got != tt.want {
				t.Errorf("taskProject = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildTaskSubmitCommand(t *testing.T) {
	task := &kubeopenv1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"}}
	cfg := agentConfig{workspaceDir: "/work"}
	cmd := buildTaskSubmitCommand(task, cfg, "http://agent.ns.svc:4096", "proj")

	want := []string{
		"/kubeopencode", "task-submit",
		"--url", "http://agent.ns.svc:4096",
		"--task-file", "/work/task.md",
		"--token-file", KubeadpClientMountPath + "/" + KubeadpAPITokenKey,
		"--project", "proj",
	}
	if strings.Join(cmd, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("command = %v, want %v", cmd, want)
	}
	if strings.Contains(strings.Join(cmd, " "), "opencode run") || strings.Contains(strings.Join(cmd, " "), "--attach") {
		t.Errorf("the Task command must not use the opencode server API: %v", cmd)
	}

	task.Annotations = map[string]string{TaskModelAnnotation: "prov/model-1"}
	task.Spec.Timeout = &metav1.Duration{Duration: 90 * time.Minute}
	cmd = buildTaskSubmitCommand(task, cfg, "http://x", "p")
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "--model prov/model-1") || !strings.Contains(joined, "--timeout 5400s") {
		t.Errorf("model and timeout must be passed: %v", cmd)
	}
}

func TestBuildTaskSubmitCommandTimeoutRoundsUpToOneSecond(t *testing.T) {
	task := &kubeopenv1alpha1.Task{Spec: kubeopenv1alpha1.TaskSpec{Timeout: &metav1.Duration{Duration: 100 * time.Millisecond}}}
	joined := strings.Join(buildTaskSubmitCommand(task, agentConfig{workspaceDir: "/w"}, "http://x", "p"), " ")
	if !strings.Contains(joined, "--timeout 1s") {
		t.Errorf("sub-second timeouts must become 1s: %s", joined)
	}
}

func TestBuildKubeadpClientVolumeProjectsOnlyTheToken(t *testing.T) {
	v := buildKubeadpClientVolume("my-agent")
	if v.Name != KubeadpClientVolumeName || v.Secret == nil {
		t.Fatalf("unexpected volume: %+v", v)
	}
	if v.Secret.SecretName != "my-agent"+KubeadpSecretSuffix {
		t.Errorf("secret name = %q", v.Secret.SecretName)
	}
	if len(v.Secret.Items) != 1 || v.Secret.Items[0].Key != KubeadpAPITokenKey {
		t.Errorf("only the api token may be projected: %+v", v.Secret.Items)
	}
	for _, it := range v.Secret.Items {
		if it.Key == KubeadpJobsetRootKeyKey {
			t.Error("the jobset root key must never reach Task Pods")
		}
	}
	if v.Secret.DefaultMode == nil || *v.Secret.DefaultMode != 0o400 {
		t.Errorf("token file mode must be 0400: %v", v.Secret.DefaultMode)
	}
}

func TestWorkspaceInitScript(t *testing.T) {
	s := workspaceInitScript("/workspace", "exec /tools/kubeadp-agent worker")
	for _, want := range []string{
		"mkdir -p '/workspace/workspace'",
		"[ ! -d '/workspace/workspace'/.git ]",
		"init -q -b main",
		"commit -q --allow-empty",
		"exec /tools/kubeadp-agent worker",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("init script lacks %q:\n%s", want, s)
		}
	}
	if !strings.HasSuffix(s, "exec /tools/kubeadp-agent worker") {
		t.Errorf("the worker command must come last: %s", s)
	}
}

func TestBuildPodAgentRefUsesTaskSubmitAndSystemImage(t *testing.T) {
	task := &kubeopenv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "default"},
		Spec:       kubeopenv1alpha1.TaskSpec{AgentRef: &kubeopenv1alpha1.AgentReference{Name: "my-agent"}},
	}
	cfg := agentConfig{
		executorImage: "devbox:v1",
		workspaceDir:  "/workspace", serviceAccountName: "sa",
	}
	sys := defaultSystemConfig()
	pod := buildPod(task, "t-pod", cfg, nil, nil, nil, nil, sys, "http://my-agent.default.svc:4096")

	c := pod.Spec.Containers[0]
	if c.Image != sys.systemImage {
		t.Errorf("image = %q, want the system image %q", c.Image, sys.systemImage)
	}
	if len(c.Command) < 2 || c.Command[0] != "/kubeopencode" || c.Command[1] != "task-submit" {
		t.Fatalf("command = %v", c.Command)
	}

	var found bool
	for _, vm := range c.VolumeMounts {
		if vm.Name == KubeadpClientVolumeName && vm.MountPath == KubeadpClientMountPath && vm.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Errorf("token volume must be mounted read-only: %+v", c.VolumeMounts)
	}
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == KubeadpClientVolumeName {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != "my-agent"+KubeadpSecretSuffix {
		t.Errorf("token Secret volume missing or wrong: %+v", vol)
	}
}

func TestBuildPodAgentRefWithCustomCommandKeepsExecutorImage(t *testing.T) {
	task := &kubeopenv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "default"},
		Spec:       kubeopenv1alpha1.TaskSpec{AgentRef: &kubeopenv1alpha1.AgentReference{Name: "a"}},
	}
	cfg := agentConfig{
		executorImage: "devbox:v1", workspaceDir: "/workspace",
		command: []string{"sh", "-c", "echo custom"},
	}
	pod := buildPod(task, "t-pod", cfg, nil, nil, nil, nil, defaultSystemConfig(), "http://a.default.svc:4096")
	c := pod.Spec.Containers[0]
	if c.Image != "devbox:v1" {
		t.Errorf("a custom command keeps the executor image, got %q", c.Image)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == KubeadpClientVolumeName {
			t.Error("a custom command must not get the token volume")
		}
	}
}

func TestJobIDFromPod(t *testing.T) {
	podWith := func(name, msg string, terminated bool) *corev1.Pod {
		cs := corev1.ContainerStatus{Name: name}
		if terminated {
			cs.State.Terminated = &corev1.ContainerStateTerminated{Message: msg}
		}
		return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}}}
	}
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"nil pod", nil, ""},
		{"published id", podWith("agent", `{"jobId":"a1b2c3d4"}`, true), "a1b2c3d4"},
		{"not terminated", podWith("agent", `{"jobId":"a1b2c3d4"}`, false), ""},
		{"other container", podWith("sidecar", `{"jobId":"a1b2c3d4"}`, true), ""},
		{"garbage message", podWith("agent", "OOMKilled", true), ""},
		{"empty id", podWith("agent", `{"jobId":""}`, true), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobIDFromPod(tt.pod); got != tt.want {
				t.Errorf("jobIDFromPod = %q, want %q", got, tt.want)
			}
		})
	}
}
