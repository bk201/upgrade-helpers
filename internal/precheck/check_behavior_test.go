package precheck

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

type staticResolver map[schema.GroupResource]schema.GroupVersionResource

func (r staticResolver) Resolve(resource schema.GroupResource) (schema.GroupVersionResource, error) {
	return r[resource], nil
}

func testEnvironment(t *testing.T, coreObjects []runtime.Object, dynamicObjects []runtime.Object, resources map[schema.GroupResource]schema.GroupVersionResource, listKinds map[schema.GroupVersionResource]string) *Environment {
	t.Helper()
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, dynamicObjects...)
	return NewEnvironment(fake.NewSimpleClientset(coreObjects...), dynamicClient, staticResolver(resources))
}

func TestBundleChecks(t *testing.T) {
	bundleGVR := schema.GroupVersionResource{Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "bundles"}
	objects := []runtime.Object{
		object(bundleGVR, "fleet-local", "mcc-harvester", map[string]any{"spec": map[string]any{"helm": map[string]any{}}, "status": map[string]any{"summary": map[string]any{"ready": int64(0), "desiredReady": int64(1)}}}),
		object(bundleGVR, "fleet-default", "ready", map[string]any{"spec": map[string]any{"helm": map[string]any{}}, "status": map[string]any{"summary": map[string]any{"ready": int64(1)}}}),
		object(bundleGVR, "fleet-default", "pending", map[string]any{"spec": map[string]any{"helm": map[string]any{}}, "status": map[string]any{"summary": map[string]any{"ready": int64(0)}}}),
	}
	env := testEnvironment(t, nil, objects, staticResolver{bundleGVR.GroupResource(): bundleGVR}, map[schema.GroupVersionResource]string{bundleGVR: "BundleList"})
	if result := checkBundles(context.Background(), env); result.Status != Fail || len(result.Details) != 1 || result.Details[0] != "fleet-default/pending" {
		t.Fatalf("unexpected Helm bundle result: %+v", result)
	}
	if result := checkHarvesterBundle(context.Background(), env); result.Status != Fail {
		t.Fatalf("unexpected Harvester bundle result: %+v", result)
	}
}

func TestNodeCheckDetectsWitnessAndReadinessFailures(t *testing.T) {
	nodes := []runtime.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"node-role.harvesterhci.io/witness": "true"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2", Labels: map[string]string{"node-role.harvesterhci.io/witness": "true"}}, Spec: corev1.NodeSpec{Unschedulable: true}},
	}
	env := testEnvironment(t, nodes, nil, nil, nil)
	env.Version = ClusterVersion{Raw: "v1.8.0", Major: 1, Minor: 8}
	result := checkNodes(context.Background(), env)
	if result.Status != Fail || len(result.Details) < 3 {
		t.Fatalf("unexpected node result: %+v", result)
	}
}

func TestBackingImageStatuses(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "backingimages"}
	tests := []struct {
		name       string
		copies     int64
		wantStatus Status
	}{
		{name: "zero fails", copies: 0, wantStatus: Fail},
		{name: "low warns", copies: 2, wantStatus: Warning},
		{name: "three passes", copies: 3, wantStatus: Pass},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := testEnvironment(t, nil, []runtime.Object{object(gvr, "longhorn-system", "image", map[string]any{"spec": map[string]any{"minNumberOfCopies": test.copies}})}, staticResolver{gvr.GroupResource(): gvr}, map[schema.GroupVersionResource]string{gvr: "BackingImageList"})
			env.Version = ClusterVersion{Raw: "v1.8.0", Major: 1, Minor: 8}
			if got := checkBackingImages(context.Background(), env); got.Status != test.wantStatus {
				t.Fatalf("got %+v, want %s", got, test.wantStatus)
			}
		})
	}
}

func TestImageVolumeSizeUsesKubernetesQuantities(t *testing.T) {
	imageGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "disk", Namespace: "default", Annotations: map[string]string{"harvesterhci.io/imageId": "default/image"}},
		Spec:       corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("9Gi")}}},
	}
	image := object(imageGVR, "default", "image", map[string]any{"status": map[string]any{"virtualSize": float64(9*1024*1024*1024 + 1)}})
	env := testEnvironment(t, []runtime.Object{pvc}, []runtime.Object{image}, staticResolver{imageGVR.GroupResource(): imageGVR}, map[schema.GroupVersionResource]string{imageGVR: "VirtualMachineImageList"})
	result := checkImageVolumeSize(context.Background(), env)
	if result.Status != Fail || len(result.Details) != 1 {
		t.Fatalf("unexpected image volume result: %+v", result)
	}
}

func TestVirtualMachineHostDeviceIsDetected(t *testing.T) {
	settingGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "settings"}
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	setting := object(settingGVR, "", "upgrade-config", map[string]any{"value": `{"restoreVM":false}`})
	vm := object(vmGVR, "default", "vm", map[string]any{
		"status": map[string]any{"printableStatus": "Running"},
		"spec":   map[string]any{"template": map[string]any{"spec": map[string]any{"domain": map[string]any{"devices": map[string]any{"hostDevices": []any{map[string]any{"name": "gpu"}}}}}}},
	})
	env := testEnvironment(t, nil, []runtime.Object{setting, vm}, staticResolver{settingGVR.GroupResource(): settingGVR, vmGVR.GroupResource(): vmGVR}, map[schema.GroupVersionResource]string{vmGVR: "VirtualMachineList"})
	result := checkVirtualMachines(context.Background(), env)
	if result.Status != Fail || len(result.Details) != 1 {
		t.Fatalf("unexpected VM result: %+v", result)
	}
}

func TestCAPIChecks(t *testing.T) {
	clusterGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	machineGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "machines"}
	cluster := object(clusterGVR, "fleet-local", "local", map[string]any{"spec": map[string]any{"paused": true}, "status": map[string]any{"phase": "Pending"}})
	machine := object(machineGVR, "fleet-local", "machine-1", map[string]any{"status": map[string]any{"phase": "Provisioning"}})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	env := testEnvironment(t, []runtime.Object{node}, []runtime.Object{cluster, machine}, staticResolver{clusterGVR.GroupResource(): clusterGVR, machineGVR.GroupResource(): machineGVR}, map[schema.GroupVersionResource]string{machineGVR: "MachineList"})
	for name, result := range map[string]Result{
		"state":         checkClusterState(context.Background(), env),
		"pause":         checkClusterPause(context.Background(), env),
		"machine count": checkMachineCount(context.Background(), env),
		"machine state": checkMachineState(context.Background(), env),
	} {
		if name == "machine count" {
			if result.Status != Pass {
				t.Fatalf("%s: got %+v", name, result)
			}
			continue
		}
		if result.Status != Fail {
			t.Fatalf("%s: got %+v", name, result)
		}
	}
}

func TestLonghornVolumeChecks(t *testing.T) {
	volumeGVR := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "volumes"}
	volume := object(volumeGVR, "longhorn-system", "volume-1", map[string]any{
		"spec":   map[string]any{"numberOfReplicas": int64(1)},
		"status": map[string]any{"state": "attached", "robustness": "healthy", "kubernetesStatus": map[string]any{"workloadsStatus": []any{map[string]any{"podStatus": "Succeeded"}}}},
	})
	nodes := []runtime.Object{&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}}
	env := testEnvironment(t, nodes, []runtime.Object{volume}, staticResolver{volumeGVR.GroupResource(): volumeGVR}, map[schema.GroupVersionResource]string{volumeGVR: "VolumeList"})
	if result := checkVolumes(context.Background(), env); result.Status != Fail {
		t.Fatalf("single replica was not detected: %+v", result)
	}
	if result := checkAttachedVolumes(context.Background(), env); result.Status != Fail {
		t.Fatalf("stale attachment was not detected: %+v", result)
	}
}

func TestPodAndSecretChecks(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "default"}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "local-kubeconfig", Namespace: "fleet-local", Labels: map[string]string{}}}
	env := testEnvironment(t, []runtime.Object{pod, secret}, nil, nil, nil)
	if result := checkPods(context.Background(), env); result.Status != Fail {
		t.Fatalf("non-ready pod was not detected: %+v", result)
	}
	if result := checkKubeconfigSecret(context.Background(), env); result.Status != Fail {
		t.Fatalf("missing secret label was not detected: %+v", result)
	}
}

func TestBackupTargetRefreshInterval(t *testing.T) {
	settingGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "settings"}
	setting := object(settingGVR, "", "backup-target", map[string]any{"value": `{"type":"s3","refreshIntervalInSeconds":0}`})
	env := testEnvironment(t, nil, []runtime.Object{setting}, staticResolver{settingGVR.GroupResource(): settingGVR}, nil)
	env.Version = ClusterVersion{Raw: "v1.4.2", Major: 1, Minor: 4, Patch: 2}
	if result := checkBackupTarget(context.Background(), env); result.Status != Fail {
		t.Fatalf("invalid backup target passed: %+v", result)
	}
	env.Version = ClusterVersion{Raw: "v1.4.3", Major: 1, Minor: 4, Patch: 3}
	if result := checkBackupTarget(context.Background(), env); result.Status != Skipped {
		t.Fatalf("version gate failed: %+v", result)
	}
}

func TestNetworkAvailability(t *testing.T) {
	settingGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "settings"}
	managerGVR := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "instancemanagers"}
	backingManagerGVR := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "backingimagemanagers"}
	poolGVR := schema.GroupVersionResource{Group: "whereabouts.cni.cncf.io", Version: "v1alpha1", Resource: "ippools"}
	objects := []runtime.Object{
		object(settingGVR, "", "rwx-network", map[string]any{"value": `{"share-storage-network":false}`}),
		object(settingGVR, "", "storage-network", map[string]any{"value": `{"range":"10.0.0.0/30"}`}),
		object(managerGVR, "longhorn-system", "im-1", nil),
		object(backingManagerGVR, "longhorn-system", "bim-1", nil),
		object(poolGVR, "kube-system", "10.0.0.0-30", map[string]any{"spec": map[string]any{"allocations": map[string]any{"0": map[string]any{}}}}),
	}
	resources := staticResolver{settingGVR.GroupResource(): settingGVR, managerGVR.GroupResource(): managerGVR, backingManagerGVR.GroupResource(): backingManagerGVR, poolGVR.GroupResource(): poolGVR}
	lists := map[schema.GroupVersionResource]string{managerGVR: "InstanceManagerList", backingManagerGVR: "BackingImageManagerList"}
	env := testEnvironment(t, nil, objects, resources, lists)
	if result := checkStorageNetworkIPs(context.Background(), env); result.Status != Fail {
		t.Fatalf("insufficient storage addresses passed: %+v", result)
	}
}

func TestVersionSpecificNodeChecksSkipWithoutCreatingDaemonSets(t *testing.T) {
	env := testEnvironment(t, nil, nil, nil, nil)
	env.Version = ClusterVersion{Raw: "v1.5.0", Major: 1, Minor: 5}
	if result := checkNetworkConfigNodes(context.Background(), env); result.Status != Skipped {
		t.Fatalf("network config gate failed: %+v", result)
	}
	if result := checkCOSStateNodes(context.Background(), env); result.Status != Skipped {
		t.Fatalf("COS_STATE gate failed: %+v", result)
	}
}

func object(gvr schema.GroupVersionResource, namespace, name string, values map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.Group + "/" + gvr.Version,
		"kind":       strings.TrimSuffix(gvr.Resource, "s"),
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
	for key, value := range values {
		object.Object[key] = value
	}
	return object
}
