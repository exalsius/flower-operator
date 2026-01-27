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
	"maps"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	federationv1 "github.com/exalsius/flower-operator/api/v1"
)

const (
	// Flower network ports
	portServerAppIO = 9091
	portFleetAPI    = 9092
	portControlAPI  = 9093
	portClientAppIO = 9094

	// Labels
	labelManagedBy = "flower-operator"

	// Condition types
	conditionTypeReady = "Ready"

	// Retry configuration
	maxRetries        = 5
	initialRetryDelay = 10 * time.Millisecond
)

// FederationReconciler reconciles a Federation object
type FederationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=flwr.exalsius.ai,resources=federations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=flwr.exalsius.ai,resources=federations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=flwr.exalsius.ai,resources=federations/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *FederationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	federation := &federationv1.Federation{}
	if err := r.Get(ctx, req.NamespacedName, federation); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Federation resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "unable to fetch Federation")
		return ctrl.Result{}, err
	}

	superLinkDeployment, err := r.reconcileSuperLinkDeployment(ctx, federation)
	if err != nil {
		logger.Error(err, "failed to reconcile SuperLink Deployment")
		return ctrl.Result{}, err
	}

	superLinkService, err := r.reconcileSuperLinkService(ctx, federation)
	if err != nil {
		logger.Error(err, "failed to reconcile SuperLink Service")
		return ctrl.Result{}, err
	}

	poolStatuses := make([]federationv1.PoolStatus, 0, len(federation.Spec.SuperNodes.Pools))
	for _, pool := range federation.Spec.SuperNodes.Pools {
		poolStatus, err := r.reconcilePoolWorkload(ctx, federation, pool)
		if err != nil {
			logger.Error(err, "failed to reconcile pool workload", "pool", pool.Name)
			return ctrl.Result{}, err
		}
		poolStatuses = append(poolStatuses, poolStatus)
	}

	if err := r.cleanupOrphanedPools(ctx, federation); err != nil {
		logger.Error(err, "failed to cleanup orphaned pools")
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(ctx, federation, superLinkDeployment, superLinkService, poolStatuses); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *FederationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&federationv1.Federation{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Named("federation").
		Complete(r)
}

// =============================================================================
// Builder Functions
// =============================================================================

func (r *FederationReconciler) buildSuperLinkDeployment(federation *federationv1.Federation) *appsv1.Deployment {
	name := fmt.Sprintf("%s-superlink", federation.Name)
	labels := r.buildLabels(federation, "superlink", "superlink", "")

	replicas := int32(1)

	containers := []corev1.Container{
		r.buildSuperLinkContainer(federation),
	}

	// Add SuperExec sidecar in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		containers = append(containers, r.buildSuperExecServerAppContainer(federation))
	}

	podSpec := corev1.PodSpec{
		Containers: containers,
	}

	// Merge podTemplate overrides
	if pt := federation.Spec.SuperLink.PodTemplate; pt != nil {
		podSpec = r.mergePodSpec(podSpec, pt.Spec)
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: federation.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
}

func (r *FederationReconciler) buildSuperLinkContainer(federation *federationv1.Federation) corev1.Container {
	args := []string{
		fmt.Sprintf("--isolation=%s", federation.Spec.IsolationMode),
	}
	if isInsecure(federation) {
		args = append(args, "--insecure")
	}

	args = append(args, federation.Spec.SuperLink.ExtraArgs...)

	ports := []corev1.ContainerPort{
		{Name: "fleet", ContainerPort: portFleetAPI, Protocol: corev1.ProtocolTCP},
		{Name: "control", ContainerPort: portControlAPI, Protocol: corev1.ProtocolTCP},
	}

	// Add ServerAppIO port in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		ports = append(ports, corev1.ContainerPort{
			Name: "serverappio", ContainerPort: portServerAppIO, Protocol: corev1.ProtocolTCP,
		})
	}

	return corev1.Container{
		Name:      "superlink",
		Image:     federation.Spec.SuperLink.Image,
		Args:      args,
		Ports:     ports,
		Resources: federation.Spec.SuperLink.Resources,
		Env:       federation.Spec.SuperLink.Env,
	}
}

func (r *FederationReconciler) buildSuperExecServerAppContainer(federation *federationv1.Federation) corev1.Container {
	args := []string{
		"--plugin-type=serverapp",
		fmt.Sprintf("--appio-api-address=localhost:%d", portServerAppIO),
	}
	if isInsecure(federation) {
		args = append(args, "--insecure")
	}

	return corev1.Container{
		Name:      "superexec-serverapp",
		Image:     federation.Spec.SuperLink.SuperExecImage,
		Args:      args,
		Resources: federation.Spec.SuperLink.SuperExecResources,
		Env:       federation.Spec.SuperLink.Env,
	}
}

func (r *FederationReconciler) buildSuperLinkService(federation *federationv1.Federation) *corev1.Service {
	name := fmt.Sprintf("%s-superlink", federation.Name)
	baseLabels := r.buildLabels(federation, "superlink", "superlink", "")

	// Merge user-provided labels (operator labels take precedence for core labels)
	labels := make(map[string]string)
	if federation.Spec.SuperLink.Service.Labels != nil {
		maps.Copy(labels, federation.Spec.SuperLink.Service.Labels)
	}
	// Operator-managed labels override user labels
	maps.Copy(labels, baseLabels)

	serviceType := corev1.ServiceTypeClusterIP
	if federation.Spec.SuperLink.Service.Type != "" {
		serviceType = federation.Spec.SuperLink.Service.Type
	}

	ports := federation.Spec.SuperLink.Service.Ports
	fleetPort := int32(portFleetAPI)
	if ports.GRPC > 0 {
		fleetPort = ports.GRPC
	}
	controlPort := int32(portControlAPI)
	if ports.Extra > 0 {
		controlPort = ports.Extra
	}
	serverAppIOPort := int32(portServerAppIO)
	if ports.ServerAppIO > 0 {
		serverAppIOPort = ports.ServerAppIO
	}

	servicePorts := []corev1.ServicePort{
		{Name: "fleet", Port: fleetPort, TargetPort: intstr.FromInt32(portFleetAPI), Protocol: corev1.ProtocolTCP},
		{Name: "control", Port: controlPort, TargetPort: intstr.FromInt32(portControlAPI), Protocol: corev1.ProtocolTCP},
	}

	// Add ServerAppIO port in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name: "serverappio", Port: serverAppIOPort, TargetPort: intstr.FromInt32(portServerAppIO), Protocol: corev1.ProtocolTCP,
		})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   federation.Namespace,
			Labels:      labels,
			Annotations: federation.Spec.SuperLink.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: baseLabels, // Selector must match pod labels, not user labels
			Ports:    servicePorts,
		},
	}
}

func (r *FederationReconciler) buildDaemonSet(federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) *appsv1.DaemonSet {
	name := fmt.Sprintf("%s-supernode-%s", federation.Name, pool.Name)
	labels := r.buildLabels(federation, "supernode", fmt.Sprintf("pool-%s", pool.Name), pool.Name)

	containers := []corev1.Container{
		r.buildSuperNodeContainer(federation, pool),
	}

	// Add SuperExec sidecar in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		containers = append(containers, r.buildSuperExecClientAppContainer(federation, pool))
	}

	podSpec := corev1.PodSpec{
		Containers: containers,
	}

	// Apply mountAll GPU config (runtimeClassName for NVIDIA, volumes for AMD)
	r.applyMountAllGPU(&podSpec, pool.GPU)

	// Merge podTemplate overrides
	if pt := pool.PodTemplate; pt != nil {
		podSpec = r.mergePodSpec(podSpec, pt.Spec)
	}

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: federation.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
}

func (r *FederationReconciler) buildStatefulSet(federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) *appsv1.StatefulSet {
	name := fmt.Sprintf("%s-supernode-%s", federation.Name, pool.Name)
	labels := r.buildLabels(federation, "supernode", fmt.Sprintf("pool-%s", pool.Name), pool.Name)

	containers := []corev1.Container{
		r.buildSuperNodeContainer(federation, pool),
	}

	// Add SuperExec sidecar in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		containers = append(containers, r.buildSuperExecClientAppContainer(federation, pool))
	}

	podSpec := corev1.PodSpec{
		Containers: containers,
	}

	// Apply mountAll GPU config (runtimeClassName for NVIDIA, volumes for AMD)
	r.applyMountAllGPU(&podSpec, pool.GPU)

	// Merge podTemplate overrides
	if pt := pool.PodTemplate; pt != nil {
		podSpec = r.mergePodSpec(podSpec, pt.Spec)
	}

	replicas := int32(1)
	if pool.Replicas != nil {
		replicas = *pool.Replicas
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: federation.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName: name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
}

func (r *FederationReconciler) buildSuperNodeContainer(federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) corev1.Container {
	image := federation.Spec.SuperNodes.Image
	if pool.Images.SuperNode != "" {
		image = pool.Images.SuperNode
	}

	superLinkService := fmt.Sprintf("%s-superlink", federation.Name)
	args := []string{
		fmt.Sprintf("--superlink=%s:%d", superLinkService, portFleetAPI),
		fmt.Sprintf("--isolation=%s", federation.Spec.IsolationMode),
	}
	if isInsecure(federation) {
		args = append(args, "--insecure")
	}

	// Add ClientAppIO address in process mode
	if federation.Spec.IsolationMode == federationv1.IsolationModeProcess {
		args = append(args, fmt.Sprintf("--clientappio-api-address=0.0.0.0:%d", portClientAppIO))
	}

	args = append(args, pool.ExtraArgs...)

	ports := []corev1.ContainerPort{
		{Name: "clientappio", ContainerPort: portClientAppIO, Protocol: corev1.ProtocolTCP},
	}

	resources := pool.SuperNodeResources.DeepCopy()
	// Only set GPU resource requests when enabled=true and mountAll=false
	if pool.GPU != nil && pool.GPU.Enabled && !pool.GPU.MountAll {
		resourceName := r.getGPUResourceName(pool.GPU)
		count := int64(pool.GPU.Count)
		if count == 0 {
			count = 1
		}
		quantity := resource.NewQuantity(count, resource.DecimalSI)

		if resources.Limits == nil {
			resources.Limits = corev1.ResourceList{}
		}
		if resources.Requests == nil {
			resources.Requests = corev1.ResourceList{}
		}
		resources.Limits[corev1.ResourceName(resourceName)] = *quantity
		resources.Requests[corev1.ResourceName(resourceName)] = *quantity
	}

	envVars := mergeEnvVars(federation.Spec.SuperNodes.Env, pool.Env)
	if gpuEnv := getMountAllGPUEnvVar(pool.GPU); gpuEnv != nil {
		envVars = append(envVars, *gpuEnv)
	}

	container := corev1.Container{
		Name:         "supernode",
		Image:        image,
		Args:         args,
		Ports:        ports,
		Resources:    *resources,
		Env:          envVars,
		VolumeMounts: getMountAllGPUVolumeMounts(pool.GPU),
	}

	// Set security context for AMD GPU access
	if secCtx := getMountAllGPUSecurityContext(pool.GPU); secCtx != nil {
		container.SecurityContext = secCtx
	}

	return container
}

func (r *FederationReconciler) buildSuperExecClientAppContainer(federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) corev1.Container {
	args := []string{
		"--plugin-type=clientapp",
		fmt.Sprintf("--appio-api-address=localhost:%d", portClientAppIO),
	}
	if isInsecure(federation) {
		args = append(args, "--insecure")
	}

	envVars := mergeEnvVars(federation.Spec.SuperNodes.Env, pool.Env)
	if gpuEnv := getMountAllGPUEnvVar(pool.GPU); gpuEnv != nil {
		envVars = append(envVars, *gpuEnv)
	}

	container := corev1.Container{
		Name:         "superexec-clientapp",
		Image:        pool.Images.SuperExecClientApp,
		Args:         args,
		Resources:    pool.SuperExecResources,
		Env:          envVars,
		VolumeMounts: getMountAllGPUVolumeMounts(pool.GPU),
	}

	// Set security context for AMD GPU access
	if secCtx := getMountAllGPUSecurityContext(pool.GPU); secCtx != nil {
		container.SecurityContext = secCtx
	}

	return container
}

// =============================================================================
// Reconciliation Helpers
// =============================================================================

// updateWithRetry retries an update operation on conflict errors with exponential backoff
func (r *FederationReconciler) updateWithRetry(ctx context.Context, obj client.Object, updateFunc func(client.Object) error) error {
	var lastErr error
	delay := initialRetryDelay

	for attempt := range maxRetries {
		key := client.ObjectKeyFromObject(obj)
		if err := r.Get(ctx, key, obj); err != nil {
			return fmt.Errorf("failed to re-fetch object: %w", err)
		}

		if err := updateFunc(obj); err != nil {
			if apierrors.IsConflict(err) {
				lastErr = err
				if attempt < maxRetries-1 {
					time.Sleep(delay)
					delay *= 2
					continue
				}
			}
			return err
		}

		return nil
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

func (r *FederationReconciler) reconcileSuperLinkDeployment(ctx context.Context, federation *federationv1.Federation) (*appsv1.Deployment, error) {
	logger := log.FromContext(ctx)
	desired := r.buildSuperLinkDeployment(federation)

	if err := controllerutil.SetControllerReference(federation, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference: %w", err)
	}

	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating SuperLink Deployment", "name", desired.Name)
			if err := r.Create(ctx, desired); err != nil {
				return nil, fmt.Errorf("failed to create deployment: %w", err)
			}
			return desired, nil
		}
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	latest := &appsv1.Deployment{}
	latest.Name = desired.Name
	latest.Namespace = desired.Namespace
	if err := r.updateWithRetry(ctx, latest, func(obj client.Object) error {
		dep := obj.(*appsv1.Deployment)
		dep.Spec = desired.Spec
		dep.Labels = desired.Labels
		if err := controllerutil.SetControllerReference(federation, dep, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		return r.Update(ctx, dep)
	}); err != nil {
		return nil, fmt.Errorf("failed to update deployment: %w", err)
	}

	return latest, nil
}

func (r *FederationReconciler) reconcileSuperLinkService(ctx context.Context, federation *federationv1.Federation) (*corev1.Service, error) {
	logger := log.FromContext(ctx)
	desired := r.buildSuperLinkService(federation)

	if err := controllerutil.SetControllerReference(federation, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set owner reference: %w", err)
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating SuperLink Service", "name", desired.Name)
			if err := r.Create(ctx, desired); err != nil {
				return nil, fmt.Errorf("failed to create service: %w", err)
			}
			return desired, nil
		}
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	// Check if Service type changed (immutable field - requires recreation)
	if existing.Spec.Type != desired.Spec.Type {
		logger.Info("Service type changed, recreating Service",
			"name", desired.Name,
			"oldType", existing.Spec.Type,
			"newType", desired.Spec.Type)

		if err := r.Delete(ctx, existing); err != nil {
			return nil, fmt.Errorf("failed to delete service for type change: %w", err)
		}

		// Wait a moment for deletion to complete
		time.Sleep(100 * time.Millisecond)

		logger.Info("creating SuperLink Service with new type", "name", desired.Name)
		if err := r.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed to create service: %w", err)
		}
		return desired, nil
	}

	latest := &corev1.Service{}
	latest.Name = desired.Name
	latest.Namespace = desired.Namespace
	if err := r.updateWithRetry(ctx, latest, func(obj client.Object) error {
		svc := obj.(*corev1.Service)

		// Preserve immutable Service fields
		clusterIP := svc.Spec.ClusterIP
		ipFamilyPolicy := svc.Spec.IPFamilyPolicy
		ipFamilies := svc.Spec.IPFamilies

		svc.Spec = desired.Spec
		svc.Spec.ClusterIP = clusterIP
		if ipFamilyPolicy != nil {
			svc.Spec.IPFamilyPolicy = ipFamilyPolicy
		}
		if len(ipFamilies) > 0 {
			svc.Spec.IPFamilies = ipFamilies
		}

		// Merge labels: operator labels take precedence over user labels
		if svc.Labels == nil {
			svc.Labels = make(map[string]string)
		}
		if desired.Labels != nil {
			for k, v := range desired.Labels {
				if !strings.HasPrefix(k, "app.kubernetes.io/") && k != "flower.flwr.ai/pool" {
					svc.Labels[k] = v
				}
			}
		}
		for k, v := range desired.Labels {
			if strings.HasPrefix(k, "app.kubernetes.io/") || k == "flower.flwr.ai/pool" {
				svc.Labels[k] = v
			}
		}

		if svc.Annotations == nil {
			svc.Annotations = make(map[string]string)
		}
		if desired.Annotations != nil {
			maps.Copy(svc.Annotations, desired.Annotations)
		}

		if err := controllerutil.SetControllerReference(federation, svc, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		return r.Update(ctx, svc)
	}); err != nil {
		return nil, fmt.Errorf("failed to update service: %w", err)
	}

	return latest, nil
}

func (r *FederationReconciler) reconcilePoolWorkload(ctx context.Context, federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) (federationv1.PoolStatus, error) {
	logger := log.FromContext(ctx)

	switch federation.Spec.Mode {
	case federationv1.DeploymentModeDaemonSet:
		return r.reconcileDaemonSet(ctx, logger, federation, pool)
	case federationv1.DeploymentModeStatefulSet:
		return r.reconcileStatefulSet(ctx, logger, federation, pool)
	default:
		return federationv1.PoolStatus{}, fmt.Errorf("unknown deployment mode: %s", federation.Spec.Mode)
	}
}

func (r *FederationReconciler) reconcileDaemonSet(ctx context.Context, logger logr.Logger, federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) (federationv1.PoolStatus, error) {
	desired := r.buildDaemonSet(federation, pool)

	if err := controllerutil.SetControllerReference(federation, desired, r.Scheme); err != nil {
		return federationv1.PoolStatus{}, fmt.Errorf("failed to set owner reference: %w", err)
	}

	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating SuperNode DaemonSet", "name", desired.Name, "pool", pool.Name)
			if err := r.Create(ctx, desired); err != nil {
				return federationv1.PoolStatus{}, fmt.Errorf("failed to create daemonset: %w", err)
			}
			return federationv1.PoolStatus{
				Name:    pool.Name,
				Kind:    "DaemonSet",
				Desired: 0,
				Ready:   0,
			}, nil
		}
		return federationv1.PoolStatus{}, fmt.Errorf("failed to get daemonset: %w", err)
	}

	latest := &appsv1.DaemonSet{}
	latest.Name = desired.Name
	latest.Namespace = desired.Namespace
	if err := r.updateWithRetry(ctx, latest, func(obj client.Object) error {
		ds := obj.(*appsv1.DaemonSet)
		ds.Spec = desired.Spec
		ds.Labels = desired.Labels
		if err := controllerutil.SetControllerReference(federation, ds, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		return r.Update(ctx, ds)
	}); err != nil {
		return federationv1.PoolStatus{}, fmt.Errorf("failed to update daemonset: %w", err)
	}

	return federationv1.PoolStatus{
		Name:    pool.Name,
		Kind:    "DaemonSet",
		Desired: latest.Status.DesiredNumberScheduled,
		Ready:   latest.Status.NumberReady,
	}, nil
}

func (r *FederationReconciler) reconcileStatefulSet(ctx context.Context, logger logr.Logger, federation *federationv1.Federation, pool federationv1.SuperNodePoolSpec) (federationv1.PoolStatus, error) {
	desired := r.buildStatefulSet(federation, pool)

	if err := controllerutil.SetControllerReference(federation, desired, r.Scheme); err != nil {
		return federationv1.PoolStatus{}, fmt.Errorf("failed to set owner reference: %w", err)
	}

	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating SuperNode StatefulSet", "name", desired.Name, "pool", pool.Name)
			if err := r.Create(ctx, desired); err != nil {
				return federationv1.PoolStatus{}, fmt.Errorf("failed to create statefulset: %w", err)
			}
			replicas := int32(1)
			if pool.Replicas != nil {
				replicas = *pool.Replicas
			}
			return federationv1.PoolStatus{
				Name:    pool.Name,
				Kind:    "StatefulSet",
				Desired: replicas,
				Ready:   0,
			}, nil
		}
		return federationv1.PoolStatus{}, fmt.Errorf("failed to get statefulset: %w", err)
	}

	latest := &appsv1.StatefulSet{}
	latest.Name = desired.Name
	latest.Namespace = desired.Namespace
	if err := r.updateWithRetry(ctx, latest, func(obj client.Object) error {
		sts := obj.(*appsv1.StatefulSet)
		sts.Spec = desired.Spec
		sts.Labels = desired.Labels
		if err := controllerutil.SetControllerReference(federation, sts, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		return r.Update(ctx, sts)
	}); err != nil {
		return federationv1.PoolStatus{}, fmt.Errorf("failed to update statefulset: %w", err)
	}

	desired_replicas := int32(1)
	if latest.Spec.Replicas != nil {
		desired_replicas = *latest.Spec.Replicas
	}

	return federationv1.PoolStatus{
		Name:    pool.Name,
		Kind:    "StatefulSet",
		Desired: desired_replicas,
		Ready:   latest.Status.ReadyReplicas,
	}, nil
}

func (r *FederationReconciler) cleanupOrphanedPools(ctx context.Context, federation *federationv1.Federation) error {
	logger := log.FromContext(ctx)

	currentPools := make(map[string]bool)
	for _, pool := range federation.Spec.SuperNodes.Pools {
		currentPools[pool.Name] = true
	}

	// List and cleanup DaemonSets
	daemonSetList := &appsv1.DaemonSetList{}
	if err := r.List(ctx, daemonSetList,
		client.InNamespace(federation.Namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance":   federation.Name,
			"app.kubernetes.io/managed-by": labelManagedBy,
			"app.kubernetes.io/name":       "supernode",
		}); err != nil {
		return fmt.Errorf("failed to list daemonsets: %w", err)
	}

	for i := range daemonSetList.Items {
		ds := &daemonSetList.Items[i]
		poolName := ds.Labels["flower.flwr.ai/pool"]
		if poolName != "" && !currentPools[poolName] {
			logger.Info("deleting orphaned DaemonSet", "name", ds.Name, "pool", poolName)
			if err := r.Delete(ctx, ds); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete orphaned daemonset %s: %w", ds.Name, err)
			}
		}
	}

	// List and cleanup StatefulSets
	statefulSetList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSetList,
		client.InNamespace(federation.Namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance":   federation.Name,
			"app.kubernetes.io/managed-by": labelManagedBy,
			"app.kubernetes.io/name":       "supernode",
		}); err != nil {
		return fmt.Errorf("failed to list statefulsets: %w", err)
	}

	for i := range statefulSetList.Items {
		sts := &statefulSetList.Items[i]
		poolName := sts.Labels["flower.flwr.ai/pool"]
		if poolName != "" && !currentPools[poolName] {
			logger.Info("deleting orphaned StatefulSet", "name", sts.Name, "pool", poolName)
			if err := r.Delete(ctx, sts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete orphaned statefulset %s: %w", sts.Name, err)
			}
		}
	}

	return nil
}

func (r *FederationReconciler) updateStatus(ctx context.Context, federation *federationv1.Federation, deployment *appsv1.Deployment, service *corev1.Service, poolStatuses []federationv1.PoolStatus) error {
	latest := &federationv1.Federation{}
	if err := r.Get(ctx, types.NamespacedName{Name: federation.Name, Namespace: federation.Namespace}, latest); err != nil {
		return fmt.Errorf("failed to re-fetch federation: %w", err)
	}

	latest.Status.ObservedGeneration = latest.Generation

	controlPort := int32(portControlAPI)
	if federation.Spec.SuperLink.Service.Ports.Extra > 0 {
		controlPort = federation.Spec.SuperLink.Service.Ports.Extra
	}
	latest.Status.Endpoints.SuperLinkService = fmt.Sprintf("%s.%s.svc:%d", service.Name, service.Namespace, controlPort)

	latest.Status.Pools = poolStatuses

	var totalReady, totalDesired int32
	for _, ps := range poolStatuses {
		totalReady += ps.Ready
		totalDesired += ps.Desired
	}
	latest.Status.ReadySupernodes = totalReady
	latest.Status.DesiredSupernodes = totalDesired
	latest.Status.Supernodes = fmt.Sprintf("%d/%d", totalReady, totalDesired)

	ready := true
	message := "All components are ready"

	if deployment.Status.ReadyReplicas < 1 {
		ready = false
		message = "SuperLink deployment not ready"
	}

	for _, ps := range poolStatuses {
		if ps.Ready < ps.Desired {
			ready = false
			message = fmt.Sprintf("Pool %s not fully ready (%d/%d)", ps.Name, ps.Ready, ps.Desired)
			break
		}
	}

	condition := metav1.Condition{
		Type:               conditionTypeReady,
		ObservedGeneration: latest.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "AllReady"
		condition.Message = message
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "NotReady"
		condition.Message = message
	}
	meta.SetStatusCondition(&latest.Status.Conditions, condition)

	if err := r.Status().Update(ctx, latest); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// =============================================================================
// Utility Helpers
// =============================================================================

func (r *FederationReconciler) buildLabels(federation *federationv1.Federation, name, component, poolName string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/instance":   federation.Name,
		"app.kubernetes.io/component":  component,
		"app.kubernetes.io/managed-by": labelManagedBy,
	}
	if poolName != "" {
		labels["flower.flwr.ai/pool"] = poolName
	}
	return labels
}

// isInsecure returns whether insecure mode should be enabled.
// Defaults to true if not explicitly set.
func isInsecure(federation *federationv1.Federation) bool {
	if federation.Spec.Insecure == nil {
		return true // default
	}
	return *federation.Spec.Insecure
}

// mergeEnvVars merges two env var slices; override takes precedence by Name.
func mergeEnvVars(base, override []corev1.EnvVar) []corev1.EnvVar {
	if len(base) == 0 {
		return override
	}
	if len(override) == 0 {
		return base
	}
	envMap := make(map[string]corev1.EnvVar)
	for _, e := range base {
		envMap[e.Name] = e
	}
	for _, e := range override {
		envMap[e.Name] = e
	}
	result := make([]corev1.EnvVar, 0, len(envMap))
	for _, e := range envMap {
		result = append(result, e)
	}
	return result
}

func (r *FederationReconciler) getGPUResourceName(gpu *federationv1.GPUConfig) string {
	if gpu.ResourceName != "" {
		return gpu.ResourceName
	}
	switch gpu.Vendor {
	case "nvidia":
		return "nvidia.com/gpu"
	case "amd":
		return "amd.com/gpu"
	default:
		return "nvidia.com/gpu"
	}
}

func (r *FederationReconciler) applyMountAllGPU(podSpec *corev1.PodSpec, gpu *federationv1.GPUConfig) {
	if gpu == nil || !gpu.MountAll {
		return
	}

	switch gpu.Vendor {
	case "nvidia":
		runtimeClass := "nvidia"
		podSpec.RuntimeClassName = &runtimeClass

	case "amd":
		// Add volumes for AMD GPU access
		charDevice := corev1.HostPathCharDev
		directory := corev1.HostPathDirectory
		podSpec.Volumes = append(podSpec.Volumes,
			corev1.Volume{
				Name: "dev-kfd",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/dev/kfd",
						Type: &charDevice,
					},
				},
			},
			corev1.Volume{
				Name: "dev-dri",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/dev/dri",
						Type: &directory,
					},
				},
			},
		)
	}
}

func getMountAllGPUEnvVar(gpu *federationv1.GPUConfig) *corev1.EnvVar {
	if gpu == nil || !gpu.MountAll {
		return nil
	}

	switch gpu.Vendor {
	case "nvidia":
		return &corev1.EnvVar{Name: "NVIDIA_VISIBLE_DEVICES", Value: "all"}
	case "amd":
		return &corev1.EnvVar{Name: "ROCR_VISIBLE_DEVICES", Value: "all"}
	default:
		return nil
	}
}

func getMountAllGPUVolumeMounts(gpu *federationv1.GPUConfig) []corev1.VolumeMount {
	if gpu == nil || !gpu.MountAll || gpu.Vendor != "amd" {
		return nil
	}

	return []corev1.VolumeMount{
		{Name: "dev-kfd", MountPath: "/dev/kfd"},
		{Name: "dev-dri", MountPath: "/dev/dri"},
	}
}

func getMountAllGPUSecurityContext(gpu *federationv1.GPUConfig) *corev1.SecurityContext {
	if gpu == nil || !gpu.MountAll || gpu.Vendor != "amd" {
		return nil
	}

	privileged := true
	return &corev1.SecurityContext{Privileged: &privileged}
}

func (r *FederationReconciler) mergePodSpec(base, override corev1.PodSpec) corev1.PodSpec {
	// Scheduling overrides
	if override.NodeSelector != nil {
		base.NodeSelector = override.NodeSelector
	}
	if override.Affinity != nil {
		base.Affinity = override.Affinity
	}
	if len(override.Tolerations) > 0 {
		base.Tolerations = override.Tolerations
	}

	// Service account and runtime
	if override.ServiceAccountName != "" {
		base.ServiceAccountName = override.ServiceAccountName
	}
	if override.PriorityClassName != "" {
		base.PriorityClassName = override.PriorityClassName
	}
	if override.RuntimeClassName != nil {
		base.RuntimeClassName = override.RuntimeClassName
	}

	// Merge volumes (append, don't replace)
	if len(override.Volumes) > 0 {
		existingVolumes := make(map[string]bool)
		for _, v := range base.Volumes {
			existingVolumes[v.Name] = true
		}
		for _, v := range override.Volumes {
			if !existingVolumes[v.Name] {
				base.Volumes = append(base.Volumes, v)
			}
		}
	}

	// Security context
	if override.SecurityContext != nil {
		base.SecurityContext = override.SecurityContext
	}

	// DNS settings
	if override.DNSPolicy != "" {
		base.DNSPolicy = override.DNSPolicy
	}
	if override.DNSConfig != nil {
		base.DNSConfig = override.DNSConfig
	}

	// Host settings
	if override.HostNetwork {
		base.HostNetwork = override.HostNetwork
	}
	if override.HostPID {
		base.HostPID = override.HostPID
	}
	if override.HostIPC {
		base.HostIPC = override.HostIPC
	}

	// Image pull secrets (merge)
	if len(override.ImagePullSecrets) > 0 {
		existingSecrets := make(map[string]bool)
		for _, s := range base.ImagePullSecrets {
			existingSecrets[s.Name] = true
		}
		for _, s := range override.ImagePullSecrets {
			if !existingSecrets[s.Name] {
				base.ImagePullSecrets = append(base.ImagePullSecrets, s)
			}
		}
	}

	return base
}
