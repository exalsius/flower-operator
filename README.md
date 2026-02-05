# flower-operator

A Kubernetes operator for managing [Flower](https://flower.ai/) Federated Learning federations.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Deployment Modes](#deployment-modes)
- [Isolation Modes](#isolation-modes)
- [SuperNode Pools](#supernode-pools)
- [GPU Configuration](#gpu-configuration)
- [Node Configuration](#node-configuration)
- [Configuration Reference](#configuration-reference)
- [Examples](#examples)
- [Usage Workflow](#usage-workflow)
- [Status and Monitoring](#status-and-monitoring)
- [Troubleshooting](#troubleshooting)
- [Uninstallation](#uninstallation)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Overview

The flower-operator manages **Flower Federated Learning federations** on Kubernetes. It automates the deployment and lifecycle management of federated learning infrastructure, allowing you to focus on your ML workloads.

A **Federation** consists of:

- **SuperLink**: The control plane that coordinates federated learning runs
- **SuperNodes**: Client nodes that participate in training (organized into pools)
- **SuperExec** (optional): Processes that execute ServerApps and ClientApps

The operator provisions and manages this infrastructure, exposes a stable SuperLink endpoint, and lets users submit training jobs externally using `flwr run`.

For more information see the [Flower Documentation](https://flower.ai/docs/framework/index.html).

> **Note**: The operator does NOT start training jobs automatically. Training is triggered by users via the Flower CLI `flwr`.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        Federation CR                            │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    flower-operator                              │
│                  (Controller Manager)                           │
└─────────────────────────────────────────────────────────────────┘
                              │
            ┌─────────────────┼─────────────────┐
            ▼                 ▼                 ▼
┌───────────────────┐ ┌─────────────┐ ┌─────────────────────────┐
│ SuperLink         │ │  Service    │ │  SuperNode Pools        │
│ Deployment        │ │ (ClusterIP/ │ │  (DaemonSet or          │
│ + SuperExec       │ │  NodePort/  │ │   StatefulSet per pool) │
│   sidecar*        │ │  LoadBal.)  │ │  + SuperExec sidecars*  │
└───────────────────┘ └─────────────┘ └─────────────────────────┘

* SuperExec sidecars are only created in "process" isolation mode
```

The controller reconciles a single `Federation` CR into:
- One SuperLink Deployment + Service
- One workload (DaemonSet or StatefulSet) per SuperNode pool

Resources are named deterministically:
- `<federation>-superlink` for SuperLink components
- `<federation>-supernode-<pool>` for each pool's workload

For more information see [Flower Architecture](https://flower.ai/docs/framework/explanation-flower-architecture.html).

## Features

| Feature | Description |
|---------|-------------|
| **Deployment Modes** | DaemonSet (one pod per node) or StatefulSet (fixed replicas) |
| **Isolation Modes** | Process (sidecar containers) or subprocess (internal execution) |
| **Multi-Pool Support** | Heterogeneous hardware with different images, resources, and scheduling per pool |
| **GPU Support** | NVIDIA and AMD GPUs with per-pool configuration |
| **Resource Management** | Fine-grained resource requests/limits for all containers |
| **Flexible Scheduling** | nodeSelector, affinity, and tolerations via podTemplate |
| **Service Exposure** | ClusterIP, NodePort, or LoadBalancer for SuperLink |
| **Status Reporting** | Per-pool readiness, conditions, and SuperLink endpoint |

## Prerequisites

- Kubernetes v1.25+ (for CEL validation support)
- kubectl v1.11.3+
- Access to a Kubernetes cluster with cluster-admin privileges
- (Optional) GPU device plugins if using GPU features (NVIDIA or AMD)

## Installation

Install the operator using Helm:

```sh
# Install using Helm
helm install flower-operator ./dist/chart/ \
  --namespace flower-operator-system \
  --create-namespace \
  --set manager.image.repository=<registry>/flower-operator \
  --set manager.image.tag=<tag>
```

Or with a custom values file:

```sh
helm install flower-operator ./dist/chart/ \
  --namespace flower-operator-system \
  --create-namespace \
  -f my-values.yaml
```

Key Helm values:
- `manager.image.repository`: Controller manager image repository
- `manager.image.tag`: Controller manager image tag
- `manager.replicas`: Number of controller replicas (default: 1)
- `crd.enable`: Install CRDs with chart (default: true)
- `crd.keep`: Keep CRDs on uninstall (default: true)
- `metrics.enable`: Enable metrics endpoint (default: true)

See [dist/chart/values.yaml](dist/chart/values.yaml) for all available options.

## Quick Start

1. **Create a minimal Federation**:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: my-federation
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        replicas: 2
```

2. **Apply the Federation**:

```sh
kubectl apply -f my-federation.yaml
```

3. **Check the status**:

```sh
kubectl get federation my-federation
```

Output:
```
NAME            MODE          ISOLATION    SUPERNODES   READY   AGE
my-federation   StatefulSet   subprocess   2/2          True    1m
```

4. **Port-forward the SuperLink service** (Port 9093):

```sh
kubectl port-forward service/my-federation-superlink 9093:9093
```

5. **Add the remote federation configuration to your Flower app's `pyproject.toml`**:

```toml
[tool.flwr.federations.remote-federation]
address = "127.0.0.1:9093"
insecure = true
```

6. **Run your Flower app on the remote federation**:

```sh
flwr run . remote-federation --stream
```

## Deployment Modes

The `spec.mode` field controls how SuperNode pools are deployed.

### DaemonSet Mode

Creates one SuperNode pod per eligible Kubernetes node. Use this when you want node-local clients that scale automatically with your cluster.

| Characteristic | Description |
|----------------|-------------|
| Scaling | Automatic - one pod per matching node |
| Replica count | Not configurable (`replicas` must be unset) |
| Node targeting | Use `nodeSelector`, `affinity`, `tolerations` in `podTemplate` |
| Use cases | Edge deployments, node-local data, GPU nodes |

**Example**:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-daemonset
spec:
  mode: DaemonSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0
    resources:
      requests:
        memory: "256Mi"
        cpu: "100m"
      limits:
        memory: "512Mi"
        cpu: "500m"
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        images: {}
        supernodeResources:
          requests:
            memory: "512Mi"
            cpu: "200m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        # Target specific nodes:
        # podTemplate:
        #   spec:
        #     nodeSelector:
        #       flower.dev/role: client
```

### StatefulSet Mode

Creates a fixed number of SuperNode replicas per pool. Use this when you need a predictable client count or stable pod identities.

| Characteristic | Description |
|----------------|-------------|
| Scaling | Manual - controlled by `replicas` field |
| Replica count | Required for each pool (`replicas >= 1`) |
| Pod identity | Stable ordinal naming (e.g., `fed-supernode-pool-0`, `fed-supernode-pool-1`) |
| Use cases | Testing, predictable workloads, stateful clients |

**Example**:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-statefulset
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0
    resources:
      requests:
        memory: "256Mi"
        cpu: "100m"
      limits:
        memory: "512Mi"
        cpu: "500m"
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        replicas: 2  # Required in StatefulSet mode
        images: {}
        supernodeResources:
          requests:
            memory: "512Mi"
            cpu: "200m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
```

## Isolation Modes

The `spec.isolationMode` field controls how Flower executes ServerApps and ClientApps. This is a global setting for the entire Federation. The isolation modes directly map to the [isolation modes supported by the Flower framework](https://flower.ai/docs/framework/ref-flower-network-communication.html).



### Process Isolation (`process`)

SuperExec runs as **sidecar containers** alongside SuperLink and SuperNodes. This separates the control plane from application execution.

**Architecture**:

```
┌─────────────────────────────────┐    ┌─────────────────────────────────┐
│       SuperLink Pod             │    │       SuperNode Pod             │
│  ┌───────────┐ ┌─────────────┐  │    │  ┌───────────┐ ┌─────────────┐  │
│  │ superlink │ │ superexec-  │  │    │  │ supernode │ │ superexec-  │  │
│  │ container │ │ serverapp   │  │    │  │ container │ │ clientapp   │  │
│  │           │ │ sidecar     │  │    │  │           │ │ sidecar     │  │
│  └───────────┘ └─────────────┘  │    │  └───────────┘ └─────────────┘  │
│      control      dependencies  │    │      control      dependencies  │
│      logic        + ServerApp   │    │      logic        + ClientApp   │
└─────────────────────────────────┘    └─────────────────────────────────┘
```

**Requirements**:
- `spec.superlink.superexecImage` must be set
- `pool.images.superexecClientApp` must be set for each pool

**Benefits**:
- Separation of concerns between control and execution
- Independent image management for dependencies
- Smaller base images for SuperLink/SuperNode

**Example**:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-process
spec:
  mode: StatefulSet
  isolationMode: process

  superlink:
    image: flwr/superlink:1.25.0
    superexecImage: flwr/superexec:1.25.0  # Required
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        replicas: 2
        images:
          superexecClientApp: flwr/superexec:1.25.0  # Required
```

### Subprocess Isolation (`subprocess`)

SuperLink and SuperNode containers spawn SuperExec internally. No sidecar containers are created.

**Architecture**:

```
┌─────────────────────┐    ┌─────────────────────┐
│   SuperLink Pod     │    │   SuperNode Pod     │
│  ┌───────────────┐  │    │  ┌───────────────┐  │
│  │   superlink   │  │    │  │   supernode   │  │
│  │   container   │  │    │  │   container   │  │
│  │  (includes    │  │    │  │  (includes    │  │
│  │   ServerApp   │  │    │  │   ClientApp   │  │
│  │   deps)       │  │    │  │   deps)       │  │
│  └───────────────┘  │    │  └───────────────┘  │
└─────────────────────┘    └─────────────────────┘
```

**Requirements**:
- `spec.superlink.superexecImage` must be empty/unset
- `pool.images.superexecClientApp` must be empty/unset for all pools
- Images must include all application dependencies

**Benefits**:
- Simpler deployment with fewer containers
- Single image to manage per component
- Lower resource overhead

**Example**:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-subprocess
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: my-registry/superlink-with-deps:1.25.0  # Includes ServerApp deps
    service:
      type: ClusterIP

  supernodes:
    image: my-registry/supernode-with-deps:1.25.0  # Includes ClientApp deps
    pools:
      - name: default
        replicas: 2
        images: {}  # No superexecClientApp
```

### Comparison Table

| Aspect | Process | Subprocess |
|--------|---------|------------|
| Containers per pod | 2 (main + sidecar) | 1 |
| Image management | Separate control/exec images | Single image with all deps |
| Resource overhead | Higher (two containers) | Lower |
| Dependency isolation | Better (sidecar handles deps) | All deps in main image |
| Configuration complexity | Higher (more images to specify) | Lower |

## SuperNode Pools

Pools are the **only way** to define SuperNodes. Each pool becomes its own Kubernetes workload (DaemonSet or StatefulSet), enabling heterogeneous configurations within a single Federation.

### Why Pools?

- **Heterogeneous hardware**: Different nodes may have different capabilities
- **Different images**: Each pool can use different container images
- **Different resources**: Customize CPU/memory/GPU per pool
- **Independent scheduling**: Target specific nodes per pool
- **Mixed environments**: CPU, NVIDIA GPU, and AMD GPU pools in one Federation

### Pool Configuration

```yaml
supernodes:
  image: flwr/supernode:1.25.0  # Default image for all pools
  pools:
    - name: cpu-pool
      replicas: 4  # StatefulSet mode only
      images: {}   # Uses default image
      supernodeResources:
        requests:
          cpu: "1"
          memory: "2Gi"

    - name: gpu-pool
      replicas: 2
      images:
        supernode: my-registry/supernode-gpu:1.25.0  # Docker image with dependencies for the GPU training
      gpu:
        enabled: true
        vendor: nvidia
        resourceName: nvidia.com/gpu
        count: 1 # use one nvidia GPU in the SuperNode pod
```

### Multi-Pool Example

See [examples/federation-multipools.yaml](examples/federation-multipools.yaml) for a complete multi-pool configuration:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-multipools
spec:
  mode: DaemonSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: pool-a
        images: {}
        # podTemplate:
        #   spec:
        #     nodeSelector:
        #       flower.ai/pool: a

      - name: pool-b
        images:
          supernode: flwr/supernode:1.24.0  # Override with different version
        # podTemplate:
        #   spec:
        #     nodeSelector:
        #       flower.ai/pool: b
```

## GPU Configuration

GPU support is opt-in and configured per pool. The operator does not model GPU inventory; it relies on Kubernetes device plugins to provide GPU resources.

### Configuration Options

| Field | Description | Default |
|-------|-------------|---------|
| `enabled` | Toggle GPU requests for this pool | `false` |
| `vendor` | GPU vendor hint: `nvidia` or `amd` | - |
| `resourceName` | Extended resource name (e.g., `nvidia.com/gpu`) | - |
| `count` | Number of GPUs per pod | `1` |
| `mountAll` | Expose all GPUs on each node | `false` |

### NVIDIA GPU Example

```yaml
pools:
  - name: nvidia-pool
    replicas: 2
    images: {}
    gpu:
      enabled: true
      vendor: nvidia
      resourceName: nvidia.com/gpu
      count: 1
```

### AMD GPU Example

```yaml
pools:
  - name: amd-pool
    replicas: 2
    images: {}
    gpu:
      enabled: true
      vendor: amd
      resourceName: amd.com/gpu
      count: 1
```

### Mount All GPUs

When `mountAll: true`, all GPUs on each node are exposed to the pod:

- **NVIDIA**: Sets `runtimeClassName` and `NVIDIA_VISIBLE_DEVICES=all`
- **AMD**: Mounts `/dev/kfd` and `/dev/dri`, sets `ROCR_VISIBLE_DEVICES=all`

```yaml
pools:
  - name: all-gpus
    images: {}
    gpu:
      enabled: true
      vendor: nvidia
      mountAll: true  # Requires vendor to be set
```

### Mixed GPU Clusters

Use separate pools for different GPU vendors:

```yaml
supernodes:
  image: flwr/supernode:1.25.0
  pools:
    - name: cpu-only
      replicas: 4
      images: {}

    - name: nvidia-gpus
      replicas: 2
      images:
        supernode: my-registry/supernode-nvidia:1.25.0 # Image with NVIDIA dependencies
      gpu:
        enabled: true
        vendor: nvidia
        resourceName: nvidia.com/gpu
        count: 1

    - name: amd-gpus
      replicas: 2
      images:
        supernode: my-registry/supernode-amd:1.25.0 # Image with AMD dependencies
      gpu:
        enabled: true
        vendor: amd
        resourceName: amd.com/gpu
        count: 1
```

## Node Configuration

The `nodeConfig` field allows passing key-value pairs to the `--node-config` flag on SuperNodes. This is useful for data partitioning, client identification, and other per-replica configuration.

### Placeholders

Values can contain placeholders that are expanded at runtime:

| Placeholder | Description | StatefulSet | DaemonSet |
|-------------|-------------|-------------|-----------|
| `{index}` | Replica ordinal (0-based) | Yes | No |
| `{replicas}` | Total replicas in the pool | Yes | No |
| `{pool}` | Pool name | Yes | Yes |
| `{node}` | Kubernetes node name | Yes | Yes |

### StatefulSet with Data Partitioning

In StatefulSet mode, use `{index}` and `{replicas}` to implement data partitioning:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: partitioned-training
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0

  supernodes:
    image: my-app/client:latest
    pools:
      - name: default
        replicas: 4
        images: {}
        nodeConfig:
          # Each replica gets a unique partition (0, 1, 2, 3)
          partition-id: "{index}"
          # Total partitions matches replica count (4)
          num-partitions: "{replicas}"
          # Unique client name
          name: "client_{pool}_{index}"
```

This generates pods with the following `--node-config` values (in TOML format):
- Pod 0: `--node-config 'name="client_default_0" num-partitions=4 partition-id=0'`
- Pod 1: `--node-config 'name="client_default_1" num-partitions=4 partition-id=1'`
- Pod 2: `--node-config 'name="client_default_2" num-partitions=4 partition-id=2'`
- Pod 3: `--node-config 'name="client_default_3" num-partitions=4 partition-id=3'`

Note: String values are automatically quoted for TOML compatibility. Integer placeholders like `{index}` and `{replicas}` remain unquoted since they expand to integers.

### DaemonSet with Node Identification

In DaemonSet mode, use `{node}` for unique identification since `{index}` is not available:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: edge-federation
spec:
  mode: DaemonSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0

  supernodes:
    image: my-app/client:latest
    pools:
      - name: edge
        images: {}
        nodeConfig:
          # Use Kubernetes node name for unique identification
          name: "client_{node}"
          node-id: "{node}"
          pool: "{pool}"
```

### Static Configuration

You can also use `nodeConfig` for static values (same for all replicas):

```yaml
nodeConfig:
  dataset: "cifar10"
  batch-size: "32"
  learning-rate: "0.01"
```

## Resource Configuration

The operator supports fine-grained resource configuration for all containers in a Federation. Resource requirements help Kubernetes schedule pods appropriately and ensure fair resource allocation.

### Available Resource Fields

| Field | Component | Isolation Mode | Description |
|-------|-----------|----------------|-------------|
| `spec.superlink.resources` | SuperLink container | All | CPU/memory for SuperLink control plane |
| `spec.superlink.superexecResources` | SuperExec ServerApp sidecar | `process` only | CPU/memory for ServerApp execution |
| `spec.supernodes.pools[].supernodeResources` | SuperNode container | All | CPU/memory for SuperNode control plane |
| `spec.supernodes.pools[].superexecResources` | SuperExec ClientApp sidecar | `process` only | CPU/memory for ClientApp execution |

### SuperLink Resources

Configure resources for the SuperLink container and optionally for the SuperExec ServerApp sidecar (in process mode):

```yaml
superlink:
  image: flwr/superlink:1.25.0
  resources:
    requests:
      memory: "256Mi"
      cpu: "100m"
    limits:
      memory: "512Mi"
      cpu: "500m"
  # Process mode only: SuperExec ServerApp sidecar resources
  superexecResources:
    requests:
      memory: "128Mi"
      cpu: "50m"
    limits:
      memory: "256Mi"
      cpu: "200m"
  service:
    type: ClusterIP
```

### SuperNode Pool Resources

Configure resources per pool for the SuperNode container and optionally for the SuperExec ClientApp sidecar (in process mode):

```yaml
supernodes:
  image: flwr/supernode:1.25.0
  pools:
    - name: default
      replicas: 3
      supernodeResources:
        requests:
          memory: "512Mi"
          cpu: "200m"
        limits:
          memory: "1Gi"
          cpu: "1000m"
      # Process mode only: SuperExec ClientApp sidecar resources
      superexecResources:
        requests:
          memory: "256Mi"
          cpu: "100m"
        limits:
          memory: "512Mi"
          cpu: "500m"
```

### Multi-Pool with Different Resources

Different pools can have different resource requirements to match their workloads:

```yaml
supernodes:
  image: flwr/supernode:1.25.0
  pools:
    - name: cpu-pool
      replicas: 3
      supernodeResources:
        requests:
          cpu: "500m"
          memory: "512Mi"
        limits:
          cpu: "2000m"
          memory: "1Gi"
    
    - name: memory-pool
      replicas: 2
      supernodeResources:
        requests:
          cpu: "200m"
          memory: "2Gi"
        limits:
          cpu: "1000m"
          memory: "4Gi"
    
    - name: gpu-pool
      replicas: 2
      supernodeResources:
        requests:
          cpu: "1000m"
          memory: "2Gi"
        limits:
          cpu: "4000m"
          memory: "8Gi"
      gpu:
        enabled: true
        vendor: nvidia
        resourceName: nvidia.com/gpu
        count: 1
```

### Best Practices

**Requests vs Limits**:
- **Requests**: Minimum resources guaranteed to the container. Used by the scheduler to place pods.
- **Limits**: Maximum resources the container can use. Prevents resource exhaustion.

**GPU Workloads**:
- When using GPUs, allocate sufficient CPU and memory for GPU operations
- GPU pools typically need more CPU (1000m+) and memory (2Gi+) than CPU-only pools

**Resource Planning**:
- Start with conservative requests and adjust based on monitoring
- Set limits to prevent runaway processes from consuming cluster resources
- Consider node capacity when setting pool resource requirements

### Integration with GPU Configuration

Resource configuration works alongside GPU settings. When GPUs are enabled, ensure sufficient CPU and memory are allocated:

```yaml
pools:
  - name: nvidia-gpu-pool
    replicas: 2
    supernodeResources:
      requests:
        cpu: "1000m"
        memory: "2Gi"
      limits:
        cpu: "4000m"
        memory: "8Gi"
    gpu:
      enabled: true
      vendor: nvidia
      resourceName: nvidia.com/gpu
      count: 1
```

## Configuration Reference

### FederationSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `mode` | `DaemonSet` \| `StatefulSet` | Yes | How SuperNode pools are deployed |
| `isolationMode` | `process` \| `subprocess` | Yes | How ServerApp/ClientApp execution is handled |
| `version` | string | No | Convenience field for Flower version (for future use) |
| `insecure` | bool | No | Use insecure connections (default: `true`) |
| `superlink` | SuperLinkSpec | Yes | SuperLink configuration |
| `supernodes` | SuperNodesSpec | Yes | SuperNode pools configuration |

### SuperLinkSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | string | Yes | SuperLink container image |
| `superexecImage` | string | process mode | SuperExec sidecar image for ServerApp |
| `resources` | ResourceRequirements | No | Resources for SuperLink container |
| `superexecResources` | ResourceRequirements | No | Resources for SuperExec sidecar |
| `env` | []EnvVar | No | Environment variables |
| `extraArgs` | []string | No | Extra command-line arguments |
| `podTemplate` | PodTemplateSpec | No | Pod customization |
| `service` | SuperLinkServiceSpec | No | Service configuration |

### SuperLinkServiceSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | `ClusterIP` \| `NodePort` \| `LoadBalancer` | `ClusterIP` | Service type |
| `ports.grpc` | int32 | `9092` | GRPC port for SuperNode connections |
| `ports.serverAppIO` | int32 | `9091` | ServerAppIO port (process mode) |
| `ports.extra` | int32 | `9093` | Extra port (metrics, etc.) |
| `labels` | map[string]string | - | Additional service labels |
| `annotations` | map[string]string | - | Service annotations |

### SuperNodesSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | string | Yes | Default SuperNode image |
| `env` | []EnvVar | No | Default environment variables for all pools |
| `pools` | []SuperNodePoolSpec | Yes | List of SuperNode pools (1-16) |

### SuperNodePoolSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Pool name (unique, DNS-compatible) |
| `replicas` | int32 | StatefulSet mode | Number of replicas (must be >= 1) |
| `images.supernode` | string | No | Override SuperNode image |
| `images.superexecClientApp` | string | process mode | SuperExec ClientApp sidecar image |
| `gpu` | GPUConfig | No | GPU configuration |
| `supernodeResources` | ResourceRequirements | No | Resources for SuperNode container |
| `superexecResources` | ResourceRequirements | No | Resources for SuperExec sidecar |
| `podTemplate` | PodTemplateSpec | No | Pod customization |
| `extraArgs` | []string | No | Extra command-line arguments |
| `env` | []EnvVar | No | Environment variables (merged with defaults) |
| `nodeConfig` | map[string]string | No | Key-value pairs for `--node-config` flag (see [Node Configuration](#node-configuration)) |

### GPUConfig

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable GPU requests |
| `vendor` | `nvidia` \| `amd` | - | GPU vendor |
| `resourceName` | string | - | Extended resource name |
| `count` | int32 | `1` | GPUs per pod |
| `mountAll` | bool | `false` | Mount all GPUs on node |

## Examples

The `examples/` directory contains ready-to-use Federation configurations:

### DaemonSet with Process Isolation

[examples/federation-daemonset-process.yaml](examples/federation-daemonset-process.yaml)

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-daemonset-process
spec:
  mode: DaemonSet
  isolationMode: process

  superlink:
    image: flwr/superlink:1.25.0
    superexecImage: flwr/superexec:1.25.0
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        images:
          superexecClientApp: flwr/superexec:1.25.0
```

### StatefulSet with Subprocess Isolation

[examples/federation-statefulset-subprocess.yaml](examples/federation-statefulset-subprocess.yaml)

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-statefulset-subprocess
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0
    service:
      type: ClusterIP

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        replicas: 2
        images: {}
```

### Resource Configuration Example

[examples/federation-resources.yaml](examples/federation-resources.yaml) demonstrates comprehensive resource configuration:
- SuperLink with resources (process and subprocess modes)
- SuperExec sidecar resources (process mode)
- Multiple pools with different resource requirements (CPU, memory, GPU)
- Both DaemonSet and StatefulSet modes
- Resource configuration combined with GPU settings

### Node Configuration Examples

[examples/federation-nodeconfig.yaml](examples/federation-nodeconfig.yaml) - StatefulSet with data partitioning:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-nodeconfig
spec:
  mode: StatefulSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: default
        replicas: 4
        images: {}
        nodeConfig:
          partition-id: "{index}"
          num-partitions: "{replicas}"
          name: "client_{pool}_{index}"
```

[examples/federation-nodeconfig-daemonset.yaml](examples/federation-nodeconfig-daemonset.yaml) - DaemonSet with node identification:

```yaml
apiVersion: flwr.exalsius.ai/v1
kind: Federation
metadata:
  name: federation-nodeconfig-daemonset
spec:
  mode: DaemonSet
  isolationMode: subprocess

  superlink:
    image: flwr/superlink:1.25.0

  supernodes:
    image: flwr/supernode:1.25.0
    pools:
      - name: edge
        images: {}
        nodeConfig:
          name: "client_{node}"
          node-id: "{node}"
          pool: "{pool}"
```


## Usage Workflow

### 1. Deploy the Federation

```sh
kubectl apply -f my-federation.yaml
```

### 2. Verify the Status

```sh
# Check Federation status
kubectl get federation my-federation

# Detailed status
kubectl describe federation my-federation
```

### 3. List Created Resources

```sh
# SuperLink deployment
kubectl get deployment -l app.kubernetes.io/managed-by=flower-operator

# SuperNode workloads (DaemonSets or StatefulSets)
kubectl get daemonset,statefulset -l app.kubernetes.io/managed-by=flower-operator

# Services
kubectl get service -l app.kubernetes.io/managed-by=flower-operator
```

### 4. Get the SuperLink Endpoint

```sh
# From status
kubectl get federation my-federation -o jsonpath='{.status.endpoints.superlinkService}'

# Or from service directly
kubectl get service my-federation-superlink -o jsonpath='{.spec.clusterIP}'
```

### 5. Run a Training Job

```sh
# Create the Flower numpy quickstart example
flwr new @flwrlabs/quickstart-pytorch
cd quickstart-pytorch

# Adjust the pyproject.toml with the remote federation
[tool.flwr.federations.remote-federation]
address = "127.0.0.1:9093"
insecure = true

# Using port-forward for local development
kubectl port-forward service/my-federation-superlink 9093:9093

# In another terminal, start the flower run
flwr run . remote-federation --stream
```

### 6. Monitor the Federation

```sh
# Watch Federation status
kubectl get federation my-federation -w

# Check SuperNode pod logs
kubectl logs -l flower.flwr.ai/component=supernode -f

# Check SuperLink pod logs
kubectl logs -l flower.flwr.ai/component=superlink -f
```

## Status and Monitoring

### Status Fields

| Field | Description |
|-------|-------------|
| `observedGeneration` | Generation last processed by controller |
| `supernodes` | Aggregate availability (e.g., "2/3") |
| `readySupernodes` | Total ready SuperNodes across all pools |
| `desiredSupernodes` | Total desired SuperNodes across all pools |
| `endpoints.superlinkService` | SuperLink service address for `flwr run` |
| `pools` | Per-pool status (name, kind, desired, ready) |
| `conditions` | Standard Kubernetes conditions |

### kubectl Output

```sh
$ kubectl get federation
NAME            MODE          ISOLATION    SUPERNODES   READY   AGE
my-federation   StatefulSet   subprocess   2/2          True    5m
```

### Conditions

| Condition | Description |
|-----------|-------------|
| `Ready` | Federation is fully operational |
| `Reconciling` | Controller is processing changes |

### Per-Pool Status

```sh
$ kubectl get federation my-federation -o jsonpath='{.status.pools}' | jq
[
  {
    "name": "default",
    "kind": "StatefulSet",
    "desired": 2,
    "ready": 2
  }
]
```

## Troubleshooting

### Common Issues

#### Federation Not Ready

```sh
# Check conditions
kubectl describe federation my-federation

# Check operator logs
kubectl logs -n flower-operator-system deployment/flower-operator-controller-manager -c manager
```

#### Pools Not Scheduling

```sh
# Check pod events
kubectl describe pod -l flower.flwr.ai/federation=my-federation

# Common causes:
# - nodeSelector doesn't match any nodes
# - Insufficient resources
# - Taints without tolerations
```

#### GPU Not Available

```sh
# Verify GPU device plugin is running
kubectl get pods -n kube-system -l name=nvidia-device-plugin-ds

# Check node GPU capacity
kubectl describe node <node-name> | grep -A5 "Allocatable"
```

#### Validation Errors

The CRD enforces these rules via CEL validation:

| Error | Cause | Fix |
|-------|-------|-----|
| "replicas must not be set for pools when mode is DaemonSet" | Set replicas in DaemonSet mode | Remove `replicas` from pools |
| "replicas must be set (>=1) for every pool when mode is StatefulSet" | Missing replicas in StatefulSet mode | Add `replicas` to each pool |
| "spec.superlink.superexecImage is required when isolationMode=process" | Process mode without SuperExec image | Set `superexecImage` |
| "spec.supernodes.pools[*].images.superexecClientApp must be empty when isolationMode=subprocess" | Subprocess mode with SuperExec images | Remove `superexecClientApp` |

### Debugging Commands

```sh
# Federation details
kubectl describe federation my-federation

# Operator logs
kubectl logs -n flower-operator-system deployment/flower-operator-controller-manager -c manager -f

# Pod events
kubectl get events --field-selector involvedObject.kind=Pod -l flower.flwr.ai/federation=my-federation

# Resource status
kubectl get all -l flower.flwr.ai/federation=my-federation
```

## Uninstallation

### Remove Federation Resources

```sh
# Delete specific Federation
kubectl delete federation my-federation

# Or delete all Federations
kubectl delete federation --all
```

### Uninstall the Operator

```sh
# Delete CRDs (this also deletes all Federation resources!)
make uninstall

# Undeploy the controller
make undeploy
```

### Cleanup Namespace

```sh
kubectl delete namespace flower-operator-system
```

## Development

### Building

```sh
# Build the operator binary
make build

# Build the container image
make docker-build IMG=<registry>/flower-operator:tag
```

### Testing

```sh
# Run unit tests
make test

# Run linter
make lint

# Auto-fix lint issues
make lint-fix
```

### Running Locally

```sh
# Install CRDs
make install

# Run the controller locally (uses current kubeconfig)
make run
```

### Code Generation

After modifying `api/v1/federation_types.go`:

```sh
# Regenerate CRDs and RBAC
make manifests

# Regenerate DeepCopy methods
make generate
```

For detailed development guidelines, see [AGENTS.md](AGENTS.md).

## Contributing

Contributions are welcome! Please follow these guidelines:

1. Follow the [Kubebuilder](https://book.kubebuilder.io/) conventions
2. Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages
3. Ensure all tests pass: `make test`
4. Run linter before submitting: `make lint`

See [AGENTS.md](AGENTS.md) for detailed development practices and code structure.

## References

- [Flower Documentation](https://flower.ai/docs/)
- [Kubebuilder Book](https://book.kubebuilder.io/)
- [Kubernetes API Conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
- [controller-runtime FAQ](https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md)

## License

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
