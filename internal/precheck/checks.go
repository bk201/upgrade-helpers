package precheck

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"golang.org/x/sync/errgroup"
)

func Registry() []Check {
	return []Check{
		{ID: "certificates", Name: "Certificates", Scope: "node", Run: checkCertificates},
		{ID: "node-free-space", Name: "Node Free Space", Scope: "cluster", Run: checkFreeSpace},
		{ID: "helm-bundles", Name: "Helm Bundles", Scope: "cluster", Run: checkBundles},
		{ID: "harvester-bundle", Name: "Harvester Bundle", Scope: "cluster", Run: checkHarvesterBundle},
		{ID: "node-status", Name: "Node Status", Scope: "cluster", Run: checkNodes},
		{ID: "capi-cluster-state", Name: "CAPI Cluster State", Scope: "cluster", Run: checkClusterState},
		{ID: "capi-cluster-pause", Name: "CAPI Cluster Pause", Scope: "cluster", Run: checkClusterPause},
		{ID: "capi-machine-count", Name: "CAPI Machine Count", Scope: "cluster", Run: checkMachineCount},
		{ID: "capi-machine-state", Name: "CAPI Machine State", Scope: "cluster", Run: checkMachineState},
		{ID: "longhorn-volume-health", Name: "Longhorn Volume Health", Scope: "cluster", Run: checkVolumes},
		{ID: "stale-longhorn-volumes", Name: "Stale Longhorn Volumes", Scope: "cluster", Run: checkAttachedVolumes},
		{ID: "longhorn-backing-images", Name: "Longhorn Backing Images", Scope: "cluster", Run: checkBackingImages},
		{ID: "image-volume-size", Name: "Image Volume Size", Scope: "cluster", Run: checkImageVolumeSize},
		{ID: "virtual-machines", Name: "Virtual Machines", Scope: "cluster", Run: checkVirtualMachines},
		{ID: "pod-status", Name: "Pod Status", Scope: "cluster", Run: checkPods},
		{ID: "kubeconfig-secret", Name: "Kubeconfig Secret", Scope: "cluster", Run: checkKubeconfigSecret},
		{ID: "backup-target", Name: "Backup Target", Scope: "cluster", Run: checkBackupTarget},
		{ID: "storage-network-ip", Name: "Storage Network IP Availability", Scope: "cluster", Run: checkStorageNetworkIPs},
		{ID: "rwx-network-ip", Name: "RWX Network IP Availability", Scope: "cluster", Run: checkRWXNetworkIPs},
		{ID: "network-config", Name: "Network Configuration", Scope: "node", Run: checkNetworkConfigNodes},
		{ID: "cos-state-size", Name: "COS_STATE Partition Size", Scope: "node", Run: checkCOSStateNodes},
	}
}

func Run(ctx context.Context, env *Environment) Report {
	started := time.Now().UTC()
	checks := Registry()
	results := make([]Result, len(checks))
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index, registered := range checks {
		index, registered := index, registered
		group.Go(func() error {
			start := time.Now()
			result := executeCheck(groupContext, registered, env)
			result.ID = registered.ID
			result.Name = registered.Name
			result.Scope = registered.Scope
			result.DurationMillis = time.Since(start).Milliseconds()
			results[index] = result
			return nil
		})
	}
	_ = group.Wait()
	finished := time.Now().UTC()
	return Report{
		SchemaVersion:  "v1",
		ClusterVersion: env.Version.Raw,
		StartedAt:      started,
		FinishedAt:     finished,
		Summary:        Summarize(results),
		Checks:         results,
	}
}

func executeCheck(ctx context.Context, check Check, env *Environment) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = NewResult(Error, "check panicked", fmt.Sprintf("%v", recovered))
			env.Verbose("panic in %s: %s", check.ID, debug.Stack())
		}
	}()
	if err := ctx.Err(); err != nil {
		return NewResult(Error, "check canceled", err.Error())
	}
	return check.Run(ctx, env)
}
