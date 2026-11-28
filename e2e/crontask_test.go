// Copyright Contributors to the KubeOpenCode project

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeopenv1alpha1 "github.com/kubeopencode/kubeopencode/api/v1alpha1"
)

var _ = Describe("CronTask E2E Tests", Label(LabelCronTask), func() {
	var (
		agent     *kubeopenv1alpha1.Agent
		agentName string
	)

	BeforeEach(func() {
		// Create an Agent with echo executor image for CronTask tests
		agentName = uniqueName("ct-echo")
		agent = &kubeopenv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentName,
				Namespace: testNS,
			},
			Spec: kubeopenv1alpha1.AgentSpec{
				KubeadpImage:       kubeadpImage,
				ExecutorImage:      echoImage,
				ServiceAccountName: testServiceAccount,
				WorkspaceDir:       "/workspace",
				Command:            []string{"sh", "-c", "echo '=== Task Content ===' && find ${WORKSPACE_DIR} -type f -print0 2>/dev/null | sort -z | xargs -0 -I {} sh -c 'echo \"--- File: {} ---\" && cat \"{}\" && echo' && echo '=== Task Completed ==='"},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).Should(Succeed())
	})

	AfterEach(func() {
		// Clean up Agent
		if agent != nil {
			_ = k8sClient.Delete(ctx, agent)
		}
	})

	Context("CronTask creation and status", func() {
		It("should create CronTask with nextScheduleTime set", func() {
			cronTaskName := uniqueName("ct-create")
			description := "Scheduled test task"

			By("Creating a CronTask with a far-future schedule")
			cronTask := &kubeopenv1alpha1.CronTask{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cronTaskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.CronTaskSpec{
					Schedule: "0 0 1 1 *", // Once a year on Jan 1st
					TaskTemplate: kubeopenv1alpha1.TaskTemplateSpec{
						Spec: kubeopenv1alpha1.TaskSpec{
							AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
							Description: &description,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cronTask)).Should(Succeed())

			By("Verifying CronTask has nextScheduleTime set")
			cronTaskKey := types.NamespacedName{Name: cronTaskName, Namespace: testNS}
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Status.NextScheduleTime != nil
			}, timeout, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, cronTask)).Should(Succeed())
		})
	})

	Context("Manual trigger", func() {
		It("should create a child Task when trigger annotation is added", func() {
			cronTaskName := uniqueName("ct-trigger")
			description := "Manually triggered task"

			By("Creating a CronTask with a far-future schedule")
			cronTask := &kubeopenv1alpha1.CronTask{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cronTaskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.CronTaskSpec{
					Schedule: "0 0 1 1 *",
					TaskTemplate: kubeopenv1alpha1.TaskTemplateSpec{
						Spec: kubeopenv1alpha1.TaskSpec{
							AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
							Description: &description,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cronTask)).Should(Succeed())

			By("Waiting for CronTask to be reconciled")
			cronTaskKey := types.NamespacedName{Name: cronTaskName, Namespace: testNS}
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Status.NextScheduleTime != nil
			}, timeout, interval).Should(BeTrue())

			By("Adding trigger annotation")
			Eventually(func() error {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return err
				}
				if ct.Annotations == nil {
					ct.Annotations = make(map[string]string)
				}
				ct.Annotations[kubeopenv1alpha1.CronTaskTriggerAnnotation] = "true"
				return k8sClient.Update(ctx, ct)
			}, timeout, interval).Should(Succeed())

			By("Verifying a child Task is created with crontask label")
			Eventually(func() int {
				tasks := &kubeopenv1alpha1.TaskList{}
				if err := k8sClient.List(ctx, tasks,
					client.InNamespace(testNS),
					client.MatchingLabels{kubeopenv1alpha1.CronTaskLabelKey: cronTaskName}); err != nil {
					return 0
				}
				return len(tasks.Items)
			}, timeout, interval).Should(BeNumerically(">=", 1))

			By("Verifying trigger annotation is removed")
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				_, exists := ct.Annotations[kubeopenv1alpha1.CronTaskTriggerAnnotation]
				return !exists
			}, timeout, interval).Should(BeTrue())

			By("Verifying totalExecutions is incremented")
			Eventually(func() int64 {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return 0
				}
				return ct.Status.TotalExecutions
			}, timeout, interval).Should(BeNumerically(">=", 1))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, cronTask)).Should(Succeed())
		})
	})

	Context("Suspend and resume", func() {
		It("should clear nextScheduleTime when suspended and restore when resumed", func() {
			cronTaskName := uniqueName("ct-suspend")
			description := "Suspend test task"

			By("Creating a CronTask")
			cronTask := &kubeopenv1alpha1.CronTask{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cronTaskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.CronTaskSpec{
					Schedule: "0 0 1 1 *",
					TaskTemplate: kubeopenv1alpha1.TaskTemplateSpec{
						Spec: kubeopenv1alpha1.TaskSpec{
							AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
							Description: &description,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cronTask)).Should(Succeed())

			By("Waiting for nextScheduleTime to be set")
			cronTaskKey := types.NamespacedName{Name: cronTaskName, Namespace: testNS}
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Status.NextScheduleTime != nil
			}, timeout, interval).Should(BeTrue())

			By("Suspending the CronTask")
			Eventually(func() error {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return err
				}
				suspend := true
				ct.Spec.Suspend = &suspend
				return k8sClient.Update(ctx, ct)
			}, timeout, interval).Should(Succeed())

			By("Verifying nextScheduleTime is cleared when suspended")
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Spec.Suspend != nil && *ct.Spec.Suspend && ct.Status.NextScheduleTime == nil
			}, timeout, interval).Should(BeTrue())

			By("Resuming the CronTask")
			Eventually(func() error {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return err
				}
				suspend := false
				ct.Spec.Suspend = &suspend
				return k8sClient.Update(ctx, ct)
			}, timeout, interval).Should(Succeed())

			By("Verifying nextScheduleTime is restored after resume")
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Status.NextScheduleTime != nil
			}, timeout, interval).Should(BeTrue())

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, cronTask)).Should(Succeed())
		})
	})

	Context("OwnerReference cleanup", func() {
		It("should garbage collect child Tasks when CronTask is deleted", func() {
			cronTaskName := uniqueName("ct-owner")
			description := "OwnerRef test task"

			By("Creating a CronTask")
			cronTask := &kubeopenv1alpha1.CronTask{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cronTaskName,
					Namespace: testNS,
				},
				Spec: kubeopenv1alpha1.CronTaskSpec{
					Schedule: "0 0 1 1 *",
					TaskTemplate: kubeopenv1alpha1.TaskTemplateSpec{
						Spec: kubeopenv1alpha1.TaskSpec{
							AgentRef:    &kubeopenv1alpha1.AgentReference{Name: agentName},
							Description: &description,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cronTask)).Should(Succeed())

			By("Waiting for CronTask to be reconciled")
			cronTaskKey := types.NamespacedName{Name: cronTaskName, Namespace: testNS}
			Eventually(func() bool {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return false
				}
				return ct.Status.NextScheduleTime != nil
			}, timeout, interval).Should(BeTrue())

			By("Triggering a child Task")
			Eventually(func() error {
				ct := &kubeopenv1alpha1.CronTask{}
				if err := k8sClient.Get(ctx, cronTaskKey, ct); err != nil {
					return err
				}
				if ct.Annotations == nil {
					ct.Annotations = make(map[string]string)
				}
				ct.Annotations[kubeopenv1alpha1.CronTaskTriggerAnnotation] = "true"
				return k8sClient.Update(ctx, ct)
			}, timeout, interval).Should(Succeed())

			By("Waiting for child Task to be created")
			Eventually(func() int {
				tasks := &kubeopenv1alpha1.TaskList{}
				if err := k8sClient.List(ctx, tasks,
					client.InNamespace(testNS),
					client.MatchingLabels{kubeopenv1alpha1.CronTaskLabelKey: cronTaskName}); err != nil {
					return 0
				}
				return len(tasks.Items)
			}, timeout, interval).Should(BeNumerically(">=", 1))

			By("Verifying child Task has ownerReference to CronTask")
			tasks := &kubeopenv1alpha1.TaskList{}
			Expect(k8sClient.List(ctx, tasks,
				client.InNamespace(testNS),
				client.MatchingLabels{kubeopenv1alpha1.CronTaskLabelKey: cronTaskName})).Should(Succeed())
			Expect(tasks.Items).ShouldNot(BeEmpty())
			childTask := tasks.Items[0]
			Expect(childTask.OwnerReferences).ShouldNot(BeEmpty())
			foundOwner := false
			for _, ref := range childTask.OwnerReferences {
				if ref.Kind == "CronTask" && ref.Name == cronTaskName {
					foundOwner = true
					break
				}
			}
			Expect(foundOwner).Should(BeTrue(), "Child Task should have CronTask as owner")

			By("Deleting the CronTask")
			Expect(k8sClient.Delete(ctx, cronTask)).Should(Succeed())

			By("Verifying child Tasks are garbage collected")
			Eventually(func() int {
				tasks := &kubeopenv1alpha1.TaskList{}
				if err := k8sClient.List(ctx, tasks,
					client.InNamespace(testNS),
					client.MatchingLabels{kubeopenv1alpha1.CronTaskLabelKey: cronTaskName}); err != nil {
					return 0
				}
				return len(tasks.Items)
			}, timeout, interval).Should(Equal(0))
		})
	})
})
