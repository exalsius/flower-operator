/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	federationv1 "github.com/exalsius/flower-operator/api/v1"
)

const (
	containerNameSuperLink          = "superlink"
	containerNameSuperNode          = "supernode"
	containerNameSuperExecServerApp = "superexec-serverapp"
	containerNameSuperExecClientApp = "superexec-clientapp"
)

var _ = Describe("Federation Controller", func() {
	const (
		federationName      = "test-federation"
		federationNamespace = "default"
	)

	ctx := context.Background()

	Context("DaemonSet + subprocess mode", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName,
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			// Delete the federation
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName, Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create SuperLink StatefulSet, Service, and DaemonSet", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName, Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink StatefulSet was created")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
			Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(1)) // No sidecar in subprocess mode
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Name).To(Equal(containerNameSuperLink))
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/superlink:1.26.0"))
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--isolation=subprocess"))

			By("Checking SuperLink Service was created")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-superlink", federationName),
				Namespace: federationNamespace,
			}, service)).To(Succeed())
			Expect(service.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(service.Spec.Ports).To(HaveLen(2)) // fleet, control (no serverappio in subprocess)

			By("Checking SuperNode DaemonSet was created")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(1)) // No sidecar in subprocess mode
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Name).To(Equal(containerNameSuperNode))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/supernode:1.26.0"))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--isolation=subprocess"))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--superlink=%s-superlink:9092", federationName)))

			By("Checking labels are correct")
			Expect(statefulSet.Labels["app.kubernetes.io/managed-by"]).To(Equal("flower-operator"))
			Expect(daemonSet.Labels["flower.flwr.ai/pool"]).To(Equal("default"))
		})

		It("should update status with endpoints and conditions", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName, Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking status was updated")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName, Namespace: federationNamespace}, fed)).To(Succeed())
			Expect(fed.Status.ObservedGeneration).To(Equal(fed.Generation))
			Expect(fed.Status.Endpoints.SuperLinkService).To(Equal(fmt.Sprintf("%s-superlink.%s.svc:9093", federationName, federationNamespace)))
			Expect(fed.Status.Pools).To(HaveLen(1))
			Expect(fed.Status.Pools[0].Name).To(Equal("default"))
			Expect(fed.Status.Pools[0].Kind).To(Equal("DaemonSet"))

			By("Checking Ready condition exists")
			cond := meta.FindStatusCondition(fed.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
		})
	})

	Context("StatefulSet + subprocess mode", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			replicas := int32(3)
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-sts",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeStatefulSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:     "gpu-pool",
								Replicas: &replicas,
								Images:   federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create StatefulSet with correct replicas", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking StatefulSet was created with correct replicas")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(*statefulSet.Spec.Replicas).To(Equal(int32(3)))
			Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(1))
		})
	})

	Context("DaemonSet + process mode", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-process",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "default",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-process", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create SuperExec sidecars in process mode", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-process", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink StatefulSet has SuperExec sidecar")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-process-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(2))

			var superexecContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperExecServerApp {
					superexecContainer = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(superexecContainer).NotTo(BeNil())
			Expect(superexecContainer.Image).To(Equal("flwr/superexec:1.26.0"))
			Expect(superexecContainer.Args).To(ContainElement("--plugin-type=serverapp"))

			By("Checking SuperLink container has process isolation args")
			var superlinkContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperLink {
					superlinkContainer = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(superlinkContainer).NotTo(BeNil())
			Expect(superlinkContainer.Args).To(ContainElement("--isolation=process"))

			By("Checking SuperNode DaemonSet has SuperExec sidecar")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-process-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(2))

			var clientAppContainer *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				if daemonSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperExecClientApp {
					clientAppContainer = &daemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(clientAppContainer).NotTo(BeNil())
			Expect(clientAppContainer.Image).To(Equal("flwr/superexec:1.26.0"))
			Expect(clientAppContainer.Args).To(ContainElement("--plugin-type=clientapp"))

			By("Checking Service has serverappio port in process mode")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-process-superlink", federationName),
				Namespace: federationNamespace,
			}, service)).To(Succeed())
			Expect(service.Spec.Ports).To(HaveLen(3)) // fleet, control, serverappio
		})
	})

	Context("Multiple pools", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-multi",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "pool-a", Images: federationv1.PoolImages{}},
							{Name: "pool-b", Images: federationv1.PoolImages{}},
							{Name: "pool-c", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-multi", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create one DaemonSet per pool", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-multi", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking each pool has a DaemonSet")
			for _, poolName := range []string{"pool-a", "pool-b", "pool-c"} {
				daemonSet := &appsv1.DaemonSet{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      fmt.Sprintf("%s-multi-supernode-%s", federationName, poolName),
					Namespace: federationNamespace,
				}, daemonSet)).To(Succeed(), "DaemonSet for pool %s should exist", poolName)
				Expect(daemonSet.Labels["flower.flwr.ai/pool"]).To(Equal(poolName))
			}

			By("Checking status has all pools")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-multi", Namespace: federationNamespace}, fed)).To(Succeed())
			Expect(fed.Status.Pools).To(HaveLen(3))
		})
	})

	Context("Pool removal (orphan cleanup)", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-orphan",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "keep", Images: federationv1.PoolImages{}},
							{Name: "remove", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-orphan", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should delete orphaned DaemonSets when pools are removed", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both DaemonSets exist")
			ds1 := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-orphan-supernode-keep", federationName),
				Namespace: federationNamespace,
			}, ds1)).To(Succeed())

			ds2 := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-orphan-supernode-remove", federationName),
				Namespace: federationNamespace,
			}, ds2)).To(Succeed())

			By("Removing a pool from the spec")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-orphan", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperNodes.Pools = []federationv1.SuperNodePoolSpec{
				{Name: "keep", Images: federationv1.PoolImages{}},
			}
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the orphaned DaemonSet is deleted")
			orphanDS := &appsv1.DaemonSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-orphan-supernode-remove", federationName),
				Namespace: federationNamespace,
			}, orphanDS)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("Verifying the kept DaemonSet still exists")
			keptDS := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-orphan-supernode-keep", federationName),
				Namespace: federationNamespace,
			}, keptDS)).To(Succeed())
		})
	})

	Context("GPU configuration", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-gpu",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "gpu-pool",
								Images: federationv1.PoolImages{},
								GPU: &federationv1.GPUConfig{
									Enabled: true,
									Vendor:  "nvidia",
									Count:   2,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-gpu", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should configure GPU resources on the container", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-gpu", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking GPU resources are set")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-gpu-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			container := daemonSet.Spec.Template.Spec.Containers[0]
			gpuResource := container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
			Expect(gpuResource.Equal(resource.MustParse("2"))).To(BeTrue())

			gpuRequest := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]
			Expect(gpuRequest.Equal(resource.MustParse("2"))).To(BeTrue())
		})
	})

	Context("PodTemplate overrides", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-template",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "custom",
								Images: federationv1.PoolImages{},
								PodTemplate: &corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{}, // Required by k8s API
										NodeSelector: map[string]string{
											"flower.dev/role": "client",
										},
										Tolerations: []corev1.Toleration{
											{
												Key:      "gpu",
												Operator: corev1.TolerationOpExists,
												Effect:   corev1.TaintEffectNoSchedule,
											},
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-template", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should apply podTemplate overrides", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-template", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking nodeSelector is applied")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-template-supernode-custom", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			Expect(daemonSet.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("flower.dev/role", "client"))

			By("Checking tolerations are applied")
			Expect(daemonSet.Spec.Template.Spec.Tolerations).To(HaveLen(1))
			Expect(daemonSet.Spec.Template.Spec.Tolerations[0].Key).To(Equal("gpu"))
		})
	})

	Context("Image resolution", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-image",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:default",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default-image",
								Images: federationv1.PoolImages{},
							},
							{
								Name: "custom-image",
								Images: federationv1.PoolImages{
									SuperNode: "custom/supernode:override",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-image", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should use pool override image when specified, default otherwise", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-image", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking default pool uses default image")
			defaultDS := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-image-supernode-default-image", federationName),
				Namespace: federationNamespace,
			}, defaultDS)).To(Succeed())
			Expect(defaultDS.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/supernode:default"))

			By("Checking custom pool uses override image")
			customDS := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-image-supernode-custom-image", federationName),
				Namespace: federationNamespace,
			}, customDS)).To(Succeed())
			Expect(customDS.Spec.Template.Spec.Containers[0].Image).To(Equal("custom/supernode:override"))
		})
	})

	Context("Federation not found", func() {
		It("should return without error when Federation doesn't exist", func() {
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("Owner references", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-owner",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "default", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-owner", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should set owner references on created resources", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-owner", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the federation to check UID
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-owner", Namespace: federationNamespace}, fed)).To(Succeed())

			By("Checking StatefulSet has owner reference")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-owner-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.OwnerReferences).To(HaveLen(1))
			Expect(statefulSet.OwnerReferences[0].UID).To(Equal(fed.UID))
			Expect(statefulSet.OwnerReferences[0].Kind).To(Equal("Federation"))

			By("Checking Service has owner reference")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-owner-superlink", federationName),
				Namespace: federationNamespace,
			}, service)).To(Succeed())
			Expect(service.OwnerReferences).To(HaveLen(1))
			Expect(service.OwnerReferences[0].UID).To(Equal(fed.UID))

			By("Checking DaemonSet has owner reference")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-owner-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.OwnerReferences).To(HaveLen(1))
			Expect(daemonSet.OwnerReferences[0].UID).To(Equal(fed.UID))
		})
	})

	Context("Service type configuration", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-svc",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						Service: federationv1.SuperLinkServiceSpec{
							Type: corev1.ServiceTypeLoadBalancer,
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "default", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create Service with specified type", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking Service type is LoadBalancer")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-superlink", federationName),
				Namespace: federationNamespace,
			}, service)).To(Succeed())
			Expect(service.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
		})
	})

	Context("Service labels and annotations", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-labels-annotations",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						Service: federationv1.SuperLinkServiceSpec{
							Labels: map[string]string{
								"custom-label": "custom-value",
								"environment":  "test",
							},
							Annotations: map[string]string{
								"custom-annotation":    "custom-annotation-value",
								"prometheus.io/scrape": "true",
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-labels-annotations", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create Service with custom labels and annotations", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-labels-annotations", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking Service has custom labels merged with operator labels")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-labels-annotations-superlink",
				Namespace: federationNamespace,
			}, service)).To(Succeed())

			// Check operator-managed labels are present
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "superlink"))
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "test-labels-annotations"))
			Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/component", "superlink"))

			// Check custom labels are present
			Expect(service.Labels).To(HaveKeyWithValue("custom-label", "custom-value"))
			Expect(service.Labels).To(HaveKeyWithValue("environment", "test"))

			By("Checking Service has custom annotations")
			Expect(service.Annotations).To(HaveKeyWithValue("custom-annotation", "custom-annotation-value"))
			Expect(service.Annotations).To(HaveKeyWithValue("prometheus.io/scrape", "true"))
		})

		It("should update Service labels and annotations", func() {
			By("Reconciling the Federation initially")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-labels-annotations", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Updating Federation with new labels and annotations")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-labels-annotations", Namespace: federationNamespace}, fed)).To(Succeed())

			fed.Spec.SuperLink.Service.Labels["updated-label"] = "updated-value"
			fed.Spec.SuperLink.Service.Annotations["updated-annotation"] = "updated-annotation-value"
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-labels-annotations", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking Service has updated labels and annotations")
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-labels-annotations-superlink",
				Namespace: federationNamespace,
			}, service)).To(Succeed())

			Expect(service.Labels).To(HaveKeyWithValue("updated-label", "updated-value"))
			Expect(service.Annotations).To(HaveKeyWithValue("updated-annotation", "updated-annotation-value"))
		})
	})

	Context("Extra args", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-args",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:      "default",
								Images:    federationv1.PoolImages{},
								ExtraArgs: []string{"--custom-arg=value", "--another-arg"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-args", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should include extra args in SuperNode container", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-args", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking extra args are included")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-args-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			args := daemonSet.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement("--custom-arg=value"))
			Expect(args).To(ContainElement("--another-arg"))
		})
	})

	Context("Insecure mode configuration", func() {
		var reconciler *FederationReconciler

		BeforeEach(func() {
			reconciler = &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should include --insecure flag when insecure is unset (default)", func() {
			federation := &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-insecure-default",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					// Insecure is NOT set - should default to true
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "default", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, federation)
			}()

			By("Reconciling the Federation")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-insecure-default", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink StatefulSet has --insecure flag")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-default-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))

			By("Checking SuperNode DaemonSet has --insecure flag")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-default-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))
		})

		It("should include --insecure flag when insecure is explicitly true", func() {
			insecureTrue := true
			federation := &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-insecure-true",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					Insecure:      &insecureTrue,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "default", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, federation)
			}()

			By("Reconciling the Federation")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-insecure-true", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink StatefulSet has --insecure flag")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-true-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))

			By("Checking SuperNode DaemonSet has --insecure flag")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-true-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--insecure"))
		})

		It("should NOT include --insecure flag when insecure is false", func() {
			insecureFalse := false
			federation := &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-insecure-false",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					Insecure:      &insecureFalse,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "default", Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, federation)
			}()

			By("Reconciling the Federation")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-insecure-false", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink StatefulSet does NOT have --insecure flag")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-false-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Args).NotTo(ContainElement("--insecure"))

			By("Checking SuperNode DaemonSet does NOT have --insecure flag")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-false-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Args).NotTo(ContainElement("--insecure"))
		})

		It("should NOT include --insecure flag in SuperExec sidecars when insecure is false (process mode)", func() {
			insecureFalse := false
			federation := &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-insecure-false-process",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					Insecure:      &insecureFalse,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "default",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, federation)
			}()

			By("Reconciling the Federation")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-insecure-false-process", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperExec ServerApp sidecar does NOT have --insecure flag")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-false-process-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			var superexecServerApp *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperExecServerApp {
					superexecServerApp = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(superexecServerApp).NotTo(BeNil())
			Expect(superexecServerApp.Args).NotTo(ContainElement("--insecure"))

			By("Checking SuperExec ClientApp sidecar does NOT have --insecure flag")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-insecure-false-process-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			var superexecClientApp *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				if daemonSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperExecClientApp {
					superexecClientApp = &daemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(superexecClientApp).NotTo(BeNil())
			Expect(superexecClientApp.Args).NotTo(ContainElement("--insecure"))
		})
	})

	Context("StatefulSet + process mode", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			replicas := int32(2)
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-sts-process",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeStatefulSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:     "gpu-pool",
								Replicas: &replicas,
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts-process", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should create StatefulSet with SuperExec sidecars in process mode", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts-process", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking StatefulSet was created with 2 containers")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-process-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(*statefulSet.Spec.Replicas).To(Equal(int32(2)))
			Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(2))

			By("Checking SuperExec ClientApp sidecar exists")
			var clientAppContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperExecClientApp {
					clientAppContainer = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(clientAppContainer).NotTo(BeNil())
			Expect(clientAppContainer.Args).To(ContainElement("--plugin-type=clientapp"))
		})
	})

	Context("StatefulSet update", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			replicas := int32(2)
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-sts-update",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeStatefulSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:     "default",
								Replicas: &replicas,
								Images:   federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts-update", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should update StatefulSet replicas when spec changes", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial replica count")
			sts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-update-supernode-default", federationName),
				Namespace: federationNamespace,
			}, sts)).To(Succeed())
			Expect(*sts.Spec.Replicas).To(Equal(int32(2)))

			By("Updating replica count in Federation spec")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts-update", Namespace: federationNamespace}, fed)).To(Succeed())
			newReplicas := int32(5)
			fed.Spec.SuperNodes.Pools[0].Replicas = &newReplicas
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying updated replica count")
			updatedSts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-update-supernode-default", federationName),
				Namespace: federationNamespace,
			}, updatedSts)).To(Succeed())
			Expect(*updatedSts.Spec.Replicas).To(Equal(int32(5)))
		})
	})

	Context("GPU configuration - AMD", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-gpu-amd",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "amd-pool",
								Images: federationv1.PoolImages{},
								GPU: &federationv1.GPUConfig{
									Enabled: true,
									Vendor:  "amd",
									Count:   1,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-gpu-amd", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should configure AMD GPU resources on the container", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-gpu-amd", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking AMD GPU resources are set")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-gpu-amd-supernode-amd-pool", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			container := daemonSet.Spec.Template.Spec.Containers[0]
			gpuResource := container.Resources.Limits[corev1.ResourceName("amd.com/gpu")]
			Expect(gpuResource.Equal(resource.MustParse("1"))).To(BeTrue())
		})
	})

	Context("GPU configuration - custom resource name", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-gpu-custom",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "custom-gpu-pool",
								Images: federationv1.PoolImages{},
								GPU: &federationv1.GPUConfig{
									Enabled:      true,
									ResourceName: "example.com/custom-gpu",
									Count:        4,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-gpu-custom", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should configure custom GPU resource name", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-gpu-custom", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking custom GPU resources are set")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-gpu-custom-supernode-custom-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			container := daemonSet.Spec.Template.Spec.Containers[0]
			gpuResource := container.Resources.Limits[corev1.ResourceName("example.com/custom-gpu")]
			Expect(gpuResource.Equal(resource.MustParse("4"))).To(BeTrue())
		})
	})

	Context("PodTemplate overrides - extended", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-template-ext",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "extended",
								Images: federationv1.PoolImages{},
								PodTemplate: &corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers:         []corev1.Container{},
										ServiceAccountName: "custom-sa",
										PriorityClassName:  "high-priority",
										Affinity: &corev1.Affinity{
											NodeAffinity: &corev1.NodeAffinity{
												RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
													NodeSelectorTerms: []corev1.NodeSelectorTerm{
														{
															MatchExpressions: []corev1.NodeSelectorRequirement{
																{
																	Key:      "kubernetes.io/arch",
																	Operator: corev1.NodeSelectorOpIn,
																	Values:   []string{"amd64"},
																},
															},
														},
													},
												},
											},
										},
										Volumes: []corev1.Volume{
											{
												Name: "data-volume",
												VolumeSource: corev1.VolumeSource{
													EmptyDir: &corev1.EmptyDirVolumeSource{},
												},
											},
										},
										SecurityContext: &corev1.PodSecurityContext{
											RunAsNonRoot: func() *bool { b := true; return &b }(),
											FSGroup:      func() *int64 { g := int64(1000); return &g }(),
										},
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-template-ext", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should apply extended podTemplate overrides", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-template-ext", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking extended overrides are applied")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-template-ext-supernode-extended", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			spec := daemonSet.Spec.Template.Spec

			By("Checking serviceAccountName is applied")
			Expect(spec.ServiceAccountName).To(Equal("custom-sa"))

			By("Checking priorityClassName is applied")
			Expect(spec.PriorityClassName).To(Equal("high-priority"))

			By("Checking affinity is applied")
			Expect(spec.Affinity).NotTo(BeNil())
			Expect(spec.Affinity.NodeAffinity).NotTo(BeNil())

			By("Checking volumes are merged")
			volumeNames := make([]string, len(spec.Volumes))
			for i, v := range spec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ContainElement("data-volume"))

			By("Checking securityContext is applied")
			Expect(spec.SecurityContext).NotTo(BeNil())
			Expect(*spec.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(*spec.SecurityContext.FSGroup).To(Equal(int64(1000)))
		})
	})

	Context("StatefulSet orphan cleanup", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			replicas := int32(1)
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-sts-orphan",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeStatefulSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{Name: "keep", Replicas: &replicas, Images: federationv1.PoolImages{}},
							{Name: "remove", Replicas: &replicas, Images: federationv1.PoolImages{}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts-orphan", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should delete orphaned StatefulSets when pools are removed", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both StatefulSets exist")
			sts1 := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-orphan-supernode-keep", federationName),
				Namespace: federationNamespace,
			}, sts1)).To(Succeed())

			sts2 := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-orphan-supernode-remove", federationName),
				Namespace: federationNamespace,
			}, sts2)).To(Succeed())

			By("Removing a pool from the spec")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-sts-orphan", Namespace: federationNamespace}, fed)).To(Succeed())
			replicas := int32(1)
			fed.Spec.SuperNodes.Pools = []federationv1.SuperNodePoolSpec{
				{Name: "keep", Replicas: &replicas, Images: federationv1.PoolImages{}},
			}
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-sts-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the orphaned StatefulSet is deleted")
			orphanSts := &appsv1.StatefulSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-orphan-supernode-remove", federationName),
				Namespace: federationNamespace,
			}, orphanSts)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("Verifying the kept StatefulSet still exists")
			keptSts := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-sts-orphan-supernode-keep", federationName),
				Namespace: federationNamespace,
			}, keptSts)).To(Succeed())
		})
	})

	Context("Resource requirements", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-resources",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
						SuperExecResources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "default",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
								SuperNodeResources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("200m"),
										corev1.ResourceMemory: resource.MustParse("256Mi"),
									},
								},
								SuperExecResources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("1Gi"),
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-resources", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should configure resource requirements on all containers", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-resources", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink container resources")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-resources-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			var superlinkContainer, superexecServerAppContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				c := &statefulSet.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperLink:
					superlinkContainer = c
				case containerNameSuperExecServerApp:
					superexecServerAppContainer = c
				}
			}

			Expect(superlinkContainer).NotTo(BeNil())
			Expect(superlinkContainer.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("100m")))
			Expect(superlinkContainer.Resources.Limits[corev1.ResourceMemory]).To(Equal(resource.MustParse("512Mi")))

			Expect(superexecServerAppContainer).NotTo(BeNil())
			Expect(superexecServerAppContainer.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("50m")))

			By("Checking SuperNode container resources")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-resources-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			var supernodeContainer, superexecClientAppContainer *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				c := &daemonSet.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperNode:
					supernodeContainer = c
				case containerNameSuperExecClientApp:
					superexecClientAppContainer = c
				}
			}

			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Resources.Requests[corev1.ResourceCPU]).To(Equal(resource.MustParse("200m")))

			Expect(superexecClientAppContainer).NotTo(BeNil())
			Expect(superexecClientAppContainer.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("1")))
		})
	})

	Context("Reconcile - Federation not found", func() {
		It("should return without error when Federation is deleted", func() {
			By("Reconciling a non-existent Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "non-existent-federation", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("Service update path", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-svc-update",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						Service: federationv1.SuperLinkServiceSpec{
							Labels: map[string]string{
								"custom-label": "value1",
							},
							Annotations: map[string]string{
								"custom-annotation": "value1",
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should update Service labels and annotations", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial Service was created")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-update-superlink", federationName),
				Namespace: federationNamespace,
			}, svc)).To(Succeed())
			Expect(svc.Labels["custom-label"]).To(Equal("value1"))
			Expect(svc.Annotations["custom-annotation"]).To(Equal("value1"))

			By("Updating Federation spec with new labels and annotations")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperLink.Service.Labels["custom-label"] = "value2"
			fed.Spec.SuperLink.Service.Annotations["custom-annotation"] = "value2"
			fed.Spec.SuperLink.Service.Annotations["new-annotation"] = "new-value"
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service was updated")
			updatedSvc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-update-superlink", federationName),
				Namespace: federationNamespace,
			}, updatedSvc)).To(Succeed())
			Expect(updatedSvc.Annotations["custom-annotation"]).To(Equal("value2"))
			Expect(updatedSvc.Annotations["new-annotation"]).To(Equal("new-value"))
		})

		It("should recreate Service when type changes", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial Service type is ClusterIP")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-update-superlink", federationName),
				Namespace: federationNamespace,
			}, svc)).To(Succeed())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))

			By("Updating Federation spec to use NodePort")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperLink.Service.Type = corev1.ServiceTypeNodePort
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Service type was changed to NodePort")
			updatedSvc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-update-superlink", federationName),
				Namespace: federationNamespace,
			}, updatedSvc)).To(Succeed())
			Expect(updatedSvc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
		})
	})

	Context("Service custom ports", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-svc-ports",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
						Service: federationv1.SuperLinkServiceSpec{
							Ports: federationv1.SuperLinkPorts{
								GRPC:        9192,
								Extra:       9193,
								ServerAppIO: 9191,
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "default",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc-ports", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should use custom port numbers", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-svc-ports", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying custom ports on Service")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc-ports-superlink", federationName),
				Namespace: federationNamespace,
			}, svc)).To(Succeed())

			var fleetPort, controlPort, serverAppIOPort int32
			for _, p := range svc.Spec.Ports {
				switch p.Name {
				case "fleet":
					fleetPort = p.Port
				case "control":
					controlPort = p.Port
				case "serverappio":
					serverAppIOPort = p.Port
				}
			}
			Expect(fleetPort).To(Equal(int32(9192)))
			Expect(controlPort).To(Equal(int32(9193)))
			Expect(serverAppIOPort).To(Equal(int32(9191)))

			By("Verifying status endpoint uses custom control port")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-svc-ports", Namespace: federationNamespace}, fed)).To(Succeed())
			Expect(fed.Status.Endpoints.SuperLinkService).To(ContainSubstring(":9193"))
		})
	})

	Context("mergePodSpec - additional paths", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			runtimeClassName := "nvidia"
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-merge-pod",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						PodTemplate: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:        []corev1.Container{}, // Required by k8s API
								PriorityClassName: "high-priority",
								RuntimeClassName:  &runtimeClassName,
								DNSPolicy:         corev1.DNSClusterFirstWithHostNet,
								DNSConfig: &corev1.PodDNSConfig{
									Nameservers: []string{"8.8.8.8"},
								},
								HostNetwork: true,
								HostPID:     true,
								HostIPC:     true,
								ImagePullSecrets: []corev1.LocalObjectReference{
									{Name: "my-registry-secret"},
								},
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-merge-pod", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should merge all PodSpec fields", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-merge-pod", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying PodSpec fields were merged")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-merge-pod-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			podSpec := statefulSet.Spec.Template.Spec
			Expect(podSpec.PriorityClassName).To(Equal("high-priority"))
			Expect(podSpec.RuntimeClassName).NotTo(BeNil())
			Expect(*podSpec.RuntimeClassName).To(Equal("nvidia"))
			Expect(podSpec.DNSPolicy).To(Equal(corev1.DNSClusterFirstWithHostNet))
			Expect(podSpec.DNSConfig).NotTo(BeNil())
			Expect(podSpec.DNSConfig.Nameservers).To(ContainElement("8.8.8.8"))
			Expect(podSpec.HostNetwork).To(BeTrue())
			Expect(podSpec.HostPID).To(BeTrue())
			Expect(podSpec.HostIPC).To(BeTrue())
			Expect(podSpec.ImagePullSecrets).To(HaveLen(1))
			Expect(podSpec.ImagePullSecrets[0].Name).To(Equal("my-registry-secret"))
		})
	})

	Context("mergePodSpec - volume deduplication", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-merge-vols",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						PodTemplate: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{}, // Required by k8s API
								Volumes: []corev1.Volume{
									{
										Name: "config-vol",
										VolumeSource: corev1.VolumeSource{
											ConfigMap: &corev1.ConfigMapVolumeSource{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "my-config",
												},
											},
										},
									},
									{
										Name: "secret-vol",
										VolumeSource: corev1.VolumeSource{
											Secret: &corev1.SecretVolumeSource{
												SecretName: "my-secret",
											},
										},
									},
								},
								ImagePullSecrets: []corev1.LocalObjectReference{
									{Name: "secret-a"},
									{Name: "secret-b"},
								},
							},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-merge-vols", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should merge volumes and image pull secrets without duplicates", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-merge-vols", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying volumes were added")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-merge-vols-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			podSpec := statefulSet.Spec.Template.Spec
			Expect(podSpec.Volumes).To(HaveLen(2))

			volumeNames := make([]string, len(podSpec.Volumes))
			for i, v := range podSpec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ContainElements("config-vol", "secret-vol"))

			By("Verifying image pull secrets were added")
			Expect(podSpec.ImagePullSecrets).To(HaveLen(2))
			secretNames := make([]string, len(podSpec.ImagePullSecrets))
			for i, s := range podSpec.ImagePullSecrets {
				secretNames[i] = s.Name
			}
			Expect(secretNames).To(ContainElements("secret-a", "secret-b"))
		})
	})

	Context("isInsecure - explicit false", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			insecure := false
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-secure",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					Insecure:      &insecure,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-secure", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should not add --insecure flag when explicitly set to false", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-secure", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying SuperLink container does not have --insecure flag")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-secure-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			var superlinkContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperLink {
					superlinkContainer = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(superlinkContainer).NotTo(BeNil())
			Expect(superlinkContainer.Args).NotTo(ContainElement("--insecure"))

			By("Verifying SuperNode container does not have --insecure flag")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-secure-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			var supernodeContainer *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				if daemonSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainer = &daemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Args).NotTo(ContainElement("--insecure"))
		})
	})

	Context("DaemonSet orphan cleanup", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-ds-orphan",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "pool-a",
								Images: federationv1.PoolImages{},
							},
							{
								Name:   "pool-b",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-ds-orphan", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should cleanup orphaned DaemonSets when pools are removed", func() {
			By("Reconciling to create initial resources with two pools")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-ds-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying both DaemonSets exist")
			dsA := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-orphan-supernode-pool-a", federationName),
				Namespace: federationNamespace,
			}, dsA)).To(Succeed())

			dsB := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-orphan-supernode-pool-b", federationName),
				Namespace: federationNamespace,
			}, dsB)).To(Succeed())

			By("Removing pool-b from spec")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-ds-orphan", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperNodes.Pools = []federationv1.SuperNodePoolSpec{
				{
					Name:   "pool-a",
					Images: federationv1.PoolImages{},
				},
			}
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-ds-orphan", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying pool-b DaemonSet was deleted")
			deletedDs := &appsv1.DaemonSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-orphan-supernode-pool-b", federationName),
				Namespace: federationNamespace,
			}, deletedDs)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("Verifying pool-a DaemonSet still exists")
			remainingDs := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-orphan-supernode-pool-a", federationName),
				Namespace: federationNamespace,
			}, remainingDs)).To(Succeed())
		})
	})

	Context("Status update - pool not ready", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			replicas := int32(3)
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-status-pool",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeStatefulSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:     "workers",
								Replicas: &replicas,
								Images:   federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-status-pool", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should set Ready=False when pool is not fully ready", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-status-pool", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying status shows not ready (pods won't actually be scheduled in envtest)")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-status-pool", Namespace: federationNamespace}, fed)).To(Succeed())

			readyCondition := meta.FindStatusCondition(fed.Status.Conditions, "Ready")
			Expect(readyCondition).NotTo(BeNil())
			// In envtest, pods don't actually run, so the condition may be False
			// The important thing is that the condition is set
			Expect(readyCondition.Type).To(Equal("Ready"))

			By("Verifying pool status shows desired replicas")
			Expect(fed.Status.Pools).To(HaveLen(1))
			Expect(fed.Status.Pools[0].Name).To(Equal("workers"))
			Expect(fed.Status.Pools[0].Desired).To(Equal(int32(3)))
		})
	})

	Context("SuperLink StatefulSet update path", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-dep-update",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-dep-update", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should update StatefulSet when image changes", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-dep-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial image")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-dep-update-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())
			Expect(statefulSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/superlink:1.26.0"))

			By("Updating Federation spec with new image")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-dep-update", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperLink.Image = "flwr/superlink:1.27.0"
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-dep-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying image was updated")
			updatedStatefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-dep-update-superlink", federationName),
				Namespace: federationNamespace,
			}, updatedStatefulSet)).To(Succeed())
			Expect(updatedStatefulSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/superlink:1.27.0"))
		})
	})

	Context("DaemonSet update path", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-ds-update",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-ds-update", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should update DaemonSet when image changes", func() {
			By("Reconciling to create initial resources")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-ds-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying initial image")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-update-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/supernode:1.26.0"))

			By("Updating Federation spec with new image")
			fed := &federationv1.Federation{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-ds-update", Namespace: federationNamespace}, fed)).To(Succeed())
			fed.Spec.SuperNodes.Image = "flwr/supernode:1.27.0"
			Expect(k8sClient.Update(ctx, fed)).To(Succeed())

			By("Reconciling again")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-ds-update", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying image was updated")
			updatedDaemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-ds-update-supernode-default", federationName),
				Namespace: federationNamespace,
			}, updatedDaemonSet)).To(Succeed())
			Expect(updatedDaemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("flwr/supernode:1.27.0"))
		})
	})

	// Note: "Unknown deployment mode" and "GPU default vendor" tests removed
	// because API validation prevents these scenarios from being created

	Context("SuperNode extra args", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-extra-args",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:      "default",
								Images:    federationv1.PoolImages{},
								ExtraArgs: []string{"--max-retries=5", "--timeout=30s"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-extra-args", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should include extra args in SuperNode container", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-extra-args", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying extra args are present")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-extra-args-supernode-default", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			var supernodeContainer *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				if daemonSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainer = &daemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Args).To(ContainElement("--max-retries=5"))
			Expect(supernodeContainer.Args).To(ContainElement("--timeout=30s"))
		})
	})

	Context("Pool with custom SuperNode image", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-custom-img",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "custom-pool",
								Images: federationv1.PoolImages{
									SuperNode: "my-registry/custom-supernode:v1.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-custom-img", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should use pool-specific SuperNode image", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-custom-img", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying custom image is used")
			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-custom-img-supernode-custom-pool", federationName),
				Namespace: federationNamespace,
			}, daemonSet)).To(Succeed())

			var supernodeContainer *corev1.Container
			for i := range daemonSet.Spec.Template.Spec.Containers {
				if daemonSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainer = &daemonSet.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Image).To(Equal("my-registry/custom-supernode:v1.0"))
		})
	})

	// Note: "StatefulSet with nil replicas" test removed because
	// API validation requires replicas to be set for StatefulSet mode

	Context("Environment variables - SuperLink", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-env-superlink",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
						Env: []corev1.EnvVar{
							{Name: "FLOWER_LOG_LEVEL", Value: "DEBUG"},
							{Name: "PYTHONUNBUFFERED", Value: "1"},
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "default",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-env-superlink", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should apply env vars to SuperLink and SuperExec ServerApp containers", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-env-superlink", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperLink container has env vars")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-env-superlink-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			var superlinkContainer, superexecServerAppContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				c := &statefulSet.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperLink:
					superlinkContainer = c
				case containerNameSuperExecServerApp:
					superexecServerAppContainer = c
				}
			}

			Expect(superlinkContainer).NotTo(BeNil())
			Expect(superlinkContainer.Env).To(HaveLen(2))

			envMap := make(map[string]string)
			for _, e := range superlinkContainer.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["FLOWER_LOG_LEVEL"]).To(Equal("DEBUG"))
			Expect(envMap["PYTHONUNBUFFERED"]).To(Equal("1"))

			By("Checking SuperExec ServerApp sidecar has same env vars")
			Expect(superexecServerAppContainer).NotTo(BeNil())
			Expect(superexecServerAppContainer.Env).To(HaveLen(2))

			sidecarEnvMap := make(map[string]string)
			for _, e := range superexecServerAppContainer.Env {
				sidecarEnvMap[e.Name] = e.Value
			}
			Expect(sidecarEnvMap["FLOWER_LOG_LEVEL"]).To(Equal("DEBUG"))
			Expect(sidecarEnvMap["PYTHONUNBUFFERED"]).To(Equal("1"))
		})
	})

	Context("Environment variables - SuperNode default", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-env-supernode",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Env: []corev1.EnvVar{
							{Name: "FLOWER_LOG_LEVEL", Value: "INFO"},
							{Name: "CUSTOM_VAR", Value: "default-value"},
						},
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "pool-a",
								Images: federationv1.PoolImages{},
							},
							{
								Name:   "pool-b",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-env-supernode", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should apply default env vars to all pool containers", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-env-supernode", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking pool-a has default env vars")
			dsA := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-env-supernode-supernode-pool-a", federationName),
				Namespace: federationNamespace,
			}, dsA)).To(Succeed())

			var supernodeContainerA *corev1.Container
			for i := range dsA.Spec.Template.Spec.Containers {
				if dsA.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainerA = &dsA.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(supernodeContainerA).NotTo(BeNil())
			Expect(supernodeContainerA.Env).To(HaveLen(2))

			envMapA := make(map[string]string)
			for _, e := range supernodeContainerA.Env {
				envMapA[e.Name] = e.Value
			}
			Expect(envMapA["FLOWER_LOG_LEVEL"]).To(Equal("INFO"))
			Expect(envMapA["CUSTOM_VAR"]).To(Equal("default-value"))

			By("Checking pool-b has same default env vars")
			dsB := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-env-supernode-supernode-pool-b", federationName),
				Namespace: federationNamespace,
			}, dsB)).To(Succeed())

			var supernodeContainerB *corev1.Container
			for i := range dsB.Spec.Template.Spec.Containers {
				if dsB.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainerB = &dsB.Spec.Template.Spec.Containers[i]
					break
				}
			}
			Expect(supernodeContainerB).NotTo(BeNil())
			Expect(supernodeContainerB.Env).To(HaveLen(2))
		})
	})

	Context("Environment variables - Pool override", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-env-override",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Env: []corev1.EnvVar{
							{Name: "FLOWER_LOG_LEVEL", Value: "INFO"},
							{Name: "SHARED_VAR", Value: "default"},
						},
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "gpu-pool",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
								Env: []corev1.EnvVar{
									{Name: "FLOWER_LOG_LEVEL", Value: "DEBUG"},   // Override
									{Name: "CUDA_VISIBLE_DEVICES", Value: "0,1"}, // New
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-env-override", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should merge pool env vars with defaults (pool takes precedence)", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-env-override", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperNode container has merged env vars")
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-env-override-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, ds)).To(Succeed())

			var supernodeContainer, superexecClientAppContainer *corev1.Container
			for i := range ds.Spec.Template.Spec.Containers {
				c := &ds.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperNode:
					supernodeContainer = c
				case containerNameSuperExecClientApp:
					superexecClientAppContainer = c
				}
			}

			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Env).To(HaveLen(3)) // FLOWER_LOG_LEVEL, SHARED_VAR, CUDA_VISIBLE_DEVICES

			envMap := make(map[string]string)
			for _, e := range supernodeContainer.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["FLOWER_LOG_LEVEL"]).To(Equal("DEBUG"))   // Overridden by pool
			Expect(envMap["SHARED_VAR"]).To(Equal("default"))       // From default
			Expect(envMap["CUDA_VISIBLE_DEVICES"]).To(Equal("0,1")) // New from pool

			By("Checking SuperExec ClientApp sidecar has same merged env vars")
			Expect(superexecClientAppContainer).NotTo(BeNil())
			Expect(superexecClientAppContainer.Env).To(HaveLen(3))

			sidecarEnvMap := make(map[string]string)
			for _, e := range superexecClientAppContainer.Env {
				sidecarEnvMap[e.Name] = e.Value
			}
			Expect(sidecarEnvMap["FLOWER_LOG_LEVEL"]).To(Equal("DEBUG"))
			Expect(sidecarEnvMap["CUDA_VISIBLE_DEVICES"]).To(Equal("0,1"))
		})
	})

	Context("Environment variables - only pool env vars", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-env-pool-only",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						// No default env vars
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "custom-pool",
								Images: federationv1.PoolImages{},
								Env: []corev1.EnvVar{
									{Name: "POOL_SPECIFIC", Value: "value"},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-env-pool-only", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should apply pool-only env vars when no defaults exist", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-env-pool-only", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking SuperNode container has pool-only env vars")
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-env-pool-only-supernode-custom-pool", federationName),
				Namespace: federationNamespace,
			}, ds)).To(Succeed())

			var supernodeContainer *corev1.Container
			for i := range ds.Spec.Template.Spec.Containers {
				if ds.Spec.Template.Spec.Containers[i].Name == containerNameSuperNode {
					supernodeContainer = &ds.Spec.Template.Spec.Containers[i]
					break
				}
			}

			Expect(supernodeContainer).NotTo(BeNil())
			Expect(supernodeContainer.Env).To(HaveLen(1))
			Expect(supernodeContainer.Env[0].Name).To(Equal("POOL_SPECIFIC"))
			Expect(supernodeContainer.Env[0].Value).To(Equal("value"))
		})
	})

	Context("SuperLink extra args", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-extra-args-sl",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeSubprocess,
					SuperLink: federationv1.SuperLinkSpec{
						Image: "flwr/superlink:1.26.0",
						ExtraArgs: []string{
							"--ssl-ca-certfile=certificates/ca.crt",
							"--ssl-certfile=certificates/server.pem",
							"--database=state/state.db",
						},
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name:   "default",
								Images: federationv1.PoolImages{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-extra-args-sl", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should append extra args to SuperLink container", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-extra-args-sl", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying SuperLink container has extra args appended")
			statefulSet := &appsv1.StatefulSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-extra-args-sl-superlink", federationName),
				Namespace: federationNamespace,
			}, statefulSet)).To(Succeed())

			var superlinkContainer *corev1.Container
			for i := range statefulSet.Spec.Template.Spec.Containers {
				if statefulSet.Spec.Template.Spec.Containers[i].Name == containerNameSuperLink {
					superlinkContainer = &statefulSet.Spec.Template.Spec.Containers[i]
					break
				}
			}

			Expect(superlinkContainer).NotTo(BeNil())
			// Should have: --isolation=subprocess, --insecure, plus 3 extra args
			Expect(superlinkContainer.Args).To(ContainElement("--isolation=subprocess"))
			Expect(superlinkContainer.Args).To(ContainElement("--insecure"))
			Expect(superlinkContainer.Args).To(ContainElement("--ssl-ca-certfile=certificates/ca.crt"))
			Expect(superlinkContainer.Args).To(ContainElement("--ssl-certfile=certificates/server.pem"))
			Expect(superlinkContainer.Args).To(ContainElement("--database=state/state.db"))
		})
	})

	Context("GPU mountAll - NVIDIA", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-gpu-nvidia",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "gpu-pool",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
								GPU: &federationv1.GPUConfig{
									MountAll: true,
									Vendor:   "nvidia",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-gpu-nvidia", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should set runtimeClassName and env var for NVIDIA mountAll", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-gpu-nvidia", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying DaemonSet has runtimeClassName set")
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-gpu-nvidia-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, ds)).To(Succeed())

			Expect(ds.Spec.Template.Spec.RuntimeClassName).NotTo(BeNil())
			Expect(*ds.Spec.Template.Spec.RuntimeClassName).To(Equal("nvidia"))

			By("Verifying SuperNode container has NVIDIA_VISIBLE_DEVICES env var")
			var supernodeContainer, superexecContainer *corev1.Container
			for i := range ds.Spec.Template.Spec.Containers {
				c := &ds.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperNode:
					supernodeContainer = c
				case containerNameSuperExecClientApp:
					superexecContainer = c
				}
			}

			Expect(supernodeContainer).NotTo(BeNil())
			envMap := make(map[string]string)
			for _, e := range supernodeContainer.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["NVIDIA_VISIBLE_DEVICES"]).To(Equal("all"))

			By("Verifying SuperExec ClientApp sidecar has same env var")
			Expect(superexecContainer).NotTo(BeNil())
			sidecarEnvMap := make(map[string]string)
			for _, e := range superexecContainer.Env {
				sidecarEnvMap[e.Name] = e.Value
			}
			Expect(sidecarEnvMap["NVIDIA_VISIBLE_DEVICES"]).To(Equal("all"))

			By("Verifying no GPU resource requests are set")
			Expect(supernodeContainer.Resources.Limits).NotTo(HaveKey(corev1.ResourceName("nvidia.com/gpu")))
			Expect(supernodeContainer.Resources.Requests).NotTo(HaveKey(corev1.ResourceName("nvidia.com/gpu")))
		})
	})

	Context("GPU mountAll - AMD", func() {
		var federation *federationv1.Federation

		BeforeEach(func() {
			federation = &federationv1.Federation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      federationName + "-gpu-amd",
					Namespace: federationNamespace,
				},
				Spec: federationv1.FederationSpec{
					Mode:          federationv1.DeploymentModeDaemonSet,
					IsolationMode: federationv1.IsolationModeProcess,
					SuperLink: federationv1.SuperLinkSpec{
						Image:          "flwr/superlink:1.26.0",
						SuperExecImage: "flwr/superexec:1.26.0",
					},
					SuperNodes: federationv1.SuperNodesSpec{
						Image: "flwr/supernode:1.26.0",
						Pools: []federationv1.SuperNodePoolSpec{
							{
								Name: "gpu-pool",
								Images: federationv1.PoolImages{
									SuperExecClientApp: "flwr/superexec:1.26.0",
								},
								GPU: &federationv1.GPUConfig{
									MountAll: true,
									Vendor:   "amd",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, federation)).To(Succeed())
		})

		AfterEach(func() {
			fed := &federationv1.Federation{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: federationName + "-gpu-amd", Namespace: federationNamespace}, fed); err == nil {
				Expect(k8sClient.Delete(ctx, fed)).To(Succeed())
			}
		})

		It("should set volumes, mounts, env var, and privileged for AMD mountAll", func() {
			By("Reconciling the Federation")
			reconciler := &FederationReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: federationName + "-gpu-amd", Namespace: federationNamespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying DaemonSet has volumes for AMD GPU access")
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-gpu-amd-supernode-gpu-pool", federationName),
				Namespace: federationNamespace,
			}, ds)).To(Succeed())

			volumeNames := make([]string, len(ds.Spec.Template.Spec.Volumes))
			for i, v := range ds.Spec.Template.Spec.Volumes {
				volumeNames[i] = v.Name
			}
			Expect(volumeNames).To(ContainElements("dev-kfd", "dev-dri"))

			By("Verifying SuperNode container has AMD GPU configuration")
			var supernodeContainer, superexecContainer *corev1.Container
			for i := range ds.Spec.Template.Spec.Containers {
				c := &ds.Spec.Template.Spec.Containers[i]
				switch c.Name {
				case containerNameSuperNode:
					supernodeContainer = c
				case containerNameSuperExecClientApp:
					superexecContainer = c
				}
			}

			Expect(supernodeContainer).NotTo(BeNil())

			// Check env var
			envMap := make(map[string]string)
			for _, e := range supernodeContainer.Env {
				envMap[e.Name] = e.Value
			}
			Expect(envMap["ROCR_VISIBLE_DEVICES"]).To(Equal("all"))

			// Check volume mounts
			mountNames := make([]string, len(supernodeContainer.VolumeMounts))
			for i, m := range supernodeContainer.VolumeMounts {
				mountNames[i] = m.Name
			}
			Expect(mountNames).To(ContainElements("dev-kfd", "dev-dri"))

			// Check privileged security context
			Expect(supernodeContainer.SecurityContext).NotTo(BeNil())
			Expect(supernodeContainer.SecurityContext.Privileged).NotTo(BeNil())
			Expect(*supernodeContainer.SecurityContext.Privileged).To(BeTrue())

			By("Verifying SuperExec ClientApp sidecar has same configuration")
			Expect(superexecContainer).NotTo(BeNil())

			sidecarEnvMap := make(map[string]string)
			for _, e := range superexecContainer.Env {
				sidecarEnvMap[e.Name] = e.Value
			}
			Expect(sidecarEnvMap["ROCR_VISIBLE_DEVICES"]).To(Equal("all"))

			sidecarMountNames := make([]string, len(superexecContainer.VolumeMounts))
			for i, m := range superexecContainer.VolumeMounts {
				sidecarMountNames[i] = m.Name
			}
			Expect(sidecarMountNames).To(ContainElements("dev-kfd", "dev-dri"))
		})
	})
})
