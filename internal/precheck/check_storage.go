package precheck

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func checkVolumes(ctx context.Context, env *Environment) Result {
	nodes, err := env.Nodes(ctx)
	if err != nil {
		return NewResult(Error, "could not determine cluster size", err.Error())
	}
	if len(nodes) == 1 {
		return NewResult(Skipped, "single-node cluster")
	}
	volumes, err := env.List(ctx, "longhorn.io", "volumes", "longhorn-system", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect Longhorn volumes", err.Error())
	}
	var failures []string
	for _, volume := range volumes.Items {
		name := volume.GetName()
		desired := nestedInt(volume.Object, "spec", "numberOfReplicas")
		state := nestedString(volume.Object, "status", "state")
		robustness := nestedString(volume.Object, "status", "robustness")
		if desired == 1 {
			failures = append(failures, fmt.Sprintf("%s is a single-replica volume in %s state", name, state))
			continue
		}
		if robustness == "healthy" {
			continue
		}
		if state == "detached" {
			replicas, listErr := env.List(ctx, "longhorn.io", "replicas", "longhorn-system", metav1.ListOptions{LabelSelector: "longhornvolume=" + name})
			if listErr != nil {
				return NewResult(Error, "could not inspect Longhorn replicas", listErr.Error())
			}
			if int64(len(replicas.Items)) >= desired {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s is detached and has %d of %d required replicas", name, len(replicas.Items), desired))
			continue
		}
		failures = append(failures, fmt.Sprintf("%s is %s in %s state", name, robustness, state))
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "one or more Longhorn volumes require attention", failures...)
	}
	return NewResult(Pass, "all Longhorn volumes satisfy upgrade health requirements")
}

func checkAttachedVolumes(ctx context.Context, env *Environment) Result {
	volumes, err := env.List(ctx, "longhorn.io", "volumes", "longhorn-system", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect Longhorn volumes", err.Error())
	}
	var failures []string
	for _, volume := range volumes.Items {
		if nestedString(volume.Object, "status", "state") != "attached" {
			continue
		}
		workloads := objectSlice(volume.Object, "status", "kubernetesStatus", "workloadsStatus")
		if len(workloads) == 0 {
			failures = append(failures, fmt.Sprintf("%s is attached but has no workload", volume.GetName()))
			continue
		}
		running := false
		for _, item := range workloads {
			workload, _ := item.(map[string]any)
			if nestedString(workload, "podStatus") == "Running" {
				running = true
				break
			}
		}
		if !running {
			failures = append(failures, fmt.Sprintf("%s is attached but none of its workloads are running", volume.GetName()))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "stale attached Longhorn volumes were found", failures...)
	}
	return NewResult(Pass, "no stale attached Longhorn volumes were found")
}

func checkBackingImages(ctx context.Context, env *Environment) Result {
	if env.Version.Before(1, 4) {
		return NewResult(Skipped, "not applicable before v1.4")
	}
	images, err := env.List(ctx, "longhorn.io", "backingimages", "", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect Longhorn backing images", err.Error())
	}
	var missing, low []string
	for _, image := range images.Items {
		copies := nestedInt(image.Object, "spec", "minNumberOfCopies")
		switch {
		case copies == 0:
			missing = append(missing, namespacedName(image))
		case copies < 3:
			low = append(low, fmt.Sprintf("%s has %d copies", namespacedName(image), copies))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return NewResult(Fail, "backing images with zero minimum copies were found", missing...)
	}
	if len(low) > 0 {
		sort.Strings(low)
		return NewResult(Warning, "some backing images have fewer than three copies", low...)
	}
	return NewResult(Pass, "all backing images have at least three copies")
}

func checkImageVolumeSize(ctx context.Context, env *Environment) Result {
	images, err := env.List(ctx, "harvesterhci.io", "virtualmachineimages", "", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect virtual machine images", err.Error())
	}
	minimums := make(map[string]int64, len(images.Items))
	for _, image := range images.Items {
		virtual := nestedNumber(image.Object, "status", "virtualSize")
		artifact := nestedNumber(image.Object, "status", "size")
		if artifact > virtual {
			virtual = artifact
		}
		minimums[namespacedName(image)] = ceilGiB(virtual)
	}
	pvcs, err := env.Core.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect persistent volume claims", err.Error())
	}
	var failures []string
	for _, pvc := range pvcs.Items {
		key := pvc.Namespace + "/" + pvc.Name
		imageID := pvc.Annotations["harvesterhci.io/imageId"]
		if pvc.Annotations["harvesterhci.io/goldenImage"] == "true" {
			if _, ok := minimums[key]; ok {
				imageID = key
			}
		}
		minimum, ok := minimums[imageID]
		if !ok || minimum <= 0 {
			continue
		}
		quantity, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s has no storage request (source image %s requires %s)", key, imageID, resource.NewQuantity(minimum, resource.BinarySI)))
			continue
		}
		if quantity.Cmp(*resource.NewQuantity(minimum, resource.BinarySI)) < 0 {
			failures = append(failures, fmt.Sprintf("%s requests %s but source image %s requires at least %s", key, quantity.String(), imageID, resource.NewQuantity(minimum, resource.BinarySI).String()))
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "volumes smaller than their source VM images were found", failures...)
	}
	return NewResult(Pass, "all image-backed volumes are large enough")
}

func checkVirtualMachines(ctx context.Context, env *Environment) Result {
	setting, err := env.Get(ctx, "harvesterhci.io", "settings", "", "upgrade-config")
	if err != nil {
		return NewResult(Error, "could not inspect upgrade-config", err.Error())
	}
	restoreVM := false
	value := settingEffectiveValue(setting)
	if value != "" {
		var config map[string]any
		if err := jsonUnmarshal([]byte(value), &config); err != nil {
			return NewResult(Error, "upgrade-config contains invalid JSON", err.Error())
		}
		restoreVM, _ = config["restoreVM"].(bool)
	}
	if restoreVM {
		return NewResult(Pass, "restoreVM is enabled")
	}
	vms, err := env.List(ctx, "kubevirt.io", "virtualmachines", "", metav1.ListOptions{})
	if err != nil {
		return NewResult(Error, "could not inspect virtual machines", err.Error())
	}
	var failures []string
	for _, vm := range vms.Items {
		if nestedString(vm.Object, "status", "printableStatus") == "Stopped" {
			continue
		}
		devices := objectMap(vm.Object, "spec", "template", "spec", "domain", "devices")
		for _, item := range objectSlice(devices, "disks") {
			disk, _ := item.(map[string]any)
			if _, exists := disk["cdrom"]; exists {
				failures = append(failures, namespacedName(vm)+" is running with a CD-ROM")
				break
			}
		}
		if len(objectSlice(devices, "hostDevices")) > 0 {
			failures = append(failures, namespacedName(vm)+" is running with host devices")
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return NewResult(Fail, "running VMs have migration blockers while restoreVM is disabled", failures...)
	}
	return NewResult(Pass, "running VMs have no detected migration blockers")
}

// Kept behind a helper so tests can replace malformed JSON cases without relying on decoder globals.
var jsonUnmarshal = func(data []byte, value any) error {
	return json.Unmarshal(data, value)
}
