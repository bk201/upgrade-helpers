package precheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

const validatorNamespace = "harvester-system"

type validatorMount struct {
	Name, HostPath, MountPath string
}

func checkCertificates(ctx context.Context, env *Environment) Result {
	return runNodeValidator(ctx, env, "certificates", map[string]string{"node-role.kubernetes.io/control-plane": "true"}, []validatorMount{{"rke2-tls", "/var/lib/rancher/rke2/server/tls", "/host/var/lib/rancher/rke2/server/tls"}})
}

func checkNetworkConfigNodes(ctx context.Context, env *Environment) Result {
	if !env.Version.IsMinor(1, 6) {
		return NewResult(Skipped, "only applicable when upgrading from v1.6")
	}
	return runNodeValidator(ctx, env, "network-config", nil, []validatorMount{{"oem", "/oem", "/host/oem"}})
}

func checkCOSStateNodes(ctx context.Context, env *Environment) Result {
	if !env.Version.IsMinor(1, 7) {
		return NewResult(Skipped, "only applicable when upgrading from v1.7")
	}
	return runNodeValidator(ctx, env, "cos-state-size", nil, []validatorMount{{"run-initramfs", "/run/initramfs", "/host/run/initramfs"}})
}

func runNodeValidator(ctx context.Context, env *Environment, checkID string, selector map[string]string, mounts []validatorMount) Result {
	if env.ValidatorImage == "" {
		return NewResult(Error, "validator image is not configured")
	}
	nodes, err := env.Nodes(ctx)
	if err != nil {
		return NewResult(Error, "could not determine expected validator nodes", err.Error())
	}
	var expected []string
	for _, node := range nodes {
		matches := true
		for key, value := range selector {
			if node.Labels[key] != value {
				matches = false
				break
			}
		}
		if matches {
			expected = append(expected, node.Name)
		}
	}
	sort.Strings(expected)
	if len(expected) == 0 {
		return NewResult(Error, "no nodes match the validator scope")
	}

	runID, err := randomID()
	if err != nil {
		return NewResult(Error, "could not create validator run ID", err.Error())
	}
	name := "upgrade-precheck-" + strings.ReplaceAll(checkID, "_", "-") + "-" + runID
	labels := map[string]string{"app.kubernetes.io/name": "harvester-precheck", "harvesterhci.io/precheck-run": runID, "harvesterhci.io/precheck-id": checkID}
	daemonSet := validatorDaemonSet(name, labels, selector, env.ValidatorImage, checkID, mounts)
	if _, err := env.Core.AppsV1().DaemonSets(validatorNamespace).Create(ctx, daemonSet, metav1.CreateOptions{}); err != nil {
		return NewResult(Error, "could not create node validator DaemonSet", err.Error())
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		policy := metav1.DeletePropagationBackground
		if deleteErr := env.Core.AppsV1().DaemonSets(validatorNamespace).Delete(cleanupContext, name, metav1.DeleteOptions{PropagationPolicy: &policy}); deleteErr != nil {
			env.Verbose("delete validator DaemonSet %s: %v", name, deleteErr)
		}
	}()

	deadline := time.NewTimer(env.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	selectorString := fields.OneTermEqualSelector("harvesterhci.io/precheck-run", runID).String()
	for {
		results, complete := collectNodeResults(ctx, env, selectorString, checkID, expected)
		if complete {
			return aggregateNodeResults(results)
		}
		select {
		case <-ctx.Done():
			return NewResult(Error, "node validation was canceled", ctx.Err().Error())
		case <-deadline.C:
			missing := missingNodeNames(results, expected)
			details := []string{"validator timed out after " + env.Timeout.String()}
			if len(missing) > 0 {
				details = append(details, "missing results from: "+strings.Join(missing, ", "))
			}
			result := NewResult(Error, "node validation did not complete on every expected node", details...)
			result.Nodes = results
			return result
		case <-ticker.C:
		}
	}
}

func validatorDaemonSet(name string, labels, selector map[string]string, image, checkID string, mounts []validatorMount) *appsv1.DaemonSet {
	volumes := make([]corev1.Volume, 0, len(mounts))
	volumeMounts := make([]corev1.VolumeMount, 0, len(mounts))
	hostPathType := corev1.HostPathDirectory
	for _, mount := range mounts {
		volumes = append(volumes, corev1.Volume{Name: mount.Name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: mount.HostPath, Type: &hostPathType}}})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: mount.Name, MountPath: mount.MountPath, ReadOnly: true})
	}
	falseValue := false
	trueValue := true
	zero := int64(0)
	nobody := int64(65532)
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: &falseValue, Privileged: &falseValue, ReadOnlyRootFilesystem: &trueValue, RunAsUser: &zero, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	holdSecurity := security.DeepCopy()
	holdSecurity.RunAsUser = &nobody
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: validatorNamespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeSelector:                  selector,
					Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, {Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}},
					TerminationGracePeriodSeconds: ptr(int64(1)),
					AutomountServiceAccountToken:  &falseValue,
					InitContainers:                []corev1.Container{{Name: "node-check", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Args: []string{"node-check", "--checks=" + checkID, "--root=/host"}, Env: []corev1.EnvVar{{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}}}, SecurityContext: security, VolumeMounts: volumeMounts}},
					Containers:                    []corev1.Container{{Name: "hold", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Args: []string{"hold"}, SecurityContext: holdSecurity, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/usr/local/bin/harvester-precheck", "health"}}}, InitialDelaySeconds: 0, PeriodSeconds: 2, TimeoutSeconds: 1, SuccessThreshold: 1, FailureThreshold: 3}}},
					Volumes:                       volumes,
				},
			},
		},
	}
}

func collectNodeResults(ctx context.Context, env *Environment, selector, checkID string, expected []string) ([]NodeResult, bool) {
	pods, err := env.Core.CoreV1().Pods(validatorNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, false
	}
	byNode := make(map[string]NodeResult)
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			continue
		}
		finished := false
		for _, status := range pod.Status.InitContainerStatuses {
			if status.Name == "node-check" && status.State.Terminated != nil {
				finished = true
				break
			}
		}
		if !finished {
			continue
		}
		raw, logErr := env.Core.CoreV1().Pods(validatorNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "node-check"}).DoRaw(ctx)
		if logErr != nil {
			byNode[pod.Spec.NodeName] = NodeResult{Node: pod.Spec.NodeName, Status: Error, Summary: "could not read validator output", Details: []string{logErr.Error()}}
			continue
		}
		var envelope NodeEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			byNode[pod.Spec.NodeName] = NodeResult{Node: pod.Spec.NodeName, Status: Error, Summary: "validator output is malformed", Details: []string{err.Error()}}
			continue
		}
		if len(envelope.Results) != 1 {
			byNode[pod.Spec.NodeName] = NodeResult{Node: pod.Spec.NodeName, Status: Error, Summary: "validator returned an unexpected number of results", Details: []string{checkID}}
			continue
		}
		result := envelope.Results[0]
		result.Node = pod.Spec.NodeName
		if !validStatus(result.Status) || result.Summary == "" {
			byNode[pod.Spec.NodeName] = NodeResult{Node: pod.Spec.NodeName, Status: Error, Summary: "validator returned an invalid result", Details: []string{checkID}}
			continue
		}
		byNode[pod.Spec.NodeName] = result
	}
	results := make([]NodeResult, 0, len(byNode))
	complete := true
	for _, node := range expected {
		result, ok := byNode[node]
		if !ok {
			complete = false
			continue
		}
		results = append(results, result)
	}
	stableNodeResults(results)
	return results, complete
}

func aggregateNodeResults(nodes []NodeResult) Result {
	statuses := make([]Status, 0, len(nodes))
	for _, node := range nodes {
		statuses = append(statuses, node.Status)
	}
	status := worstStatus(statuses...)
	summary := "all expected nodes passed"
	switch status {
	case Warning:
		summary = "node validation completed with warnings"
	case Fail:
		summary = "one or more nodes failed validation"
	case Error:
		summary = "one or more nodes could not be evaluated"
	}
	result := NewResult(status, summary)
	result.Nodes = nodes
	return result
}

func missingNodeNames(results []NodeResult, expected []string) []string {
	found := make(map[string]bool, len(results))
	for _, result := range results {
		found[result.Node] = true
	}
	var missing []string
	for _, node := range expected {
		if !found[node] {
			missing = append(missing, node)
		}
	}
	return missing
}

func randomID() (string, error) {
	data := make([]byte, 5)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func ptr[T any](value T) *T { return &value }
