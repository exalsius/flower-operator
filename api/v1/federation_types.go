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

// NOTE: This is a v1 API with strong validation.
// It assumes controller-runtime / kubebuilder with CEL validation enabled (k8s >= 1.25).

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Federation is the Schema for the federations API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fedf
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Isolation",type=string,JSONPath=`.spec.isolationMode`
// +kubebuilder:printcolumn:name="Supernodes",type=string,JSONPath=`.status.supernodes`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.spec.supernodes.pools) > 0",message="spec.supernodes.pools must contain at least one pool"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == 'DaemonSet' ? self.spec.supernodes.pools.all(p, !has(p.replicas)) : true",message="replicas must not be set for pools when mode is DaemonSet"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == 'StatefulSet' ? self.spec.supernodes.pools.all(p, has(p.replicas) && p.replicas >= 1) : true",message="replicas must be set (>=1) for every pool when mode is StatefulSet"
// +kubebuilder:validation:XValidation:rule="self.spec.isolationMode != 'process' || (has(self.spec.superlink.superexecImage) && size(self.spec.superlink.superexecImage) > 0)",message="spec.superlink.superexecImage is required when isolationMode=process"
// +kubebuilder:validation:XValidation:rule="self.spec.isolationMode != 'process' || self.spec.supernodes.pools.all(p, has(p.images.superexecClientApp) && size(p.images.superexecClientApp) > 0)",message="spec.supernodes.pools[*].images.superexecClientApp is required when isolationMode=process"
// +kubebuilder:validation:XValidation:rule="self.spec.isolationMode != 'subprocess' || !has(self.spec.superlink.superexecImage) || size(self.spec.superlink.superexecImage) == 0",message="spec.superlink.superexecImage must be empty when isolationMode=subprocess"
// +kubebuilder:validation:XValidation:rule="self.spec.isolationMode != 'subprocess' || self.spec.supernodes.pools.all(p, !has(p.images.superexecClientApp) || size(p.images.superexecClientApp) == 0)",message="spec.supernodes.pools[*].images.superexecClientApp must be empty when isolationMode=subprocess"
// +kubebuilder:validation:XValidation:rule="self.spec.supernodes.pools.all(p, !has(p.gpu) || !has(p.gpu.mountAll) || p.gpu.mountAll == false || (has(p.gpu.vendor) && size(p.gpu.vendor) > 0))",message="spec.supernodes.pools[*].gpu.vendor is required when gpu.mountAll=true"
type Federation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FederationSpec   `json:"spec,omitempty"`
	Status FederationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FederationList contains a list of Federation
type FederationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Federation `json:"items"`
}

//
// Spec
//

// FederationSpec defines the desired state of Federation
type FederationSpec struct {
	// Mode controls how SuperNode pools are materialized.
	// - DaemonSet: one DaemonSet per pool
	// - StatefulSet: one StatefulSet per pool (pool.replicas required)
	// +kubebuilder:validation:Required
	Mode DeploymentMode `json:"mode"`

	// IsolationMode configures Flower isolation semantics globally for SuperLink and SuperNodes.
	// - subprocess: SuperLink/SuperNode launch SuperExec internally (no operator-managed SuperExec containers)
	// - process: operator deploys SuperExec externally (as sidecars in this design)
	// +kubebuilder:validation:Required
	IsolationMode IsolationMode `json:"isolationMode"`

	// Version is an optional convenience field to default official Flower images (e.g., flwr/superlink:<version>).
	// If set, controllers may use it when explicit images are omitted.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version,omitempty"`

	// Insecure controls whether to use insecure connections (--insecure flag).
	// When true, all components (SuperLink, SuperNodes, SuperExec sidecars) will use insecure connections.
	// Defaults to true to match current behavior.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Insecure *bool `json:"insecure,omitempty"`

	// SuperLink config.
	// +kubebuilder:validation:Required
	SuperLink SuperLinkSpec `json:"superlink"`

	// SuperNodes config (pools required).
	// +kubebuilder:validation:Required
	SuperNodes SuperNodesSpec `json:"supernodes"`
}

// DeploymentMode specifies how SuperNode pools are deployed
// +kubebuilder:validation:Enum=DaemonSet;StatefulSet
type DeploymentMode string

const (
	DeploymentModeDaemonSet   DeploymentMode = "DaemonSet"
	DeploymentModeStatefulSet DeploymentMode = "StatefulSet"
)

// IsolationMode specifies how Flower isolation semantics work
// +kubebuilder:validation:Enum=process;subprocess
type IsolationMode string

const (
	IsolationModeProcess    IsolationMode = "process"
	IsolationModeSubprocess IsolationMode = "subprocess"
)

//
// SuperLink
//

// SuperLinkSpec defines the configuration for the SuperLink component
type SuperLinkSpec struct {
	// Image for the SuperLink container.
	// In subprocess isolation, this image must include any ServerApp dependencies.
	// In process isolation, ServerApp dependencies typically live in superexecImage.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// SuperExecImage is the image used as a sidecar when isolationMode=process (ServerApp plugin).
	// Must be empty when isolationMode=subprocess.
	// +kubebuilder:validation:Optional
	SuperExecImage string `json:"superexecImage,omitempty"`

	// Resources for the SuperLink container.
	// +kubebuilder:validation:Optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// SuperExecResources for the SuperExec sidecar (process mode).
	// +kubebuilder:validation:Optional
	SuperExecResources corev1.ResourceRequirements `json:"superexecResources,omitempty"`

	// Env is a list of environment variables for the SuperLink container.
	// In process mode, these are also applied to the SuperExec ServerApp sidecar.
	// +kubebuilder:validation:Optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// ExtraArgs are optional extra args appended to the SuperLink container command.
	// +kubebuilder:validation:Optional
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// PodTemplate allows customizing scheduling and pod-level settings.
	// The operator owns container lists, command/args, and ports.
	// +kubebuilder:validation:Optional
	PodTemplate *corev1.PodTemplateSpec `json:"podTemplate,omitempty"`

	// Service config for exposing the SuperLink endpoints used by `flwr run`.
	// +kubebuilder:validation:Optional
	Service SuperLinkServiceSpec `json:"service,omitempty"`
}

// SuperLinkServiceSpec defines service configuration for SuperLink
type SuperLinkServiceSpec struct {
	// Type of Service for SuperLink.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=ClusterIP
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	Type corev1.ServiceType `json:"type,omitempty"`

	// Ports are optional. Controllers may default these based on Flower defaults.
	// +kubebuilder:validation:Optional
	Ports SuperLinkPorts `json:"ports,omitempty"`

	// Labels to add to the Service. These are merged with operator-managed labels.
	// Operator-managed labels (app.kubernetes.io/* and flower.flwr.ai/*) take precedence.
	// +kubebuilder:validation:Optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations to add to the Service.
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// SuperLinkPorts defines the ports for SuperLink service
type SuperLinkPorts struct {
	// GRPC port used by SuperNodes to connect to SuperLink.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=9092
	GRPC int32 `json:"grpc,omitempty"`

	// ServerAppIO API port used by ServerApp SuperExec/ServerApp processes (process mode).
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=9091
	ServerAppIO int32 `json:"serverAppIO,omitempty"`

	// Extra is an optional port (e.g., metrics or other).
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=9093
	Extra int32 `json:"extra,omitempty"`
}

//
// SuperNodes (Pools only)
//

// SuperNodesSpec defines the configuration for SuperNode pools
type SuperNodesSpec struct {
	// Image is the default SuperNode image used when pool.images.supernode is empty.
	// In subprocess isolation, this image must include ClientApp dependencies.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Env is the default list of environment variables for SuperNode pools.
	// Pool-level env vars are merged with these (pool takes precedence on conflicts).
	// +kubebuilder:validation:Optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Pools is the only way to define SuperNodes.
	// Each pool becomes one DaemonSet/StatefulSet depending on spec.mode.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Pools []SuperNodePoolSpec `json:"pools"`
}

// SuperNodePoolSpec defines a pool of SuperNodes
type SuperNodePoolSpec struct {
	// Name is used for generated resource names (e.g., <fed>-supernode-<pool>).
	// Must be unique within the Federation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Replicas is required in StatefulSet mode and must be unset in DaemonSet mode.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Images for this pool.
	// +kubebuilder:validation:Required
	Images PoolImages `json:"images"`

	// GPU config for this pool. If enabled, the operator will request GPUs via extended resources.
	// +kubebuilder:validation:Optional
	GPU *GPUConfig `json:"gpu,omitempty"`

	// SuperNodeResources for the SuperNode container.
	// +kubebuilder:validation:Optional
	SuperNodeResources corev1.ResourceRequirements `json:"supernodeResources,omitempty"`

	// SuperExecResources for the SuperExec sidecar (process mode).
	// +kubebuilder:validation:Optional
	SuperExecResources corev1.ResourceRequirements `json:"superexecResources,omitempty"`

	// PodTemplate allows customizing scheduling and pod-level settings for this pool.
	// The operator owns container lists, command/args, and ports.
	// +kubebuilder:validation:Optional
	PodTemplate *corev1.PodTemplateSpec `json:"podTemplate,omitempty"`

	// ExtraArgs are optional extra args for SuperNode container.
	// +kubebuilder:validation:Optional
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// Env is a list of environment variables for this pool's SuperNode container.
	// These are merged with spec.supernodes.env (pool env takes precedence).
	// In process mode, these are also applied to the SuperExec ClientApp sidecar.
	// +kubebuilder:validation:Optional
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// PoolImages defines the container images for a SuperNode pool
type PoolImages struct {
	// SuperNode image for this pool. If empty, defaults to spec.supernodes.image.
	// +kubebuilder:validation:Optional
	SuperNode string `json:"supernode,omitempty"`

	// SuperExecClientApp image for ClientApps used as sidecar when isolationMode=process.
	// Must be empty when isolationMode=subprocess (validated at Federation level).
	// +kubebuilder:validation:Optional
	SuperExecClientApp string `json:"superexecClientApp,omitempty"`
}

// GPUConfig expresses intent for GPU allocation.
// Kubernetes node inventory is not modeled in the CRD.
// GPU allocation is driven by resource requests/limits for resourceName.
type GPUConfig struct {
	// Enabled toggles GPU requests for this pool.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Vendor is an optional convenience hint (e.g., for defaulting resourceName).
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=nvidia;amd
	Vendor string `json:"vendor,omitempty"`

	// ResourceName is the extended resource name for GPUs on this cluster (e.g., "nvidia.com/gpu").
	// Required if enabled=true (unless your controller defaults it from vendor).
	// +kubebuilder:validation:Optional
	ResourceName string `json:"resourceName,omitempty"`

	// Count is the number of GPUs requested per Pod.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count,omitempty"`

	// MountAll exposes all GPUs on each node to the pod.
	// When true, vendor must be set. Count and resourceName are ignored.
	// For NVIDIA: sets runtimeClassName and NVIDIA_VISIBLE_DEVICES=all
	// For AMD: mounts /dev/kfd and /dev/dri, sets ROCR_VISIBLE_DEVICES=all
	// +kubebuilder:validation:Optional
	MountAll bool `json:"mountAll,omitempty"`
}

//
// Status
//

// FederationStatus defines the observed state of Federation
type FederationStatus struct {
	// ObservedGeneration reflects the generation last processed by the controller.
	// +kubebuilder:validation:Optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Supernodes shows aggregate supernode availability in "ready/desired" format (e.g., "2/3").
	// +kubebuilder:validation:Optional
	Supernodes string `json:"supernodes,omitempty"`

	// ReadySupernodes is the total number of ready supernodes across all pools.
	// +kubebuilder:validation:Optional
	ReadySupernodes int32 `json:"readySupernodes,omitempty"`

	// DesiredSupernodes is the total number of desired supernodes across all pools.
	// +kubebuilder:validation:Optional
	DesiredSupernodes int32 `json:"desiredSupernodes,omitempty"`

	// Endpoints advertises the SuperLink address users should target with `flwr run`.
	// +kubebuilder:validation:Optional
	Endpoints FederationEndpoints `json:"endpoints,omitempty"`

	// Pools provides per-pool health and replica readiness.
	// +kubebuilder:validation:Optional
	Pools []PoolStatus `json:"pools,omitempty"`

	// Conditions includes a Ready condition and other reconciliation signals.
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// FederationEndpoints provides connection information for the Federation
type FederationEndpoints struct {
	// SuperLinkService is typically "<service>.<namespace>.svc:<port>" or "<service>:<port>".
	// +kubebuilder:validation:Optional
	SuperLinkService string `json:"superlinkService,omitempty"`
}

// PoolStatus provides status information for a SuperNode pool
type PoolStatus struct {
	// Name of the pool.
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"`

	// Kind is the workload kind backing this pool: DaemonSet or StatefulSet.
	// +kubebuilder:validation:Optional
	Kind string `json:"kind,omitempty"`

	// Desired is desired replicas (StatefulSet) or desired scheduled pods (DaemonSet).
	// +kubebuilder:validation:Optional
	Desired int32 `json:"desired,omitempty"`

	// Ready is ready replicas/pods.
	// +kubebuilder:validation:Optional
	Ready int32 `json:"ready,omitempty"`

	// Conditions are pool-scoped conditions (e.g., Schedulable, GPUResourcesAvailable).
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(&Federation{}, &FederationList{})
}
