package precheck

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func checkBundles(ctx context.Context, env *Environment) Result {
	list, err := env.List(ctx, "fleet.cattle.io", "bundles", "", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect Fleet bundles", err.Error())
	}
	var pending []string
	for _, bundle := range list.Items {
		if bundle.GetName() == "mcc-harvester" || objectMap(bundle.Object, "spec", "helm") == nil {
			continue
		}
		if nestedInt(bundle.Object, "status", "summary", "ready") == 0 {
			pending = append(pending, namespacedName(bundle))
		}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		return NewResult(Fail, "non-ready Helm bundles were found", pending...)
	}
	return NewResult(Pass, "all Helm bundles are ready")
}

func checkHarvesterBundle(ctx context.Context, env *Environment) Result {
	bundle, err := env.Get(ctx, "fleet.cattle.io", "bundles", "fleet-local", "mcc-harvester")
	if err != nil {
		return NewResult(Error, "could not inspect mcc-harvester", err.Error())
	}
	desired := nestedInt(bundle.Object, "status", "summary", "desiredReady")
	ready := nestedInt(bundle.Object, "status", "summary", "ready")
	if desired != 1 || ready != 1 {
		return NewResult(Fail, "Harvester bundle is not ready", fmt.Sprintf("desiredReady=%d ready=%d", desired, ready))
	}
	return NewResult(Pass, "Harvester bundle is ready")
}

func checkNodes(ctx context.Context, env *Environment) Result {
	nodes, err := env.Nodes(ctx)
	if err != nil {
		return NewResult(Error, "could not inspect nodes", err.Error())
	}
	var failures, notes []string
	witnesses := 0
	for _, node := range nodes {
		if node.Spec.Unschedulable {
			failures = append(failures, fmt.Sprintf("node %s is unschedulable", node.Name))
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			failures = append(failures, fmt.Sprintf("node %s is not Ready", node.Name))
		}
		if node.Labels["node-role.harvesterhci.io/witness"] == "true" {
			witnesses++
			hasTaint := false
			for _, taint := range node.Spec.Taints {
				if taint.Key == "node-role.kubernetes.io/etcd" && taint.Value == "true" && taint.Effect == corev1.TaintEffectNoExecute {
					hasTaint = true
					break
				}
			}
			if !hasTaint {
				failures = append(failures, fmt.Sprintf("witness node %s lacks the etcd=true:NoExecute taint", node.Name))
			}
		}
	}
	if witnesses > 1 {
		failures = append(failures, fmt.Sprintf("cluster has %d witness nodes; at most one is supported", witnesses))
	}
	if witnesses == 1 {
		notes = append(notes, "cluster has one witness node")
	}

	if env.Version.IsMinor(1, 4) {
		longhornNodes, listErr := env.List(ctx, "longhorn.io", "nodes", "longhorn-system", metav1.ListOptions{})
		if listErr != nil {
			return NewResult(Error, "could not inspect Longhorn eviction state", listErr.Error())
		}
		for _, node := range longhornNodes.Items {
			if nestedBool(node.Object, "spec", "evictionRequested") {
				failures = append(failures, fmt.Sprintf("Longhorn node %s has evictionRequested=true", node.GetName()))
			}
			for diskName, value := range objectMap(node.Object, "spec", "disks") {
				disk, _ := value.(map[string]any)
				if nestedBool(disk, "evictionRequested") {
					failures = append(failures, fmt.Sprintf("Longhorn disk %s on node %s has evictionRequested=true", diskName, node.GetName()))
				}
			}
		}
	}
	sort.Strings(failures)
	sort.Strings(notes)
	if len(failures) > 0 {
		return NewResult(Fail, "one or more nodes require attention", append(failures, notes...)...)
	}
	return NewResult(Pass, "all nodes are ready and schedulable", notes...)
}

func checkClusterState(ctx context.Context, env *Environment) Result {
	cluster, err := env.Get(ctx, "cluster.x-k8s.io", "clusters", "fleet-local", "local")
	if err != nil {
		return NewResult(Error, "could not inspect the CAPI cluster", err.Error())
	}
	phase := nestedString(cluster.Object, "status", "phase")
	if phase != "Provisioned" {
		return NewResult(Fail, "CAPI cluster is not provisioned", "phase="+phase)
	}
	return NewResult(Pass, "CAPI cluster is provisioned")
}

func checkClusterPause(ctx context.Context, env *Environment) Result {
	cluster, err := env.Get(ctx, "cluster.x-k8s.io", "clusters", "fleet-local", "local")
	if err != nil {
		return NewResult(Error, "could not inspect the CAPI cluster", err.Error())
	}
	if nestedBool(cluster.Object, "spec", "paused") {
		return NewResult(Fail, "CAPI cluster is paused", "clear spec.paused on fleet-local/local before upgrading")
	}
	return NewResult(Pass, "CAPI cluster is not paused")
}

func checkMachineCount(ctx context.Context, env *Environment) Result {
	machines, err := env.List(ctx, "cluster.x-k8s.io", "machines", "fleet-local", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect CAPI machines", err.Error())
	}
	nodes, err := env.Nodes(ctx)
	if err != nil {
		return NewResult(Error, "could not inspect nodes", err.Error())
	}
	if len(machines.Items) != len(nodes) {
		return NewResult(Fail, "CAPI machine count does not match node count", fmt.Sprintf("machines=%d nodes=%d", len(machines.Items), len(nodes)))
	}
	return NewResult(Pass, "CAPI machine count matches node count")
}

func checkMachineState(ctx context.Context, env *Environment) Result {
	machines, err := env.List(ctx, "cluster.x-k8s.io", "machines", "fleet-local", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect CAPI machines", err.Error())
	}
	var failures []string
	for _, machine := range machines.Items {
		phase := nestedString(machine.Object, "status", "phase")
		if phase != "Running" {
			failures = append(failures, fmt.Sprintf("%s phase is %q", machine.GetName(), phase))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "one or more CAPI machines are not running", failures...)
	}
	return NewResult(Pass, "all CAPI machines are running")
}

func checkPods(ctx context.Context, env *Environment) Result {
	pods, err := env.Core.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect pods", err.Error())
	}
	var failures []string
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionFalse && condition.Reason != "PodCompleted" {
				failures = append(failures, pod.Namespace+"/"+pod.Name)
				break
			}
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "non-ready pods were found", failures...)
	}
	return NewResult(Pass, "all pods are ready or completed")
}

func checkKubeconfigSecret(ctx context.Context, env *Environment) Result {
	secret, err := env.Core.CoreV1().Secrets("fleet-local").Get(ctx, "local-kubeconfig", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return NewResult(Fail, "fleet-local/local-kubeconfig does not exist")
		}
		return NewResult(Error, "could not inspect fleet-local/local-kubeconfig", err.Error())
	}
	if secret.Labels["cluster.x-k8s.io/cluster-name"] != "local" {
		return NewResult(Fail, "local-kubeconfig lacks the required cluster label", "set cluster.x-k8s.io/cluster-name=local")
	}
	return NewResult(Pass, "local-kubeconfig has the required cluster label")
}

func checkBackupTarget(ctx context.Context, env *Environment) Result {
	if !(env.Version.Major == 1 && env.Version.Minor == 4 && (env.Version.Patch == 1 || env.Version.Patch == 2)) {
		return NewResult(Skipped, "only applicable to v1.4.1 and v1.4.2")
	}
	setting, err := env.Get(ctx, "harvesterhci.io", "settings", "", "backup-target")
	if err != nil {
		return NewResult(Error, "could not inspect backup-target", err.Error())
	}
	value := settingEffectiveValue(setting)
	if value == "" {
		return NewResult(Pass, "backup target is not configured")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return NewResult(Error, "backup-target contains invalid JSON", err.Error())
	}
	typeName := fmt.Sprint(config["type"])
	if typeName == "" || typeName == "<nil>" {
		return NewResult(Pass, "backup target is not configured")
	}
	refresh := int64(0)
	switch value := config["refreshIntervalInSeconds"].(type) {
	case float64:
		refresh = int64(value)
	case json.Number:
		refresh, _ = value.Int64()
	}
	if refresh <= 0 {
		return NewResult(Fail, "backup target refresh interval must be greater than zero", "a value such as 300 is recommended")
	}
	return NewResult(Pass, "backup target refresh interval is valid")
}

func checkFreeSpace(ctx context.Context, env *Environment) Result {
	nodes, err := env.Nodes(ctx)
	if err != nil {
		return NewResult(Error, "could not inspect nodes", err.Error())
	}
	const additionalBytes = uint64(13 * 1024 * 1024 * 1024)
	var failures, warnings []string
	for _, node := range nodes {
		raw, requestErr := env.Core.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + node.Name + "/proxy/stats/summary").DoRaw(ctx)
		if requestErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: kubelet filesystem stats unavailable: %v", node.Name, requestErr))
			continue
		}
		var stats struct {
			Node struct {
				FS struct {
					UsedBytes     *uint64 `json:"usedBytes"`
					CapacityBytes *uint64 `json:"capacityBytes"`
				} `json:"fs"`
			} `json:"node"`
		}
		if err := json.Unmarshal(raw, &stats); err != nil || stats.Node.FS.UsedBytes == nil || stats.Node.FS.CapacityBytes == nil || *stats.Node.FS.CapacityBytes == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: kubelet returned invalid filesystem stats", node.Name))
			continue
		}
		used, capacity := *stats.Node.FS.UsedBytes, *stats.Node.FS.CapacityBytes
		if float64(used+additionalBytes)/float64(capacity) > 0.85 {
			failures = append(failures, fmt.Sprintf("%s: used %.2f GiB of %.2f GiB; loading 13 GiB would exceed 85%%", node.Name, float64(used)/(1<<30), float64(capacity)/(1<<30)))
		}
	}
	sort.Strings(failures)
	sort.Strings(warnings)
	if len(failures) > 0 {
		return NewResult(Fail, "one or more nodes lack upgrade image space", append(failures, warnings...)...)
	}
	if len(warnings) > 0 {
		return NewResult(Warning, "some nodes could not be evaluated", warnings...)
	}
	return NewResult(Pass, "all nodes have enough space for upgrade images")
}

func joinNames(values []string) string { return strings.Join(values, ", ") }
