# Shell-to-Go check mapping

This document maps the checks from the former `check.sh` implementation to the
Go pre-check program. The names in the **Registry ID** column are the stable IDs
used in JSON reports.

All checks are registered in
[`Registry`](internal/precheck/checks.go#L12). The runner executes up to four
checks concurrently, retains registry order in the final report, and converts
each result to `PASS`, `WARNING`, `FAIL`, `SKIPPED`, or `ERROR`. Kubernetes API
failures are reported as `ERROR` instead of terminating the whole program as
`bash -e` could do.

| Original shell function | Registry ID | Go implementation | Execution location |
| --- | --- | --- | --- |
| `check_certs` | `certificates` | `checkCertificates`, `localCertificates` | Validator pods on every control-plane node |
| `check_free_space` | `node-free-space` | `checkFreeSpace` | Kubernetes API and kubelet proxy |
| `check_bundles` | `helm-bundles` | `checkBundles` | Kubernetes API |
| `check_harvester_bundle` | `harvester-bundle` | `checkHarvesterBundle` | Kubernetes API |
| `check_nodes` | `node-status` | `checkNodes` | Kubernetes API |
| `check_cluster` | `capi-cluster-state` | `checkClusterState` | Kubernetes API |
| `check_cluster_pause` | `capi-cluster-pause` | `checkClusterPause` | Kubernetes API |
| `check_machines` | `capi-machine-count`, `capi-machine-state` | `checkMachineCount`, `checkMachineState` | Kubernetes API |
| `check_volumes` | `longhorn-volume-health` | `checkVolumes` | Kubernetes API |
| `check_attached_volumes` | `stale-longhorn-volumes` | `checkAttachedVolumes` | Kubernetes API |
| `check_images` | `longhorn-backing-images` | `checkBackingImages` | Kubernetes API |
| `check_image_volume_size` | `image-volume-size` | `checkImageVolumeSize` | Kubernetes API |
| `check_virtual_machines_live_migration` | `virtual-machines` | `checkVirtualMachines` | Kubernetes API |
| `check_error_pods` | `pod-status` | `checkPods` | Kubernetes API |
| `check_kubeconfig_secret` | `kubeconfig-secret` | `checkKubeconfigSecret` | Kubernetes API |
| `check_backup_target` | `backup-target` | `checkBackupTarget` | Kubernetes API |
| `check_storage_network_ip_availability` | `storage-network-ip` | `checkStorageNetworkIPs` | Kubernetes API |
| `check_rwx_network_ip_availability` | `rwx-network-ip` | `checkRWXNetworkIPs` | Kubernetes API |

## Check implementations

### `check_certs`

Implemented by
[`checkCertificates`](internal/precheck/node_daemonset.go#L24) and
[`localCertificates`](internal/precheck/node_local.go#L40).

The CLI creates a temporary DaemonSet restricted to nodes labeled
`node-role.kubernetes.io/control-plane=true`. Its init container mounts each
node's `/var/lib/rancher/rke2/server/tls` directory read-only and parses every
`.crt` file with Go's `crypto/x509` package. Expired certificates produce
`FAIL`; certificates expiring within ten days produce `WARNING`; unreadable or
malformed certificates produce `ERROR`.

Unlike the shell function, this validates every control-plane node instead of
only the machine on which the command was launched. The CLI requires a
structured result from every expected node and always attempts to remove the
temporary DaemonSet.

### `check_free_space`

Implemented by [`checkFreeSpace`](internal/precheck/check_cluster.go#L241).

The check lists nodes with the typed Kubernetes client and requests each
kubelet's `/stats/summary` endpoint through `nodes/proxy`. It retains the shell
formula: adding 13 GiB to current usage must not exceed 85 percent of filesystem
capacity. Insufficient space produces `FAIL`. Missing or invalid kubelet stats
produce `WARNING` because the capacity condition could not be confirmed for
that node.

### `check_bundles`

Implemented by [`checkBundles`](internal/precheck/check_cluster.go#L15).

The check lists Fleet Bundle custom resources across all namespaces. Bundles
without Helm configuration and the special `mcc-harvester` bundle are excluded.
Any remaining bundle whose `status.summary.ready` is zero is included in a
sorted failure list.

### `check_harvester_bundle`

Implemented by
[`checkHarvesterBundle`](internal/precheck/check_cluster.go#L36).

The check gets `fleet-local/mcc-harvester` directly and requires both
`status.summary.desiredReady` and `status.summary.ready` to equal one. Missing
or unreadable bundle data is an `ERROR`; a readable but non-ready summary is a
`FAIL`.

### `check_nodes`

Implemented by [`checkNodes`](internal/precheck/check_cluster.go#L49).

Using typed Node objects, the check verifies that every node is schedulable and
has a `Ready=True` condition. It also allows at most one witness node and
requires a witness to carry the `node-role.kubernetes.io/etcd=true:NoExecute`
taint.

For Harvester v1.4, the same result also checks Longhorn Node resources. Node
and disk `spec.evictionRequested` values must be false. This replaces the
shell's nested `check_longhorn_eviction_status` call without allowing that
sub-check to exit the entire process.

### `check_cluster`

Implemented by
[`checkClusterState`](internal/precheck/check_cluster.go#L116).

The check gets CAPI Cluster `fleet-local/local` and requires
`status.phase=Provisioned`.

### `check_cluster_pause`

Implemented by
[`checkClusterPause`](internal/precheck/check_cluster.go#L128).

The check gets the same CAPI Cluster and fails when `spec.paused` is true,
including guidance to clear the field before upgrading.

### `check_machines`

Split into two independently reported checks:

- [`checkMachineCount`](internal/precheck/check_cluster.go#L139) compares the
  number of CAPI Machines in `fleet-local` with the number of Kubernetes Nodes.
- [`checkMachineState`](internal/precheck/check_cluster.go#L154) requires every
  CAPI Machine to have `status.phase=Running`.

The shared API-list cache means concurrent consumers reuse the same Machine and
Node snapshots. No temporary files or piped subshell state are needed.

### `check_volumes`

Implemented by [`checkVolumes`](internal/precheck/check_storage.go#L14).

The check remains skipped for a single-node cluster. Otherwise, it evaluates
all Longhorn Volume resources directly:

- A single-replica volume fails because it can block or make node draining
  unsafe.
- A healthy multi-replica volume passes.
- A detached non-healthy volume is accepted only when its actual replica count
  is at least its requested replica count.
- Other non-healthy volumes fail.

Inspecting Volume resources directly avoids duplicate work and avoids missing a
volume merely because no matching Engine happened to be returned.

### `check_attached_volumes`

Implemented by
[`checkAttachedVolumes`](internal/precheck/check_storage.go#L59).

For each attached Longhorn Volume, the check reads
`status.kubernetesStatus.workloadsStatus`. It fails a volume with no recorded
workloads or with no workload whose pod status is `Running`. This retains the
live-migration behavior where a succeeded source pod is acceptable when a new
running pod is also present.

### `check_images`

Implemented by
[`checkBackingImages`](internal/precheck/check_storage.go#L93).

This check is skipped before Harvester v1.4. A Longhorn BackingImage with
`minNumberOfCopies=0` produces `FAIL`; a value of one or two produces
`WARNING`; three or more passes. The explicit warning status replaces the
shell's warning-shaped text embedded in an otherwise unstructured run.

### `check_image_volume_size`

Implemented by
[`checkImageVolumeSize`](internal/precheck/check_storage.go#L122).

The check lists Harvester VirtualMachineImages and Kubernetes PVCs. For each
image, the minimum size is the greater of `status.virtualSize` and
`status.size`, rounded up to a whole GiB. Golden-image PVCs and PVCs carrying a
`harvesterhci.io/imageId` annotation are matched to that minimum. PVC requests
are parsed with Kubernetes `resource.Quantity`; undersized PVCs fail with their
current and required sizes.

### `check_virtual_machines_live_migration`

Implemented by
[`checkVirtualMachines`](internal/precheck/check_storage.go#L169).

The check parses the effective Harvester `upgrade-config` setting. When
`restoreVM` is false, every non-stopped KubeVirt VirtualMachine is inspected for
CD-ROM disks and host devices. Either condition produces `FAIL`. The Go object
walk correctly associates each finding with its VM, fixing the shell
expression that could lose VM metadata while traversing host devices.

### `check_error_pods`

Implemented by [`checkPods`](internal/precheck/check_cluster.go#L173).

The typed Pod client lists every namespace. Pods with a `Ready=False` condition
fail unless the condition reason is `PodCompleted`. Failing pod names are
reported as sorted `namespace/name` values.

### `check_kubeconfig_secret`

Implemented by
[`checkKubeconfigSecret`](internal/precheck/check_cluster.go#L194).

The check gets `fleet-local/local-kubeconfig` and requires the label
`cluster.x-k8s.io/cluster-name=local`. A missing secret or label is `FAIL`; API
access failures are `ERROR`.

### `check_backup_target`

Implemented by
[`checkBackupTarget`](internal/precheck/check_cluster.go#L208).

The check only applies to v1.4.1 and v1.4.2. It parses the effective
`backup-target` setting as JSON. An absent target passes; a configured target
requires `refreshIntervalInSeconds` greater than zero. Other versions return
`SKIPPED`.

### `check_storage_network_ip_availability`

Implemented by
[`checkStorageNetworkIPs`](internal/precheck/check_network.go#L157), with shared
CIDR and allocation logic in
[`availableNetworkIPs`](internal/precheck/check_network.go#L94).

The check reads the effective `storage-network` and optional `rwx-network`
settings. For older clusters without `rwx-network`, it falls back to the
Longhorn `storage-network-for-rwx-volume-enabled` setting. Required free
addresses equal the number of Longhorn InstanceManagers plus
BackingImageManagers; one additional address is required when RWX shares the
storage network.

The configured IPv4 CIDR and exclusions are parsed with `net/netip`. Overlapping
exclusions are merged before subtraction, and current allocations are counted
from the matching Whereabouts IPPool. Invalid or unsupported ranges return
`ERROR` instead of relying on shell integer arithmetic.

### `check_rwx_network_ip_availability`

Implemented by
[`checkRWXNetworkIPs`](internal/precheck/check_network.go#L200).

The check is skipped when RWX is unconfigured or shares the storage network.
For a dedicated RWX network, it extracts the network configuration from the
`rwx-network` setting and requires one available Whereabouts address for the
temporary upgrade repository volume. It uses the same validated CIDR,
exclusion, allocation, and IPPool-name logic as the storage-network check.

## Shared Kubernetes access

Core resources use typed `client-go` clients. CRDs use the dynamic client and
the cluster's discovery API to select the preferred served version; no Fleet,
CAPI, Harvester, Longhorn, KubeVirt, or Whereabouts version is hard-coded into
the check logic. The implementation lives in
[`environment.go`](internal/precheck/environment.go).

The original `check_host` function is intentionally not mapped to a result. Its
local-host restriction is obsolete because node-local checks now run through
DaemonSets. CLI startup instead validates kubeconfig loading, API access, CRD
discovery, and Harvester server-version retrieval before running the registry.
