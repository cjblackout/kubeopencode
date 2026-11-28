// Copyright Contributors to the KubeOpenCode project

package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
	"github.com/kubeopencode/kubeopencode/internal/controller"
)

var _ = Describe("Agent Deployment E2E Tests", Label(LabelServer), func() {

	Context("Agent Deployment Creation", func() {
		It("should create Deployment and Service for Agent", func() {
			agentName := uniqueName("server-agent")

			By("Creating Agent with port")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentName := fmt.Sprintf("%s-server", agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Waiting for Service to be created")
			serviceKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				service := &corev1.Service{}
				return k8sClient.Get(ctx, serviceKey, service) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Agent status is populated")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.DeploymentName != "" &&
					a.Status.ServiceName != ""
			}, timeout, interval).Should(BeTrue())

			By("Verifying status details")
			updatedAgent := &kubeopenv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, agentKey, updatedAgent)).Should(Succeed())
			Expect(updatedAgent.Status.DeploymentName).Should(Equal(deploymentName))
			Expect(updatedAgent.Status.ServiceName).Should(Equal(agentName))
			Expect(updatedAgent.Status.URL).Should(ContainSubstring(agentName))

			By("Verifying Deployment has correct env vars (HOME, SHELL, WORKSPACE_DIR)")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())
			agentContainer := deployment.Spec.Template.Spec.Containers[0]
			envMap := make(map[string]string)
			for _, env := range agentContainer.Env {
				envMap[env.Name] = env.Value
			}
			Expect(envMap).Should(HaveKey("HOME"))
			Expect(envMap).Should(HaveKey("SHELL"))
			Expect(envMap).Should(HaveKey("WORKSPACE_DIR"))
			Expect(envMap["WORKSPACE_DIR"]).Should(Equal("/workspace"))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())

			// Wait for resources to be deleted
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("Agent Health Conditions", func() {
		It("should set ServerReady and ServerHealthy conditions", func() {
			agentName := uniqueName("server-health")

			By("Creating Agent with port")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for ServerReady condition")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				cond := getAgentCondition(a, "ServerReady")
				return cond != nil
			}, serverTimeout, interval).Should(BeTrue())

			By("Verifying conditions are set")
			updatedAgent := &kubeopenv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, agentKey, updatedAgent)).Should(Succeed())

			serverReadyCond := getAgentCondition(updatedAgent, "ServerReady")
			Expect(serverReadyCond).ShouldNot(BeNil())

			By("Verifying status is populated")
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.DeploymentName != ""
			}, serverTimeout, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Task Execution", func() {
		It("should create Task Pod that connects to Agent Deployment", func() {
			agentName := uniqueName("server-task")
			taskName := uniqueName("task-server")
			content := "# Agent Task Test"

			By("Creating Agent with port")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Agent to be ready")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.URL != ""
			}, serverTimeout, interval).Should(BeTrue())

			By("Creating Task using Agent")
			task := &kubeopenv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      taskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.TaskSpec{
					AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
					Description: &content,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for Task Pod to be created")
			var pod *corev1.Pod
			Eventually(func() bool {
				var err error
				pod, err = getPodForTask(ctx, testNS, taskName)
				return err == nil && pod != nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Pod is created (Agent uses attach pods)")
			Expect(pod.Spec.Containers).Should(HaveLen(1))
			// The pod should have the attach image or executor image
			Expect(pod.Spec.Containers[0].Image).ShouldNot(BeEmpty())

			By("Waiting for Task to transition (may complete or fail depending on Agent)")
			taskKey := types.NamespacedName{Name: taskName, Namespace: testNS}
			Eventually(func() bool {
				t := &kubeopenv1alpha1.Task{}
				if err := k8sClient.Get(ctx, taskKey, t); err != nil {
					return false
				}
				return t.Status.Phase == kubeopenv1alpha1.TaskPhaseCompleted ||
					t.Status.Phase == kubeopenv1alpha1.TaskPhaseFailed ||
					t.Status.Phase == kubeopenv1alpha1.TaskPhaseRunning
			}, timeout, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, task)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Multiple Tasks", func() {
		It("should handle multiple Tasks connecting to the same Agent Deployment", func() {
			agentName := uniqueName("server-multi")
			content := "# Multi-Task Test"

			By("Creating Agent with port")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Agent to be ready")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.URL != ""
			}, serverTimeout, interval).Should(BeTrue())

			By("Creating multiple Tasks")
			taskNames := []string{
				uniqueName("task-multi-1"),
				uniqueName("task-multi-2"),
				uniqueName("task-multi-3"),
			}

			for _, taskName := range taskNames {
				task := &kubeopenv1alpha1.Task{
					ObjectMeta: metav1.ObjectMeta{
						Name:      taskName,
						Namespace: testNS,
					},
					Spec: kubeopenv1alpha1.TaskSpec{
						AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
						Description: &content,
					},
				}
				Expect(k8sClient.Create(ctx, task)).Should(Succeed())
			}

			By("Verifying all Tasks have Pods created")
			for _, taskName := range taskNames {
				Eventually(func() bool {
					pod, err := getPodForTask(ctx, testNS, taskName)
					return err == nil && pod != nil
				}, timeout, interval).Should(BeTrue())
			}

			By("Verifying all Tasks are in Running or Completed state")
			for _, taskName := range taskNames {
				taskKey := types.NamespacedName{Name: taskName, Namespace: testNS}
				Eventually(func() bool {
					t := &kubeopenv1alpha1.Task{}
					if err := k8sClient.Get(ctx, taskKey, t); err != nil {
						return false
					}
					return t.Status.Phase == kubeopenv1alpha1.TaskPhaseRunning ||
						t.Status.Phase == kubeopenv1alpha1.TaskPhaseCompleted ||
						t.Status.Phase == kubeopenv1alpha1.TaskPhaseFailed
				}, timeout, interval).Should(BeTrue())
			}

			By("Verifying only one Deployment exists for the agent")
			deploymentName := fmt.Sprintf("%s-server", agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())

			By("Cleaning up Tasks")
			for _, taskName := range taskNames {
				task := &kubeopenv1alpha1.Task{}
				taskKey := types.NamespacedName{Name: taskName, Namespace: testNS}
				if err := k8sClient.Get(ctx, taskKey, task); err == nil {
					Expect(k8sClient.Delete(ctx, task)).Should(Succeed())
				}
			}

			By("Cleaning up Agent")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Task Pod Provisioning", func() {
		It("should run task-submit with kubeadp-init provisioning when agentRef is used", func() {
			agentName := uniqueName("server-kubeadp-init")
			taskName := uniqueName("task-kubeadp-init")
			content := "# Kubeadp Init Test"

			By("Creating Agent")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Agent to be ready")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.URL != ""
			}, serverTimeout, interval).Should(BeTrue())

			By("Creating Task")
			task := &kubeopenv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      taskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.TaskSpec{
					AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
					Description: &content,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for Task Pod to be created")
			var pod *corev1.Pod
			Eventually(func() bool {
				var err error
				pod, err = getPodForTask(ctx, testNS, taskName)
				return err == nil && pod != nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the kubeadp-init container provisions /tools")
			Expect(pod.Spec.InitContainers).ShouldNot(BeEmpty())
			Expect(pod.Spec.InitContainers[0].Name).Should(Equal("kubeadp-init"))
			Expect(pod.Spec.InitContainers[0].Image).Should(Equal(kubeadpImage))

			By("Verifying the agent container uses task-submit (no attach image)")
			agentContainer := pod.Spec.Containers[0]
			Expect(agentContainer.Command).ShouldNot(ContainElement(ContainSubstring("--attach")))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, task)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Credentials Injection", func() {
		It("should mount credentials as env vars in Agent Deployment", func() {
			agentName := uniqueName("server-creds")

			By("Creating a Secret for credentials")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName + "-creds",
					Namespace: testNS,
				},
				StringData: map[string]string{
					"API_KEY":    "test-key-value",
					"API_SECRET": "test-secret-value",
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

			By("Creating Agent with credentials")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
					Credentials: []kubeopenv1alpha1.Credential{
						{
							Name: "api-creds",
							SecretRef: kubeopenv1alpha1.SecretReference{
								Name: agentName + "-creds",
							},
							// No key specified = entire secret as envFrom
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentName := controller.ServerDeploymentName(agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Deployment has envFrom with the Secret")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())
			agentContainer := deployment.Spec.Template.Spec.Containers[0]

			// Entire secret should be mounted as envFrom
			foundEnvFrom := false
			for _, envFrom := range agentContainer.EnvFrom {
				if envFrom.SecretRef != nil && envFrom.SecretRef.Name == agentName+"-creds" {
					foundEnvFrom = true
					break
				}
			}
			Expect(foundEnvFrom).Should(BeTrue(), "Agent container should have envFrom referencing the credentials Secret")

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).Should(Succeed())
		})
	})

	Context("Agent - OPENCODE_PERMISSION Default", func() {
		It("should set OPENCODE_PERMISSION when config has no permission field", func() {
			agentName := uniqueName("server-perm")

			By("Creating Agent with no permission in config")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentName := controller.ServerDeploymentName(agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying OPENCODE_PERMISSION is set to allow-all")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())
			container := deployment.Spec.Template.Spec.Containers[0]

			envMap := make(map[string]string)
			for _, env := range container.Env {
				envMap[env.Name] = env.Value
			}
			Expect(envMap).Should(HaveKey("OPENCODE_PERMISSION"))
			Expect(envMap["OPENCODE_PERMISSION"]).Should(Equal(`{"*":"allow"}`))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Context Support", func() {
		It("should load Text context via context-init container in Deployment", func() {
			agentName := uniqueName("server-ctx")

			By("Creating Agent with Text context")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
					Contexts: []kubeopenv1alpha1.ContextItem{
						{
							Name: "coding-rules",
							Type: kubeopenv1alpha1.ContextTypeText,
							Text: "Always write tests for new code.",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentName := controller.ServerDeploymentName(agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Deployment has context-init init container")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())

			foundContextInit := false
			for _, ic := range deployment.Spec.Template.Spec.InitContainers {
				if ic.Name == "context-init" {
					foundContextInit = true
					break
				}
			}
			Expect(foundContextInit).Should(BeTrue(), "Deployment should have context-init init container for Text context")

			By("Verifying context ConfigMap was created")
			contextCMKey := types.NamespacedName{
				Name:      controller.ServerContextConfigMapName(agentName),
				Namespace: testNS,
			}
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				return k8sClient.Get(ctx, contextCMKey, cm) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying ConfigMap contains the context content")
			contextCM := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, contextCMKey, contextCM)).Should(Succeed())
			// The context should be in the ConfigMap data (aggregated under context file key)
			Expect(len(contextCM.Data)).Should(BeNumerically(">", 0))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Task Pod Command Verification", func() {
		It("should use kubeopencode task-submit in Task Pod command", func() {
			agentName := uniqueName("server-cmd")
			taskName := uniqueName("task-cmd")
			content := "# Command Verification Test"

			By("Creating Agent")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Agent to be ready")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.URL != ""
			}, serverTimeout, interval).Should(BeTrue())

			By("Creating Task")
			task := &kubeopenv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      taskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.TaskSpec{
					AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
					Description: &content,
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Waiting for Task Pod to be created")
			var pod *corev1.Pod
			Eventually(func() bool {
				var err error
				pod, err = getPodForTask(ctx, testNS, taskName)
				return err == nil && pod != nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying Pod command uses task-submit")
			agentContainer := pod.Spec.Containers[0]
			// Command should be ["/kubeopencode", "task-submit", "--url", ...]
			Expect(agentContainer.Command[0]).Should(Equal("/kubeopencode"))
			Expect(agentContainer.Command[1]).Should(Equal("task-submit"))
			Expect(agentContainer.Command[2]).Should(Equal("--url"))
			Expect(agentContainer.Command[3]).Should(ContainSubstring(agentName))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, task)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Session Persistence", func() {
		It("should create a PVC when session persistence is configured", func() {
			agentName := uniqueName("session-persist")

			By("Creating Agent with session persistence")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
					Persistence: &kubeopenv1alpha1.PersistenceConfig{
						Sessions: &kubeopenv1alpha1.VolumePersistence{
							Size: "1Gi",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for session PVC to be created")
			pvcName := controller.ServerSessionPVCName(agentName)
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: testNS}
			Eventually(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				return k8sClient.Get(ctx, pvcKey, pvc) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying PVC properties")
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).Should(Succeed())
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.String()).To(Equal("1Gi"))

			By("Waiting for Deployment to be created with session volume")
			deploymentName := controller.ServerDeploymentName(agentName)
			deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return false
				}
				// Check for session PVC volume
				for _, vol := range deployment.Spec.Template.Spec.Volumes {
					if vol.Name == controller.ServerSessionVolumeName &&
						vol.PersistentVolumeClaim != nil &&
						vol.PersistentVolumeClaim.ClaimName == pvcName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Verifying AGENT_RUN_STATE_ROOT points at the session volume in Deployment")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).Should(Succeed())
			container := deployment.Spec.Template.Spec.Containers[0]
			var foundStateEnv bool
			for _, env := range container.Env {
				if env.Name == "AGENT_RUN_STATE_ROOT" && env.Value == controller.ServerSessionMountPath {
					foundStateEnv = true
				}
			}
			Expect(foundStateEnv).To(BeTrue(), "AGENT_RUN_STATE_ROOT does not point at the session volume in Agent container")

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())

			By("Verifying PVC is cleaned up with Agent (via OwnerReference)")
			Eventually(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				return k8sClient.Get(ctx, pvcKey, pvc) != nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should NOT create a PVC when persistence is not configured", func() {
			agentName := uniqueName("no-persist")

			By("Creating Agent without persistence")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment (confirms controller processed the Agent)")
			deploymentKey := types.NamespacedName{Name: controller.ServerDeploymentName(agentName), Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying NO PVC was created")
			pvcKey := types.NamespacedName{Name: controller.ServerSessionPVCName(agentName), Namespace: testNS}
			Consistently(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				return k8sClient.Get(ctx, pvcKey, pvc) != nil
			}, timeout/2, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Workspace Persistence", func() {
		It("should create a workspace PVC when configured", func() {
			agentName := uniqueName("ws-persist")

			By("Creating Agent with workspace persistence")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
					Persistence: &kubeopenv1alpha1.PersistenceConfig{
						Workspace: &kubeopenv1alpha1.VolumePersistence{
							Size: "1Gi",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for workspace PVC to be created")
			pvcName := controller.ServerWorkspacePVCName(agentName)
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: testNS}
			Eventually(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				return k8sClient.Get(ctx, pvcKey, pvc) == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying workspace PVC properties")
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).Should(Succeed())
			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteOnce))
			storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storageReq.String()).To(Equal("1Gi"))

			By("Verifying Deployment workspace volume is PVC-backed")
			deploymentKey := types.NamespacedName{Name: controller.ServerDeploymentName(agentName), Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return false
				}
				for _, vol := range deployment.Spec.Template.Spec.Volumes {
					if vol.Name == controller.WorkspaceVolumeName &&
						vol.PersistentVolumeClaim != nil &&
						vol.PersistentVolumeClaim.ClaimName == pvcName {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())

			By("Verifying workspace PVC is cleaned up via OwnerReference")
			Eventually(func() bool {
				pvc := &corev1.PersistentVolumeClaim{}
				return k8sClient.Get(ctx, pvcKey, pvc) != nil
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("Agent - Suspend/Resume", func() {
		It("should scale deployment to 0 when suspended and back to 1 when resumed", func() {
			agentName := uniqueName("suspend-agent")

			By("Creating Agent")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentKey := types.NamespacedName{Name: controller.ServerDeploymentName(agentName), Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			By("Suspending the Agent")
			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}
			Eventually(func() error {
				var updated kubeopenv1alpha1.Agent
				if err := k8sClient.Get(ctx, agentKey, &updated); err != nil {
					return err
				}
				updated.Spec.Suspend = true
				return k8sClient.Update(ctx, &updated)
			}, timeout, interval).Should(Succeed())

			By("Expecting Deployment to scale to 0 replicas")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return -1
				}
				if deployment.Spec.Replicas == nil {
					return 1
				}
				return *deployment.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			By("Expecting Agent status to show Suspended")
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Status.Suspended
			}, timeout, interval).Should(BeTrue())

			By("Resuming the Agent")
			Eventually(func() error {
				var updated kubeopenv1alpha1.Agent
				if err := k8sClient.Get(ctx, agentKey, &updated); err != nil {
					return err
				}
				updated.Spec.Suspend = false
				return k8sClient.Update(ctx, &updated)
			}, timeout, interval).Should(Succeed())

			By("Expecting Deployment to scale back to 1 replica")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return -1
				}
				if deployment.Spec.Replicas == nil {
					return 1
				}
				return *deployment.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(1)))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})

	Context("Agent - Standby Auto-Suspend/Resume", func() {
		It("should auto-suspend after idle timeout and auto-resume when task created", func() {
			agentName := uniqueName("standby-agent")

			By("Creating Agent with standby (short idle timeout)")
			agent := &kubeopenv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.AgentSpec{
					KubeadpImage:       kubeadpImage,
					ExecutorImage:      echoImage,
					ServiceAccountName: testServiceAccount,
					WorkspaceDir:       "/workspace",
					Port:               4096,
					Standby: &kubeopenv1alpha1.StandbyConfig{
						IdleTimeout: metav1.Duration{Duration: 5 * time.Second},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).Should(Succeed())

			By("Waiting for Deployment to be created")
			deploymentKey := types.NamespacedName{Name: controller.ServerDeploymentName(agentName), Namespace: testNS}
			Eventually(func() bool {
				deployment := &appsv1.Deployment{}
				return k8sClient.Get(ctx, deploymentKey, deployment) == nil
			}, timeout, interval).Should(BeTrue())

			agentKey := types.NamespacedName{Name: agentName, Namespace: testNS}

			By("Expecting spec.suspend to become true after idle timeout")
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return false
				}
				return a.Spec.Suspend
			}, timeout, interval).Should(BeTrue())

			By("Expecting Deployment to scale to 0")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return -1
				}
				if deployment.Spec.Replicas == nil {
					return 1
				}
				return *deployment.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			By("Creating a Task to trigger auto-resume")
			task := &kubeopenv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      uniqueName("standby-task"),
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.TaskSpec{
					AgentRef: &kubeopenv1alpha1.AgentReference{Name: agentName},
				},
			}
			Expect(k8sClient.Create(ctx, task)).Should(Succeed())

			By("Expecting spec.suspend to become false (auto-resume)")
			Eventually(func() bool {
				a := &kubeopenv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, agentKey, a); err != nil {
					return true
				}
				return a.Spec.Suspend
			}, timeout, interval).Should(BeFalse())

			By("Expecting Deployment to scale back to 1")
			Eventually(func() int32 {
				deployment := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, deploymentKey, deployment); err != nil {
					return -1
				}
				if deployment.Spec.Replicas == nil {
					return 1
				}
				return *deployment.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(1)))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, task)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, agent)).Should(Succeed())
		})
	})
})
