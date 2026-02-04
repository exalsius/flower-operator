//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/exalsius/flower-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "flower-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "flower-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "flower-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "flower-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("cleaning up the metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("ensuring any existing ClusterRoleBinding is removed")
			cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=flower-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("Federation", Ordered, func() {
		const federationTestNamespace = "federation-e2e-test"

		BeforeAll(func() {
			By("creating federation test namespace")
			cmd := exec.Command("kubectl", "create", "ns", federationTestNamespace)
			_, _ = utils.Run(cmd) // Ignore error if namespace exists
		})

		AfterAll(func() {
			By("deleting federation test namespace")
			cmd := exec.Command("kubectl", "delete", "ns", federationTestNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		AfterEach(func() {
			By("cleaning up federations in test namespace")
			cmd := exec.Command("kubectl", "delete", "federations", "--all", "-n", federationTestNamespace)
			_, _ = utils.Run(cmd)
			// Wait for cleanup
			time.Sleep(5 * time.Second)
		})

		It("should create DaemonSet for subprocess isolation mode", func() {
			By("applying Federation CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-daemonset-subprocess.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-daemonset-subprocess-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperLink Service is created")
			cmd = exec.Command("kubectl", "get", "service",
				"federation-daemonset-subprocess-superlink", "-n", federationTestNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperNode DaemonSet is created")
			verifyDaemonSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-daemonset-subprocess-supernode-default", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDaemonSet, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying DaemonSet has 1 container (subprocess mode)")
			verifyContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-daemonset-subprocess-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				containers := strings.Fields(output)
				g.Expect(containers).To(HaveLen(1))
				g.Expect(containers[0]).To(Equal("supernode"))
			}
			Eventually(verifyContainers, time.Minute, time.Second).Should(Succeed())
		})

		It("should create DaemonSet with SuperExec sidecar for process mode", func() {
			By("applying Federation CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-daemonset-process.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-daemonset-process-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperLink Deployment has 2 containers (superlink + superexec-serverapp)")
			verifySuperlinkContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-daemonset-process-superlink", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				containers := strings.Fields(output)
				g.Expect(containers).To(HaveLen(2))
				g.Expect(containers).To(ContainElements("superlink", "superexec-serverapp"))
			}
			Eventually(verifySuperlinkContainers, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode DaemonSet is created")
			verifyDaemonSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-daemonset-process-supernode-default", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDaemonSet, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying DaemonSet has 2 containers (supernode + superexec-clientapp)")
			verifyContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-daemonset-process-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				containers := strings.Fields(output)
				g.Expect(containers).To(HaveLen(2))
				g.Expect(containers).To(ContainElements("supernode", "superexec-clientapp"))
			}
			Eventually(verifyContainers, time.Minute, time.Second).Should(Succeed())
		})

		It("should create StatefulSet for subprocess isolation mode", func() {
			By("applying Federation CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-statefulset-subprocess.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-statefulset-subprocess-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode StatefulSet is created")
			verifyStatefulSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					"federation-statefulset-subprocess-supernode-default", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyStatefulSet, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying StatefulSet has 2 replicas in spec")
			verifyReplicas := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					"federation-statefulset-subprocess-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.replicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("2"))
			}
			Eventually(verifyReplicas, time.Minute, time.Second).Should(Succeed())

			By("verifying StatefulSet has 1 container (subprocess mode)")
			verifyContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					"federation-statefulset-subprocess-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				containers := strings.Fields(output)
				g.Expect(containers).To(HaveLen(1))
				g.Expect(containers[0]).To(Equal("supernode"))
			}
			Eventually(verifyContainers, time.Minute, time.Second).Should(Succeed())
		})

		It("should create StatefulSet with SuperExec sidecar for process mode", func() {
			By("applying Federation CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-statefulset-process.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-statefulset-process-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode StatefulSet is created")
			verifyStatefulSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					"federation-statefulset-process-supernode-default", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyStatefulSet, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying StatefulSet has 2 containers (supernode + superexec-clientapp)")
			verifyContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "statefulset",
					"federation-statefulset-process-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				containers := strings.Fields(output)
				g.Expect(containers).To(HaveLen(2))
				g.Expect(containers).To(ContainElements("supernode", "superexec-clientapp"))
			}
			Eventually(verifyContainers, time.Minute, time.Second).Should(Succeed())
		})

		It("should create multiple DaemonSets for multiple pools", func() {
			By("applying Federation CR with multiple pools")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-multipools.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-multipools-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying DaemonSet for pool-a is created")
			verifyPoolA := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-multipools-supernode-pool-a", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyPoolA, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying DaemonSet for pool-b is created")
			verifyPoolB := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-multipools-supernode-pool-b", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyPoolB, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying pool-b uses specified image (different from default)")
			verifyPoolBImage := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-multipools-supernode-pool-b", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[0].image}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("flwr/supernode:1.24.0"))
			}
			Eventually(verifyPoolBImage, time.Minute, time.Second).Should(Succeed())
		})

		It("should create workloads with volumes and volumeMounts", func() {
			By("applying Federation CR with volumes")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-volumes-e2e.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying SuperLink Deployment is created")
			verifyDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-volumes-e2e-superlink", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDeployment, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperLink Deployment has volumes")
			verifySuperlinkVolumes := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-volumes-e2e-superlink", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.volumes[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				volumes := strings.Fields(output)
				g.Expect(volumes).To(ContainElements("superlink-data", "superlink-config"))
			}
			Eventually(verifySuperlinkVolumes, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperLink container has volumeMounts")
			verifySuperlinkMounts := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-volumes-e2e-superlink", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='superlink')].volumeMounts[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				mounts := strings.Fields(output)
				g.Expect(mounts).To(ContainElements("superlink-data", "superlink-config"))
			}
			Eventually(verifySuperlinkMounts, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperExec ServerApp sidecar has volumeMounts")
			verifySuperexecServerappMounts := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment",
					"federation-volumes-e2e-superlink", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='superexec-serverapp')].volumeMounts[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				mounts := strings.Fields(output)
				// SuperExec sidecar should only have superlink-data (from superexecVolumeMounts)
				g.Expect(mounts).To(ContainElement("superlink-data"))
				g.Expect(mounts).NotTo(ContainElement("superlink-config"))
			}
			Eventually(verifySuperexecServerappMounts, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode DaemonSet is created")
			verifyDaemonSet := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-volumes-e2e-supernode-default", "-n", federationTestNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyDaemonSet, 2*time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode DaemonSet has volumes")
			verifyPoolVolumes := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-volumes-e2e-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.volumes[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				volumes := strings.Fields(output)
				g.Expect(volumes).To(ContainElements("pool-data", "pool-scratch"))
			}
			Eventually(verifyPoolVolumes, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperNode container has volumeMounts")
			verifySupernodeMounts := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-volumes-e2e-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='supernode')].volumeMounts[*].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				mounts := strings.Fields(output)
				g.Expect(mounts).To(ContainElements("pool-data", "pool-scratch"))
			}
			Eventually(verifySupernodeMounts, time.Minute, time.Second).Should(Succeed())

			By("verifying SuperExec ClientApp sidecar has volumeMounts with readOnly")
			verifySuperexecClientappMounts := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "daemonset",
					"federation-volumes-e2e-supernode-default", "-n", federationTestNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[?(@.name=='superexec-clientapp')].volumeMounts[?(@.name=='pool-data')].readOnly}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"))
			}
			Eventually(verifySuperexecClientappMounts, time.Minute, time.Second).Should(Succeed())
		})

		It("should update status with Ready condition", func() {
			By("applying Federation CR")
			cmd := exec.Command("kubectl", "apply", "-f",
				"examples/federation-daemonset-subprocess.yaml",
				"-n", federationTestNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying Federation status shows Ready=True")
			verifyStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "federation",
					"federation-daemonset-subprocess", "-n", federationTestNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}
			Eventually(verifyStatus, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying endpoints are populated")
			verifyEndpoints := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "federation",
					"federation-daemonset-subprocess", "-n", federationTestNamespace,
					"-o", "jsonpath={.status.endpoints.superlinkService}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("federation-daemonset-subprocess-superlink"))
			}
			Eventually(verifyEndpoints, time.Minute, 5*time.Second).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
