package precheck

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
)

type Resolver interface {
	Resolve(schema.GroupResource) (schema.GroupVersionResource, error)
}

type discoveryResolver struct {
	mapper *restmapper.DeferredDiscoveryRESTMapper
}

func NewResolver(client discovery.DiscoveryInterface) Resolver {
	return &discoveryResolver{mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(client))}
}

func (r *discoveryResolver) Resolve(resource schema.GroupResource) (schema.GroupVersionResource, error) {
	return r.mapper.ResourceFor(schema.GroupVersionResource{Group: resource.Group, Resource: resource.Resource})
}

type Environment struct {
	Core           kubernetes.Interface
	Dynamic        dynamic.Interface
	Resolver       Resolver
	Version        ClusterVersion
	ValidatorImage string
	Timeout        time.Duration
	Verbose        func(string, ...any)

	cacheMu  sync.Mutex
	lists    map[string]*cachedList
	nodeOnce sync.Once
	nodes    []corev1.Node
	nodeErr  error
}

type cachedList struct {
	list  *unstructured.UnstructuredList
	err   error
	ready chan struct{}
}

func NewEnvironment(core kubernetes.Interface, dynamicClient dynamic.Interface, resolver Resolver) *Environment {
	return &Environment{Core: core, Dynamic: dynamicClient, Resolver: resolver, lists: map[string]*cachedList{}, Verbose: func(string, ...any) {}}
}

func (e *Environment) gvr(group, resource string) (schema.GroupVersionResource, error) {
	gvr, err := e.Resolver.Resolve(schema.GroupResource{Group: group, Resource: resource})
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("resolve %s.%s: %w", resource, group, err)
	}
	return gvr, nil
}

func (e *Environment) List(ctx context.Context, group, resource, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	gvr, err := e.gvr(group, resource)
	if err != nil {
		return nil, err
	}
	key := gvr.String() + "/" + namespace + "/" + options.LabelSelector
	e.cacheMu.Lock()
	if cached, ok := e.lists[key]; ok {
		e.cacheMu.Unlock()
		select {
		case <-cached.ready:
			return cached.list, cached.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cached := &cachedList{ready: make(chan struct{})}
	e.lists[key] = cached
	e.cacheMu.Unlock()

	var list *unstructured.UnstructuredList
	if namespace == "" {
		list, err = e.Dynamic.Resource(gvr).List(ctx, options)
	} else {
		list, err = e.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, options)
	}
	if err != nil {
		err = fmt.Errorf("list %s.%s: %w", resource, group, err)
	}
	cached.list = list
	cached.err = err
	close(cached.ready)
	return list, err
}

func (e *Environment) Get(ctx context.Context, group, resource, namespace, name string) (*unstructured.Unstructured, error) {
	gvr, err := e.gvr(group, resource)
	if err != nil {
		return nil, err
	}
	var object *unstructured.Unstructured
	if namespace == "" {
		object, err = e.Dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		object, err = e.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get %s %s.%s: %w", name, resource, group, err)
	}
	return object, nil
}

func (e *Environment) Nodes(ctx context.Context) ([]corev1.Node, error) {
	e.nodeOnce.Do(func() {
		list, err := e.Core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			e.nodeErr = fmt.Errorf("list nodes: %w", err)
			return
		}
		e.nodes = list.Items
	})
	return e.nodes, e.nodeErr
}

type ClusterVersion struct {
	Raw                 string
	Major, Minor, Patch int
}

func ParseClusterVersion(value string) (ClusterVersion, error) {
	var version ClusterVersion
	version.Raw = value
	if _, err := fmt.Sscanf(value, "v%d.%d.%d", &version.Major, &version.Minor, &version.Patch); err != nil {
		if _, err = fmt.Sscanf(value, "%d.%d.%d", &version.Major, &version.Minor, &version.Patch); err != nil {
			return ClusterVersion{}, fmt.Errorf("invalid Harvester version %q", value)
		}
	}
	return version, nil
}

func (v ClusterVersion) IsMinor(major, minor int) bool { return v.Major == major && v.Minor == minor }
func (v ClusterVersion) Before(major, minor int) bool {
	return v.Major < major || (v.Major == major && v.Minor < minor)
}

func DiscoverVersion(ctx context.Context, env *Environment) (ClusterVersion, error) {
	setting, err := env.Get(ctx, "harvesterhci.io", "settings", "", "server-version")
	if err != nil {
		return ClusterVersion{}, err
	}
	value, _, _ := unstructured.NestedString(setting.Object, "value")
	if value == "" {
		return ClusterVersion{}, fmt.Errorf("server-version setting is empty")
	}
	return ParseClusterVersion(value)
}
