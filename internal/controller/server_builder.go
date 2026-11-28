// Copyright Contributors to the KubeOpenCode project

package controller

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
)

const (
	// ServerDeploymentSuffix is appended to Agent name for the Deployment name.
	ServerDeploymentSuffix = "-server"

	// ServerContainerName is the name of the main container in the server Deployment.
	ServerContainerName = "kubeadp-agent"

	// DefaultServerPort is the default port for OpenCode server.
	DefaultServerPort int32 = 4096

	// ServerHealthPath is the path used for readiness probes.
	ServerHealthPath = "/readyz"

	// ServerStartupHealthPath is the path used for startup and liveness probes.
	ServerStartupHealthPath = "/healthz"

	// KubeadpSecretSuffix is appended to Agent name for the kubeadp auth secret.
	KubeadpSecretSuffix = "-kubeadp"

	// KubeadpSecretVolumeName is the volume name for mounting the kubeadp secret.
	KubeadpSecretVolumeName = "kubeadp-secret"

	// KubeadpSecretMountPath is where the kubeadp secret is mounted in the server container.
	KubeadpSecretMountPath = "/etc/kubeadp"

	// KubeadpAPITokenKey is the key in the secret containing the API Bearer token.
	KubeadpAPITokenKey = "api-token"

	// KubeadpJobsetRootKeyKey is the key in the secret containing the jobset root key.
	KubeadpJobsetRootKeyKey = "jobset-root-key"

	// ServerSessionPVCSuffix is appended to Agent name for the session PVC name.
	ServerSessionPVCSuffix = "-server-sessions"

	// ServerSessionVolumeName is the volume name for the session PVC.
	ServerSessionVolumeName = "session-data"

	// ServerSessionMountPath is where the session PVC is mounted in the server container.
	ServerSessionMountPath = "/data/sessions"

	// ServerSessionHomePath is the HOME directory of the server container when
	// session persistence is enabled. It lives inside the session PVC so the
	// opencode engine's own state (~/.config/opencode, its node_modules and
	// database) survives pod restarts alongside the worker's AGENT_RUN_STATE_ROOT.
	// The worker pod has no /etc/passwd entry for the running uid in SCC
	// environments, so a fixed absolute path is used and pre-created by the
	// workspace init script.
	ServerSessionHomePath = ServerSessionMountPath + "/home"

	// DefaultSessionPVCSize is the default size for the session PVC.
	DefaultSessionPVCSize = "1Gi"

	// ServerWorkspacePVCSuffix is appended to Agent name for the workspace PVC name.
	ServerWorkspacePVCSuffix = "-server-workspace"

	// DefaultWorkspacePVCSize is the default size for the workspace PVC.
	DefaultWorkspacePVCSize = "10Gi"

	// DefaultProbeTimeoutSeconds is the default timeout for probe HTTP/TCP checks.
	DefaultProbeTimeoutSeconds = 5

	// DefaultProbeFailureThreshold is the default failure threshold before probe is considered failed.
	DefaultProbeFailureThreshold = 3

	// DefaultStartupFailureThreshold is the failure threshold for the startup probe.
	// 2 + 2*30 = 62s max startup time.
	DefaultStartupFailureThreshold = 30

	// DefaultStartupPeriodSeconds is the period for the startup probe.
	DefaultStartupPeriodSeconds = 2

	// DefaultLivenessPeriodSeconds is the period for the liveness probe.
	DefaultLivenessPeriodSeconds = 30

	// DefaultReadinessPeriodSeconds is the period for the readiness probe.
	DefaultReadinessPeriodSeconds = 10
)

// ServerDeploymentName returns the Deployment name for a Server-mode Agent.
func ServerDeploymentName(agentName string) string {
	return agentName + ServerDeploymentSuffix
}

// ServerServiceName returns the Service name for a Server-mode Agent.
// We use the Agent name directly for simpler DNS resolution.
func ServerServiceName(agentName string) string {
	return agentName
}

// ServerURL returns the in-cluster URL for a Server-mode Agent.
func ServerURL(agentName, namespace string, port int32, clusterDomain string) string {
	return fmt.Sprintf("http://%s.%s.svc.%s:%d", agentName, namespace, clusterDomain, port)
}

// KubeadpSecretName returns the Secret name for an Agent's kubeadp auth token and jobset root key.
func KubeadpSecretName(agentName string) string {
	return agentName + KubeadpSecretSuffix
}

// ServerSessionPVCName returns the PVC name for session persistence.
func ServerSessionPVCName(agentName string) string {
	return agentName + ServerSessionPVCSuffix
}

// ServerWorkspacePVCName returns the PVC name for workspace persistence.
func ServerWorkspacePVCName(agentName string) string {
	return agentName + ServerWorkspacePVCSuffix
}

// BuildServerWorkspacePVC creates a PersistentVolumeClaim for workspace persistence.
// Returns (nil, nil) if workspace persistence is not configured.
func BuildServerWorkspacePVC(agent *kubeopenv1alpha1.Agent) (*corev1.PersistentVolumeClaim, error) {
	if agent.Spec.Persistence == nil ||
		agent.Spec.Persistence.Workspace == nil {
		return nil, nil
	}
	return buildServerPVC(agent, agent.Spec.Persistence.Workspace,
		ServerWorkspacePVCName(agent.Name), DefaultWorkspacePVCSize, "workspace")
}

// BuildServerSessionPVC creates a PersistentVolumeClaim for session data persistence.
// Returns (nil, nil) if session persistence is not configured.
func BuildServerSessionPVC(agent *kubeopenv1alpha1.Agent) (*corev1.PersistentVolumeClaim, error) {
	if agent.Spec.Persistence == nil ||
		agent.Spec.Persistence.Sessions == nil {
		return nil, nil
	}
	return buildServerPVC(agent, agent.Spec.Persistence.Sessions,
		ServerSessionPVCName(agent.Name), DefaultSessionPVCSize, "session")
}

// buildServerPVC creates a PVC with the given configuration.
func buildServerPVC(agent *kubeopenv1alpha1.Agent, vol *kubeopenv1alpha1.VolumePersistence, pvcName, defaultSize, label string) (*corev1.PersistentVolumeClaim, error) {
	size := vol.Size
	if size == "" {
		size = defaultSize
	}

	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return nil, fmt.Errorf("invalid %s PVC size %q: %w", label, size, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: agent.Namespace,
			Labels:    getServerLabels(agent.Name),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}

	if vol.StorageClassName != nil {
		pvc.Spec.StorageClassName = vol.StorageClassName
	}

	return pvc, nil
}

// getServerLabels returns the common labels used by Server-mode resources.
func getServerLabels(agentName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "kubeopencode-server",
		"app.kubernetes.io/instance":   agentName,
		"app.kubernetes.io/component":  "server",
		"app.kubernetes.io/managed-by": "kubeopencode",
		AgentLabelKey:                  agentName,
	}
}

// BuildServerDeployment creates a Deployment for an Agent.
// The Deployment runs OpenCode in serve mode with a single replica.
// Context parameters (contextConfigMap, fileMounts, dirMounts, gitMounts) enable
// Agent-level contexts to be loaded via init containers.
func BuildServerDeployment(agent *kubeopenv1alpha1.Agent, agentCfg agentConfig, sysCfg systemConfig, contextConfigMap *corev1.ConfigMap, ctxFileMounts []fileMount, ctxDirMounts []dirMount, ctxGitMounts []gitMount, gitHashAnnotations map[string]string) *appsv1.Deployment {
	port := GetServerPort(agent)

	// Build labels for selector and pod template
	labels := getServerLabels(agent.Name)

	// Merge custom labels from PodSpec if provided
	if agentCfg.podSpec != nil && agentCfg.podSpec.Labels != nil {
		maps.Copy(labels, agentCfg.podSpec.Labels)
	}

	// Build pod annotations for the Deployment pod template.
	//
	// When the user does not set podSpec.annotations, preserve the controller-
	// managed gitHashAnnotations as-is (which may be nil when there is no git
	// sync and no context ConfigMap). This avoids flipping nil -> empty map {},
	// which could needlessly churn the pod template on upgrade.
	//
	// When the user does set podSpec.annotations, merge them on top of the
	// controller-managed annotations (user wins on key conflict). User
	// annotations such as a ConfigMap checksum are part of the pod template, so
	// changing them triggers a new ReplicaSet and rollout. See issue #280.
	annotations := gitHashAnnotations
	if agentCfg.podSpec != nil && len(agentCfg.podSpec.Annotations) > 0 {
		annotations = make(map[string]string, len(gitHashAnnotations)+len(agentCfg.podSpec.Annotations))
		maps.Copy(annotations, gitHashAnnotations)
		maps.Copy(annotations, agentCfg.podSpec.Annotations)
	}

	// Build environment variables
	// HOME and SHELL are set for SCC (Security Context Constraints) compatibility.
	// In SCC environments, containers run with random UIDs that have no /etc/passwd entry,
	// causing HOME=/ (not writable) and SHELL=/sbin/nologin.
	//
	// By default the server container runs the kubeadp-agent image itself: the
	// kubeadp-agent binary and the bundled opencode engine live in /usr/local/bin.
	// When the user explicitly sets executorImage, a custom worker image without
	// the binaries, kubeadp-init provisions /tools and the worker + opencode are
	// read from there instead.
	stateRoot := ServerSessionMountPath
	opencodeBinary := "/usr/local/bin/opencode"
	workerCommand := "exec kubeadp-agent worker"
	containerImage := agentCfg.kubeadpImage
	initPrefix := "true"
	homeDir := DefaultHomeDir
	if agentCfg.executorImageExplicit {
		opencodeBinary = ToolsMountPath + "/opencode"
		workerCommand = "exec /tools/kubeadp-agent worker"
		containerImage = agentCfg.executorImage
		initPrefix = OpenCodeSymlinkCmd
	}
	// When session persistence is enabled the opencode engine writes its state
	// (~/.config/opencode, node_modules, DB) under HOME; point HOME into the
	// session PVC so that state survives pod restarts instead of being wiped
	// from the ephemeral /tmp.
	sessionPersistence := agent.Spec.Persistence != nil && agent.Spec.Persistence.Sessions != nil
	if sessionPersistence {
		homeDir = ServerSessionHomePath
	}
	envVars := []corev1.EnvVar{
		{Name: "HOME", Value: homeDir},
		{Name: "SHELL", Value: DefaultShell},
		// Prepend /tools to PATH so the OpenCode and kubeadp-agent binaries (copied by kubeadp-init)
		// are discoverable from interactive terminals.
		{Name: "PATH", Value: ToolsMountPath + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		{Name: "WORKSPACE_DIR", Value: agentCfg.workspaceDir},
		// kubeadp-agent configuration
		{Name: "KUBEADP_API_TOKEN_FILE", Value: KubeadpSecretMountPath + "/" + KubeadpAPITokenKey},
		{Name: "KUBEADP_JOBSET_ROOT_KEY_FILE", Value: KubeadpSecretMountPath + "/" + KubeadpJobsetRootKeyKey},
		{Name: "KUBEADP_ROLE", Value: "worker"},
		{Name: "AGENT_RUN_WORKSPACE_ROOT", Value: agentCfg.workspaceDir},
		{Name: "AGENT_RUN_STATE_ROOT", Value: stateRoot},
		{Name: "AGENT_RUN_OPENCODE_PATH", Value: opencodeBinary},
		// The opencode engine home/XDG data (~/.config/opencode) lives under the
		// container HOME. Keep HOME on the session volume when session persistence
		// is enabled so opencode's own state survives pod restarts alongside the
		// worker's state_root.
		{Name: "KUBEADP_SANDBOX_HOME", Value: homeDir},
		{Name: "AGENT_RUN_API_LISTEN", Value: fmt.Sprintf("0.0.0.0:%d", port)},
	}

	// Build sandbox env allow-list: default to OPENCODE_* plus any explicit credential env names.
	sandboxAllowList := []string{"OPENCODE_*"}
	for _, cred := range agentCfg.credentials {
		if cred.Env != nil && *cred.Env != "" {
			sandboxAllowList = append(sandboxAllowList, *cred.Env)
		}
	}
	envVars = append(envVars, corev1.EnvVar{
		Name:  "KUBEADP_SANDBOX_ENV_ALLOW",
		Value: strings.Join(sandboxAllowList, ","),
	})

	// Set OPENCODE_PERMISSION only if the Agent config does not include custom permissions.
	// When the config has a "permission" field, the user has explicitly configured
	// permission behavior (e.g., "ask" mode for interactive sessions), so we must not override it.
	if !configHasPermission(agentCfg.config) {
		envVars = append(envVars, corev1.EnvVar{
			Name:  OpenCodePermissionEnvVar,
			Value: DefaultOpenCodePermission,
		})
	}

	// Add OpenCode config if provided, or if skills/plugins are configured (injected into config)
	if !configIsEmpty(agentCfg.config) || len(agentCfg.skills) > 0 || hasServerPlugins(agentCfg.plugins) {
		envVars = append(envVars, corev1.EnvVar{
			Name:  OpenCodeConfigEnvVar,
			Value: OpenCodeConfigPath,
		})
	}

	// If TUI plugins are configured, set OPENCODE_TUI_CONFIG env var.
	if hasTUIPlugins(agentCfg.plugins) {
		envVars = append(envVars, corev1.EnvVar{
			Name:  OpenCodeTUIConfigEnvVar,
			Value: OpenCodeTUIConfigPath,
		})
	}

	// Build volume mounts
	volumeMounts := []corev1.VolumeMount{
		{Name: ToolsVolumeName, MountPath: ToolsMountPath},
		{Name: WorkspaceVolumeName, MountPath: agentCfg.workspaceDir},
	}

	// Build volumes
	workspaceVolumeSource := corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{},
	}
	if agent.Spec.Persistence != nil && agent.Spec.Persistence.Workspace != nil {
		workspaceVolumeSource = corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: ServerWorkspacePVCName(agent.Name),
			},
		}
	}

	volumes := []corev1.Volume{
		{
			Name: ToolsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name:         WorkspaceVolumeName,
			VolumeSource: workspaceVolumeSource,
		},
	}

	// Add session persistence volume if configured. The kubeadp-agent worker
	// persists ALL of its state (config.toml, state.sqlite, job
	// metadata/logs, a2a-inbox, opencode serve state) under AGENT_RUN_STATE_ROOT,
	// which is set to ServerSessionMountPath, so the PVC already carries every
	// worker artifact; no separate OPENCODE_DB is needed any longer. HOME is
	// pointed at ServerSessionHomePath on the same PVC so the opencode engine's
	// own data (~/.config/opencode, node_modules, DB) survives restarts too.
	if sessionPersistence {
		volumes = append(volumes, corev1.Volume{
			Name: ServerSessionVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: ServerSessionPVCName(agent.Name),
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      ServerSessionVolumeName,
			MountPath: ServerSessionMountPath,
		})
	}

	// Add kubeadp auth secret volume and mount
	kubeadpSecretVolume := corev1.Volume{
		Name: KubeadpSecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: KubeadpSecretName(agent.Name),
			},
		},
	}
	volumes = append(volumes, kubeadpSecretVolume)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      KubeadpSecretVolumeName,
		MountPath: KubeadpSecretMountPath,
		ReadOnly:  true,
	})

	// Add credentials (secrets as env vars or file mounts)
	credVols, credMounts, credEnvs, credEnvFroms := buildCredentials(agentCfg.credentials)
	volumes = append(volumes, credVols...)
	volumeMounts = append(volumeMounts, credMounts...)
	envVars = append(envVars, credEnvs...)

	// Track init containers. By default the server container runs the
	// kubeadp-agent image, which bundles both kubeadp-agent and the opencode
	// CLI engine, so NO init container is needed. Only when the user explicitly
	// sets executorImage (a custom worker image without the binaries) does a
	// single kubeadp-init container provision /tools with kubeadp-agent,
	// agent-run and opencode, and the worker command reads them from /tools.
	var initContainers []corev1.Container
	if agentCfg.executorImageExplicit {
		initContainers = append(initContainers, buildKubeadpInitContainer(agentCfg.kubeadpImage))
	}

	// Add context init containers and volumes
	var contextInitMounts []corev1.VolumeMount

	// Add context ConfigMap volume if it exists
	if contextConfigMap != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "context-files",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: contextConfigMap.Name,
					},
				},
			},
		})
		contextInitMounts = append(contextInitMounts, corev1.VolumeMount{
			Name:      "context-files",
			MountPath: "/configmap-files",
			ReadOnly:  true,
		})
	}

	// Add directory mounts (ConfigMapRef - entire ConfigMap as a directory)
	for i, dm := range ctxDirMounts {
		volumeName := fmt.Sprintf("dir-mount-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: dm.configMapName,
					},
					Optional: &dm.optional,
				},
			},
		})
		contextInitMounts = append(contextInitMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: fmt.Sprintf("/configmap-dir-%d", i),
			ReadOnly:  true,
		})
	}

	// Add context-init container if there are any files or directories to copy
	if len(ctxFileMounts) > 0 || len(ctxDirMounts) > 0 {
		contextInit := buildContextInitContainer(agentCfg.workspaceDir, ctxFileMounts, ctxDirMounts, sysCfg)
		contextInit.VolumeMounts = append(contextInit.VolumeMounts, contextInitMounts...)
		contextInit.VolumeMounts = append(contextInit.VolumeMounts, corev1.VolumeMount{
			Name:      WorkspaceVolumeName,
			MountPath: agentCfg.workspaceDir,
		})

		// Mount /tools volume so context-init can write config file
		if !configIsEmpty(agentCfg.config) || len(agentCfg.skills) > 0 || hasServerPlugins(agentCfg.plugins) {
			contextInit.VolumeMounts = append(contextInit.VolumeMounts, corev1.VolumeMount{
				Name:      ToolsVolumeName,
				MountPath: ToolsMountPath,
			})
		}

		// Handle files outside workspace
		externalDirs := make(map[string]bool)
		for _, fm := range ctxFileMounts {
			if !isUnderPath(fm.filePath, agentCfg.workspaceDir) {
				parentDir := getParentDir(fm.filePath)
				if parentDir == ToolsMountPath {
					continue
				}
				externalDirs[parentDir] = true
			}
		}
		for dir := range externalDirs {
			volumeName := sanitizeVolumeName(dir)
			volumes = append(volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})
			contextInit.VolumeMounts = append(contextInit.VolumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: dir,
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: dir,
			})
		}

		initContainers = append(initContainers, contextInit)
	}

	// Add Git context mounts (using git-init containers)
	for i, gm := range ctxGitMounts {
		volumeName := fmt.Sprintf("git-context-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})

		isWorkspaceRoot := filepath.Clean(gm.mountPath) == filepath.Clean(agentCfg.workspaceDir)
		gitInitContainer := buildGitInitContainer(gm, volumeName, i, sysCfg)

		if isWorkspaceRoot {
			gitInitContainer.Env = append(gitInitContainer.Env,
				corev1.EnvVar{Name: "GIT_WORKSPACE_DIR", Value: agentCfg.workspaceDir},
			)
			if gm.repoPath != "" {
				gitInitContainer.Env = append(gitInitContainer.Env,
					corev1.EnvVar{Name: "GIT_REPO_SUBPATH", Value: gm.repoPath},
				)
			}
			gitInitContainer.VolumeMounts = append(gitInitContainer.VolumeMounts,
				corev1.VolumeMount{
					Name:      WorkspaceVolumeName,
					MountPath: agentCfg.workspaceDir,
				},
			)
		}

		initContainers = append(initContainers, gitInitContainer)

		if !isWorkspaceRoot {
			baseSubPath := DefaultGitLink
			if gm.repoPath != "" {
				baseSubPath = DefaultGitLink + "/" + strings.TrimPrefix(gm.repoPath, "/")
			}

			if len(gm.names) > 0 {
				// When specific names are set, mount only the named subdirectories
				// instead of the entire directory. This prevents agents from
				// discovering unselected skills in the repository.
				cleanBase := filepath.Clean(baseSubPath)
				for _, name := range gm.names {
					volumeMounts = append(volumeMounts, corev1.VolumeMount{
						Name:      volumeName,
						MountPath: filepath.Join(gm.mountPath, name),
						SubPath:   filepath.Join(cleanBase, name),
					})
				}
			} else {
				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      volumeName,
					MountPath: gm.mountPath,
					SubPath:   baseSubPath,
				})
			}
		}
	}

	// Add plugin-init container and plugins volume if plugins are configured
	if len(agentCfg.plugins) > 0 {
		pluginInit := buildPluginInitContainer(agentCfg.plugins, sysCfg)
		initContainers = append(initContainers, pluginInit)

		volumes = append(volumes, corev1.Volume{
			Name: PluginsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      PluginsVolumeName,
			MountPath: DefaultPluginsMountBase,
			ReadOnly:  true,
		})
	}

	// Inject safe.directory via GIT_CONFIG_COUNT if we have Git mounts.
	// This lets git operate on repositories cloned by init containers that may
	// run as different UIDs (SCC/random-UID environments). We avoid
	// GIT_CONFIG_GLOBAL here so the user's ~/.gitconfig is not masked. See #284.
	if len(ctxGitMounts) > 0 {
		envVars = append(envVars, gitSafeDirectoryEnvVars()...)
	}

	// Check if context file is being mounted and inject OPENCODE_CONFIG_CONTENT
	contextFilePath := agentCfg.workspaceDir + "/" + ContextFileRelPath
	for _, fm := range ctxFileMounts {
		if fm.filePath == contextFilePath {
			envVars = append(envVars, corev1.EnvVar{
				Name:  OpenCodeConfigContentEnvVar,
				Value: `{"instructions":["` + ContextFileRelPath + `"]}`,
			})
			break
		}
	}

	// Add custom CA bundle to all containers if configured
	if agentCfg.caBundle != nil && (agentCfg.caBundle.ConfigMapRef != nil || agentCfg.caBundle.SecretRef != nil) {
		caVolume, caMount, caEnv := buildCABundleVolumeMountEnv(agentCfg.caBundle)
		volumes = append(volumes, caVolume)

		// Add CA bundle mount and env to all init containers
		for i := range initContainers {
			initContainers[i].VolumeMounts = append(initContainers[i].VolumeMounts, caMount)
			initContainers[i].Env = append(initContainers[i].Env, caEnv)
		}

		// Add CA bundle mount and env to the worker container
		volumeMounts = append(volumeMounts, caMount)
		envVars = append(envVars, caEnv)
	}

	// Add HTTP/HTTPS proxy environment variables to all containers if configured
	if agentCfg.proxy != nil {
		proxyEnvs := buildProxyEnvVars(agentCfg.proxy, sysCfg.clusterDomain)
		// Add to all init containers
		for i := range initContainers {
			initContainers[i].Env = append(initContainers[i].Env, proxyEnvs...)
		}
		// Add to worker container env vars
		envVars = append(envVars, proxyEnvs...)
	}

	// Add OpenTelemetry environment variables if observability is configured.
	// For server-mode Agents, the agent name comes from the Agent resource itself.
	if otelEnabled(sysCfg.observability) {
		// Server Deployments generate Pod names at runtime (deploy-hash-random),
		// so k8s.pod.name cannot be hardcoded. Instead, buildOTelEnvVars uses
		// $(OTEL_POD_NAME) in OTEL_RESOURCE_ATTRIBUTES, and we inject the
		// OTEL_POD_NAME env var via the Downward API (fieldRef: metadata.name).
		podNameEnv := corev1.EnvVar{
			Name: OtelPodNameEnvVar,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		}
		// Add Downward API pod name to all containers before OTel env vars
		// so $(OTEL_POD_NAME) in OTEL_RESOURCE_ATTRIBUTES can reference it.
		for i := range initContainers {
			initContainers[i].Env = append(initContainers[i].Env, podNameEnv)
		}
		envVars = append(envVars, podNameEnv)

		injectOTelEnv(sysCfg.observability, otelResourceIDs{
			TaskNamespace: agent.Namespace,
			AgentName:     agent.Name,
		}, initContainers, &envVars)
	}

	// Apply user-defined extraEnv and per-container-type systemContainers overrides.
	// Note: git-sync sidecar overrides are applied separately below (after sidecar construction)
	// because sidecars are not init containers and are only present in Agent Deployments.
	applyExtraEnvAndSystemOverrides(initContainers, &envVars, agentCfg)

	// Build the worker command running kubeadp-agent worker.
	// When context-init handles config file writing, we don't need inline heredoc.
	//
	// By default (no explicit executorImage) the main container IS the
	// kubeadp-agent image: kubeadp-agent and opencode are already in
	// /usr/local/bin, so no /tools copy and no symlink are needed. When an
	// explicit executorImage is used, kubeadp-init provisions /tools with the
	// binaries and the symlink makes /tools/opencode discoverable.
	hasContextInit := len(ctxFileMounts) > 0 || len(ctxDirMounts) > 0
	var homeInitDirs []string
	if sessionPersistence {
		homeInitDirs = append(homeInitDirs, homeDir)
	}
	var command []string
	if !configIsEmpty(agentCfg.config) && !hasContextInit {
		// No context-init container — write config inline in the command
		command = []string{
			"sh", "-c",
			fmt.Sprintf("%s; cat > %s << 'KOCEOF'\n%s\nKOCEOF\n%s",
				initPrefix, OpenCodeConfigPath, string(agentCfg.config.Raw),
				workspaceInitScript(agentCfg.workspaceDir, workerCommand, homeInitDirs...)),
		}
	} else {
		// Config is written by context-init, or no config at all
		command = []string{
			"sh", "-c",
			fmt.Sprintf("%s; %s", initPrefix,
				workspaceInitScript(agentCfg.workspaceDir, workerCommand, homeInitDirs...)),
		}
	}

	// Build the main container
	container := corev1.Container{
		Name:            ServerContainerName,
		Image:           containerImage,
		ImagePullPolicy: inferImagePullPolicy(containerImage),
		WorkingDir:      agentCfg.workspaceDir,
		Command:         command,
		Env:             envVars,
		EnvFrom:         credEnvFroms,
		VolumeMounts:    volumeMounts,
		Ports:           buildContainerPorts(port, agentCfg.extraPorts),
		// StartupProbe gates liveness and readiness probes until the server
		// is fully initialized.
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   ServerStartupHealthPath,
					Port:   intstr.FromInt32(port),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: DefaultStartupPeriodSeconds,
			PeriodSeconds:       DefaultStartupPeriodSeconds,
			TimeoutSeconds:      DefaultProbeTimeoutSeconds,
			FailureThreshold:    DefaultStartupFailureThreshold,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   ServerStartupHealthPath,
					Port:   intstr.FromInt32(port),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			PeriodSeconds:    DefaultLivenessPeriodSeconds,
			TimeoutSeconds:   DefaultProbeTimeoutSeconds,
			FailureThreshold: DefaultProbeFailureThreshold,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   ServerHealthPath,
					Port:   intstr.FromInt32(port),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			PeriodSeconds:    DefaultReadinessPeriodSeconds,
			TimeoutSeconds:   DefaultProbeTimeoutSeconds,
			FailureThreshold: DefaultProbeFailureThreshold,
		},
	}

	// Apply resource requirements - use custom if provided, otherwise use defaults
	if agentCfg.podSpec != nil && agentCfg.podSpec.Resources != nil {
		container.Resources = *agentCfg.podSpec.Resources
	} else {
		container.Resources = defaultResources()
	}

	// Apply security context - use custom if provided, otherwise use restricted default
	if agentCfg.podSpec != nil && agentCfg.podSpec.SecurityContext != nil {
		container.SecurityContext = agentCfg.podSpec.SecurityContext
	} else {
		container.SecurityContext = defaultSecurityContext()
	}

	// Apply lifecycle hooks (e.g., postStart for starting code-server)
	if agentCfg.podSpec != nil && agentCfg.podSpec.Lifecycle != nil {
		container.Lifecycle = agentCfg.podSpec.Lifecycle
	}

	// Apply extra volume mounts to the server container
	if agentCfg.podSpec != nil && len(agentCfg.podSpec.ExtraVolumeMounts) > 0 {
		container.VolumeMounts = append(container.VolumeMounts, agentCfg.podSpec.ExtraVolumeMounts...)
	}

	// Apply default security context to init containers
	for i := range initContainers {
		if initContainers[i].SecurityContext == nil {
			initContainers[i].SecurityContext = defaultSecurityContext()
		}
	}

	// Build git-sync sidecar containers for HotReload policy
	var sidecars []corev1.Container

	// Pre-compute CA and proxy env vars once (shared across all sidecars)
	var sidecarCAMount corev1.VolumeMount
	var sidecarCAEnv corev1.EnvVar
	hasCA := agentCfg.caBundle != nil && (agentCfg.caBundle.ConfigMapRef != nil || agentCfg.caBundle.SecretRef != nil)
	if hasCA {
		_, sidecarCAMount, sidecarCAEnv = buildCABundleVolumeMountEnv(agentCfg.caBundle)
	}
	var proxyEnvs []corev1.EnvVar
	if agentCfg.proxy != nil {
		proxyEnvs = buildProxyEnvVars(agentCfg.proxy, sysCfg.clusterDomain)
	}

	for i, gm := range ctxGitMounts {
		if gm.syncEnabled && gm.syncPolicy == kubeopenv1alpha1.GitSyncPolicyHotReload {
			sidecar := buildGitSyncSidecar(gm, fmt.Sprintf("git-context-%d", i), i, sysCfg)
			if hasCA {
				sidecar.VolumeMounts = append(sidecar.VolumeMounts, sidecarCAMount)
				sidecar.Env = append(sidecar.Env, sidecarCAEnv)
			}
			if len(proxyEnvs) > 0 {
				sidecar.Env = append(sidecar.Env, proxyEnvs...)
			}
			// Apply global extraEnv to git-sync sidecars
			if len(agentCfg.extraEnv) > 0 {
				sidecar.Env = append(sidecar.Env, agentCfg.extraEnv...)
			}
			// Apply per-container git-sync overrides
			if agentCfg.systemContainers != nil {
				applyInitContainerOverrides(&sidecar, agentCfg.systemContainers.GitSync)
			}
			sidecars = append(sidecars, sidecar)
		}
	}

	// Add extra volumes from PodSpec
	if agentCfg.podSpec != nil && len(agentCfg.podSpec.ExtraVolumes) > 0 {
		volumes = append(volumes, agentCfg.podSpec.ExtraVolumes...)
	}

	// Build pod template spec
	containers := []corev1.Container{container}
	containers = append(containers, sidecars...)
	podSpec := corev1.PodSpec{
		ServiceAccountName: agentCfg.serviceAccountName,
		InitContainers:     initContainers,
		Containers:         containers,
		Volumes:            volumes,
		RestartPolicy:      corev1.RestartPolicyAlways,
	}

	// Add imagePullSecrets for private registry authentication
	if len(agentCfg.imagePullSecrets) > 0 {
		podSpec.ImagePullSecrets = agentCfg.imagePullSecrets
	}

	// Apply scheduling configuration if provided
	if agentCfg.podSpec != nil && agentCfg.podSpec.Scheduling != nil {
		scheduling := agentCfg.podSpec.Scheduling
		if scheduling.NodeSelector != nil {
			podSpec.NodeSelector = scheduling.NodeSelector
		}
		if scheduling.Tolerations != nil {
			podSpec.Tolerations = scheduling.Tolerations
		}
		if scheduling.Affinity != nil {
			podSpec.Affinity = scheduling.Affinity
		}
	}

	// Apply runtime class if specified
	if agentCfg.podSpec != nil && agentCfg.podSpec.RuntimeClassName != nil {
		podSpec.RuntimeClassName = agentCfg.podSpec.RuntimeClassName
	}

	// Apply pod-level security context if specified
	if agentCfg.podSpec != nil && agentCfg.podSpec.PodSecurityContext != nil {
		podSpec.SecurityContext = agentCfg.podSpec.PodSecurityContext
	}

	// Single replica for now (simplicity)
	replicas := int32(1)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServerDeploymentName(agent.Name),
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					AgentLabelKey: agent.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: podSpec,
			},
		},
	}
}

// BuildServerService creates a Service for an Agent.
func BuildServerService(agent *kubeopenv1alpha1.Agent) *corev1.Service {
	port := GetServerPort(agent)

	labels := getServerLabels(agent.Name)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServerServiceName(agent.Name),
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				AgentLabelKey: agent.Name,
			},
			Ports: buildServicePorts(port, agent.Spec.ExtraPorts),
		},
	}
}

// buildContainerPorts constructs the container port list for an Agent Deployment.
// It always includes the main OpenCode server port and appends any extra ports.
func buildContainerPorts(serverPort int32, extraPorts []kubeopenv1alpha1.ExtraPort) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{
			Name:          "http",
			ContainerPort: serverPort,
			Protocol:      corev1.ProtocolTCP,
		},
	}
	for _, ep := range extraPorts {
		protocol := ep.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		ports = append(ports, corev1.ContainerPort{
			Name:          ep.Name,
			ContainerPort: ep.Port,
			Protocol:      protocol,
		})
	}
	return ports
}

// buildServicePorts constructs the service port list for an Agent Service.
// It always includes the main OpenCode server port and appends any extra ports.
func buildServicePorts(serverPort int32, extraPorts []kubeopenv1alpha1.ExtraPort) []corev1.ServicePort {
	ports := []corev1.ServicePort{
		{
			Name:       "http",
			Port:       serverPort,
			TargetPort: intstr.FromInt32(serverPort),
			Protocol:   corev1.ProtocolTCP,
		},
	}
	for _, ep := range extraPorts {
		protocol := ep.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		ports = append(ports, corev1.ServicePort{
			Name:       ep.Name,
			Port:       ep.Port,
			TargetPort: intstr.FromInt32(ep.Port),
			Protocol:   protocol,
		})
	}
	return ports
}

// GetServerPort returns the configured port or default.
func GetServerPort(agent *kubeopenv1alpha1.Agent) int32 {
	if agent.Spec.Port != 0 {
		return agent.Spec.Port
	}
	return DefaultServerPort
}
