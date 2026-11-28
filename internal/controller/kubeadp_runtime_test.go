// Copyright Contributors to the KubeOpenCode project

//go:build !integration

package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
)

func kubeadpTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := kubeopenv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func envValue(envs []corev1.EnvVar, name string) (string, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestBuildServerDeployment_RunsKubeadpWorker(t *testing.T) {
	agent := &kubeopenv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns"},
		Spec:       kubeopenv1alpha1.AgentSpec{Port: 4096},
	}
	cfg := agentConfig{
		kubeadpImage: "kubeadp:v1",
		workspaceDir: "/workspace",
		credentials: []kubeopenv1alpha1.Credential{
			{Name: "key", SecretRef: kubeopenv1alpha1.SecretReference{Name: "s", Key: ptr.To("k")}, Env: ptr.To("MY_API_KEY")},
			{Name: "file", SecretRef: kubeopenv1alpha1.SecretReference{Name: "s2"}, MountPath: ptr.To("/etc/x")},
		},
	}
	d := BuildServerDeployment(agent, cfg, defaultSystemConfig(), nil, nil, nil, nil, nil)
	if d == nil {
		t.Fatal("nil deployment")
	}
	podSpec := d.Spec.Template.Spec

	// By default the kubeadp-agent image runs the worker directly: no init
	// containers provision /tools, opencode comes from the image itself.
	if len(podSpec.InitContainers) != 0 {
		t.Errorf("default server pod must have no init containers, got: %+v", podSpec.InitContainers)
	}

	var c *corev1.Container
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == ServerContainerName {
			c = &podSpec.Containers[i]
		}
	}
	if c == nil {
		t.Fatal("server container not found")
	}
	if c.Image != "kubeadp:v1" {
		t.Errorf("server container image = %q, want the kubeadp image", c.Image)
	}

	script := strings.Join(c.Command, " ")
	if !strings.Contains(script, "exec kubeadp-agent worker") || strings.Contains(script, "opencode serve") {
		t.Errorf("server command must run the kubeadp-agent worker, got: %s", script)
	}
	if !strings.Contains(script, "'/workspace/workspace'") || !strings.Contains(script, "commit -q --allow-empty") {
		t.Errorf("server command must create the default project repository once: %s", script)
	}

	// Probes use the worker health endpoints.
	if c.StartupProbe == nil || c.StartupProbe.HTTPGet == nil || c.StartupProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("startup probe = %+v", c.StartupProbe)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("liveness probe = %+v", c.LivenessProbe)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("readiness probe = %+v", c.ReadinessProbe)
	}

	// Worker configuration.
	wants := map[string]string{
		"KUBEADP_ROLE":                 "worker",
		"KUBEADP_API_TOKEN_FILE":       KubeadpSecretMountPath + "/" + KubeadpAPITokenKey,
		"KUBEADP_JOBSET_ROOT_KEY_FILE": KubeadpSecretMountPath + "/" + KubeadpJobsetRootKeyKey,
		"AGENT_RUN_WORKSPACE_ROOT":     "/workspace",
		"AGENT_RUN_OPENCODE_PATH":      "/usr/local/bin/opencode",
		"AGENT_RUN_API_LISTEN":         "0.0.0.0:4096",
	}
	for k, want := range wants {
		if got, ok := envValue(c.Env, k); !ok || got != want {
			t.Errorf("%s = %q (present=%v), want %q", k, got, ok, want)
		}
	}

	// The sandbox env allow-list: OPENCODE_* plus the env names of env-type
	// credentials, never a value and never a file-type credential.
	allow, ok := envValue(c.Env, "KUBEADP_SANDBOX_ENV_ALLOW")
	if !ok {
		t.Fatal("KUBEADP_SANDBOX_ENV_ALLOW missing")
	}
	if allow != "OPENCODE_*,MY_API_KEY" {
		t.Errorf("KUBEADP_SANDBOX_ENV_ALLOW = %q, want OPENCODE_*,MY_API_KEY", allow)
	}

	// The Secret with the token and the root key is mounted read-only.
	var mounted bool
	for _, vm := range c.VolumeMounts {
		if vm.Name == KubeadpSecretVolumeName && vm.MountPath == KubeadpSecretMountPath && vm.ReadOnly {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("kubeadp Secret must be mounted read-only at %s: %+v", KubeadpSecretMountPath, c.VolumeMounts)
	}
	var secretVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == KubeadpSecretVolumeName {
			secretVol = &podSpec.Volumes[i]
		}
	}
	if secretVol == nil || secretVol.Secret == nil || secretVol.Secret.SecretName != "ag"+KubeadpSecretSuffix {
		t.Errorf("kubeadp Secret volume = %+v", secretVol)
	}
}

func TestBuildServerDeployment_ExplicitExecutorUsesKubeadpInit(t *testing.T) {
	agent := &kubeopenv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns"},
		Spec:       kubeopenv1alpha1.AgentSpec{Port: 4096},
	}
	cfg := agentConfig{
		kubeadpImage:          "kubeadp:v1",
		executorImage:         "devbox:v1",
		executorImageExplicit: true,
		workspaceDir:          "/workspace",
	}
	d := BuildServerDeployment(agent, cfg, defaultSystemConfig(), nil, nil, nil, nil, nil)
	if d == nil {
		t.Fatal("nil deployment")
	}
	podSpec := d.Spec.Template.Spec

	// An explicit executorImage provisions /tools through the single kubeadp-init
	// container: it copies kubeadp-agent, agent-run and opencode.
	if len(podSpec.InitContainers) != 1 || podSpec.InitContainers[0].Name != "kubeadp-init" {
		t.Fatalf("expected a single kubeadp-init init container, got: %+v", podSpec.InitContainers)
	}
	icCmd := strings.Join(podSpec.InitContainers[0].Command, " ")
	if podSpec.InitContainers[0].Image != "kubeadp:v1" ||
		!strings.Contains(icCmd, "/tools/kubeadp-agent") ||
		!strings.Contains(icCmd, "/tools/opencode") {
		t.Errorf("kubeadp-init must copy kubeadp-agent, agent-run and opencode to /tools from the kubeadp image: %+v", podSpec.InitContainers[0])
	}

	var c *corev1.Container
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == ServerContainerName {
			c = &podSpec.Containers[i]
		}
	}
	if c == nil {
		t.Fatal("server container not found")
	}
	if c.Image != "devbox:v1" {
		t.Errorf("server container image = %q, want the explicit executor image", c.Image)
	}
	script := strings.Join(c.Command, " ")
	if !strings.Contains(script, "exec /tools/kubeadp-agent worker") {
		t.Errorf("explicit executor server must run the worker from /tools, got: %s", script)
	}
	if got, ok := envValue(c.Env, "AGENT_RUN_OPENCODE_PATH"); !ok || got != "/tools/opencode" {
		t.Errorf("AGENT_RUN_OPENCODE_PATH = %q (present=%v), want /tools/opencode", got, ok)
	}
}

func TestBuildServerDeployment_SessionPersistencePointsHomeAtSessionVolume(t *testing.T) {
	agent := &kubeopenv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns"},
		Spec: kubeopenv1alpha1.AgentSpec{
			Port: 4096,
			Persistence: &kubeopenv1alpha1.PersistenceConfig{
				Sessions: &kubeopenv1alpha1.VolumePersistence{Size: "1Gi"},
			},
		},
	}
	cfg := agentConfig{
		kubeadpImage:  "kubeadp:v1",
		workspaceDir:  "/workspace",
		executorImage: "test-executor:v1.0.0",
	}
	d := BuildServerDeployment(agent, cfg, defaultSystemConfig(), nil, nil, nil, nil, nil)
	if d == nil {
		t.Fatal("nil deployment")
	}
	podSpec := d.Spec.Template.Spec

	var c *corev1.Container
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == ServerContainerName {
			c = &podSpec.Containers[i]
		}
	}
	if c == nil {
		t.Fatal("server container not found")
	}

	// The session PVC is mounted and AGENT_RUN_STATE_ROOT points at it.
	var foundStateEnv bool
	for _, env := range c.Env {
		if env.Name == "AGENT_RUN_STATE_ROOT" && env.Value == ServerSessionMountPath {
			foundStateEnv = true
		}
	}
	if !foundStateEnv {
		t.Errorf("AGENT_RUN_STATE_ROOT must point at ServerSessionMountPath with session persistence")
	}
	var foundHome bool
	for _, env := range c.Env {
		if env.Name == "HOME" && strings.HasPrefix(env.Value, ServerSessionMountPath) {
			foundHome = true
		}
	}
	if !foundHome {
		t.Errorf("HOME must live on the session volume with session persistence, got HOME=%+v", c.Env)
	}
	var foundSandboxHome bool
	for _, env := range c.Env {
		if env.Name == "KUBEADP_SANDBOX_HOME" && strings.HasPrefix(env.Value, ServerSessionMountPath) {
			foundSandboxHome = true
		}
	}
	if !foundSandboxHome {
		t.Errorf("KUBEADP_SANDBOX_HOME must live on the session volume with session persistence")
	}

	// The session PVC volume + mount exist.
	var foundVol bool
	for _, vol := range podSpec.Volumes {
		if vol.Name == ServerSessionVolumeName && vol.PersistentVolumeClaim != nil {
			foundVol = true
		}
	}
	if !foundVol {
		t.Error("session PVC volume not found")
	}
	var foundMount bool
	for _, mount := range c.VolumeMounts {
		if mount.Name == ServerSessionVolumeName && mount.MountPath == ServerSessionMountPath {
			foundMount = true
		}
	}
	if !foundMount {
		t.Error("session PVC mount not found")
	}

	// The command pre-creates the home dir on the PVC. The path is shell-escaped
	// (quoted) by workspaceInitScript, exactly like the workspace dir.
	script := strings.Join(c.Command, " ")
	if !strings.Contains(script, "mkdir -p '"+ServerSessionHomePath+"'") {
		t.Errorf("server command must pre-create the session home dir, got: %s", script)
	}
}

func TestBuildServerDeployment_NoPersistenceKeepsTempHome(t *testing.T) {
	agent := &kubeopenv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns"},
		Spec:       kubeopenv1alpha1.AgentSpec{Port: 4096},
	}
	cfg := agentConfig{
		kubeadpImage:  "kubeadp:v1",
		workspaceDir:  "/workspace",
		executorImage: "test-executor:v1.0.0",
	}
	d := BuildServerDeployment(agent, cfg, defaultSystemConfig(), nil, nil, nil, nil, nil)
	if d == nil {
		t.Fatal("nil deployment")
	}
	var c *corev1.Container
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == ServerContainerName {
			c = &d.Spec.Template.Spec.Containers[i]
		}
	}
	if c == nil {
		t.Fatal("server container not found")
	}
	for _, env := range c.Env {
		if env.Name == "HOME" && env.Value != DefaultHomeDir {
			t.Errorf("HOME must be the default %q without persistence, got %q", DefaultHomeDir, env.Value)
		}
	}
}

func TestReconcileKubeadpSecretCreatesOnceAndNeverRotates(t *testing.T) {
	scheme := kubeadpTestScheme(t)
	agent := &kubeopenv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns", UID: "uid-1"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	r := &AgentReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()

	if err := r.reconcileKubeadpSecret(ctx, agent); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	var s1 corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "ag" + KubeadpSecretSuffix}, &s1); err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	token, root := string(s1.Data[KubeadpAPITokenKey]), string(s1.Data[KubeadpJobsetRootKeyKey])
	if len(token) < 32 || len(root) < 32 {
		t.Errorf("secrets must be >= 32 bytes: token=%d root=%d", len(token), len(root))
	}
	if token == root {
		t.Error("api token and jobset root key must differ")
	}
	if len(s1.OwnerReferences) != 1 || s1.OwnerReferences[0].Name != "ag" || s1.OwnerReferences[0].Controller == nil || !*s1.OwnerReferences[0].Controller {
		t.Errorf("secret must be controlled by the agent: %+v", s1.OwnerReferences)
	}

	if err := r.reconcileKubeadpSecret(ctx, agent); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var s2 corev1.Secret
	_ = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "ag" + KubeadpSecretSuffix}, &s2)
	if string(s2.Data[KubeadpAPITokenKey]) != token || string(s2.Data[KubeadpJobsetRootKeyKey]) != root {
		t.Error("an existing Secret must never be rotated by reconcile")
	}
}

func TestResolveSessionInfoFillsJobIDAndUsage(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"exit_code":0,"data":{"job_id":"a1b2c3d4","status":"done","response":"ok","usage":{"input_tokens":11,"output_tokens":4,"reasoning_tokens":1,"cache_tokens":2,"cost":0.25}}}`)
	}))
	defer srv.Close()

	scheme := kubeadpTestScheme(t)
	agent := &kubeopenv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "ns"},
		Status:     kubeopenv1alpha1.AgentStatus{URL: srv.URL},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ag" + KubeadpSecretSuffix, Namespace: "ns"},
		Data:       map[string][]byte{KubeadpAPITokenKey: []byte("tok\n")},
	}
	task := &kubeopenv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Status: kubeopenv1alpha1.TaskExecutionStatus{
			AgentRef: &kubeopenv1alpha1.AgentReference{Name: "ag"},
			Session:  &kubeopenv1alpha1.SessionInfo{Title: "kubeopencode/ns/t"},
		},
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "agent",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: `{"jobId":"a1b2c3d4"}`}},
	}}}}

	r := &TaskReconciler{
		Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, secret).Build(),
		Scheme:        scheme,
		kubeadpClient: NewKubeadpClient(),
	}
	r.resolveSessionInfo(context.Background(), task, pod)

	if task.Status.Session.ID != "a1b2c3d4" {
		t.Errorf("session id = %q, want the job id", task.Status.Session.ID)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("the trimmed Secret token must be sent as bearer, got %q", gotAuth)
	}
	sum := task.Status.Session.Summary
	if sum == nil || sum.TokenUsage == nil || sum.TokenUsage.Input != 11 || sum.TokenUsage.Output != 4 ||
		sum.TokenUsage.Reasoning != 1 || sum.TokenUsage.Cache != 2 || sum.Cost != "0.250000" {
		t.Errorf("summary = %+v", sum)
	}
}

func TestResolveSessionInfoKeepsJobIDWhenUsageUnavailable(t *testing.T) {
	scheme := kubeadpTestScheme(t)
	task := &kubeopenv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Status: kubeopenv1alpha1.TaskExecutionStatus{
			AgentRef: &kubeopenv1alpha1.AgentReference{Name: "ag"},
			Session:  &kubeopenv1alpha1.SessionInfo{Title: "kubeopencode/ns/t"},
		},
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name:  "agent",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: `{"jobId":"deadbeef"}`}},
	}}}}
	// No Agent object exists: usage cannot be read, the job id must survive.
	r := &TaskReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme, kubeadpClient: NewKubeadpClient()}
	r.resolveSessionInfo(context.Background(), task, pod)
	if task.Status.Session.ID != "deadbeef" || task.Status.Session.Summary != nil {
		t.Errorf("session = %+v", task.Status.Session)
	}

	// No published job id (stopped Task): nothing is resolved and nothing panics.
	task.Status.Session.ID = ""
	r.resolveSessionInfo(context.Background(), task, nil)
	if task.Status.Session.ID != "" {
		t.Errorf("session id must stay empty without a published job id: %q", task.Status.Session.ID)
	}
}
