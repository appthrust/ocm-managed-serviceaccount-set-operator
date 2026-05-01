package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientgocache "k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	civ1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const clusterProfileAccessProviderName = "open-cluster-management"

type ClusterInventoryNamespaceResolver struct {
	LocalClient  client.Client
	AccessConfig *access.Config

	mu              sync.Mutex
	namespaceEvents chan event.GenericEvent
	caches          map[string]*remoteNamespaceCache
	cacheParentCtx  context.Context
}

type NamespaceEventSource interface {
	NamespaceEvents() <-chan event.GenericEvent
}

type NamespaceCacheJanitor interface {
	StopNamespaceCaches(set *authv1alpha1.ManagedServiceAccountReplicaSet, keepClusters sets.Set[string])
}

type remoteNamespaceCache struct {
	setUID   types.UID
	setName  string
	setNS    string
	cluster  string
	informer clientgocache.SharedIndexInformer
	cancel   context.CancelFunc
	done     <-chan struct{}
}

func (r *ClusterInventoryNamespaceResolver) NamespaceEvents() <-chan event.GenericEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureEventChannelLocked()
}

func (r *ClusterInventoryNamespaceResolver) ensureEventChannelLocked() chan event.GenericEvent {
	if r.namespaceEvents == nil {
		r.namespaceEvents = make(chan event.GenericEvent, 1024)
	}
	return r.namespaceEvents
}

func (r *ClusterInventoryNamespaceResolver) ResolveNamespaceSelector(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	_ authv1alpha1.RBACGrant,
	selector labels.Selector,
) ([]string, error) {
	if r.AccessConfig == nil {
		return nil, fmt.Errorf("controller namespace access is not configured")
	}
	clusterProfile, err := r.clusterProfileForOCMCluster(ctx, set.Namespace, clusterName)
	if err != nil {
		return nil, err
	}
	accessConfig, err := accessConfigForManagedServiceAccount(r.AccessConfig, controllerAccessManagedServiceAccountName(set), clusterProfile.Namespace)
	if err != nil {
		return nil, err
	}
	remoteConfig, err := accessConfig.BuildConfigFromCP(clusterProfile)
	if err != nil {
		return nil, fmt.Errorf("waiting for controller access for ClusterProfile %s/%s: %w", clusterProfile.Namespace, clusterProfile.Name, err)
	}
	namespaceCache, err := r.ensureNamespaceCache(set, clusterName, clusterProfile, remoteConfig)
	if err != nil {
		return nil, err
	}
	if err := waitForNamespaceCacheSync(ctx, namespaceCache); err != nil {
		return nil, fmt.Errorf("waiting for remote namespace cache for cluster %q: %w", clusterName, err)
	}
	objects := namespaceCache.informer.GetStore().List()
	matched := make([]string, 0, len(objects))
	for _, object := range objects {
		namespace, ok := object.(*corev1.Namespace)
		if !ok {
			continue
		}
		if selector.Matches(labels.Set(namespace.Labels)) {
			matched = append(matched, namespace.Name)
		}
	}
	return matched, nil
}

func (r *ClusterInventoryNamespaceResolver) clusterProfileForOCMCluster(ctx context.Context, preferredNamespace, clusterName string) (*civ1alpha1.ClusterProfile, error) {
	if r.LocalClient == nil {
		return nil, fmt.Errorf("local client is not configured")
	}
	var profiles civ1alpha1.ClusterProfileList
	if err := r.LocalClient.List(ctx, &profiles, client.MatchingLabels{ocmClusterNameLabel: clusterName}); err != nil {
		return nil, err
	}
	switch len(profiles.Items) {
	case 0:
		return nil, fmt.Errorf("waiting for ClusterProfile labeled %s=%s", ocmClusterNameLabel, clusterName)
	case 1:
		return &profiles.Items[0], nil
	}
	candidates := make([]civ1alpha1.ClusterProfile, 0, len(profiles.Items))
	if preferredNamespace != "" {
		for _, profile := range profiles.Items {
			if profile.Namespace == preferredNamespace {
				candidates = append(candidates, profile)
			}
		}
		if len(candidates) == 1 {
			return &candidates[0], nil
		}
	}
	candidates = candidates[:0]
	for _, profile := range profiles.Items {
		if hasClusterProfileAccessProvider(&profile, clusterProfileAccessProviderName) {
			candidates = append(candidates, profile)
		}
	}
	if len(candidates) == 1 {
		return &candidates[0], nil
	}
	return nil, fmt.Errorf("multiple ClusterProfiles labeled %s=%s; waiting for a single selected profile in namespace %q or with %q access provider", ocmClusterNameLabel, clusterName, preferredNamespace, clusterProfileAccessProviderName)
}

func hasClusterProfileAccessProvider(profile *civ1alpha1.ClusterProfile, providerName string) bool {
	matchName := func(p civ1alpha1.AccessProvider) bool { return p.Name == providerName }
	return slices.ContainsFunc(profile.Status.AccessProviders, matchName) ||
		slices.ContainsFunc(profile.Status.CredentialProviders, matchName)
}

func (r *ClusterInventoryNamespaceResolver) ensureNamespaceCache(
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	clusterProfile *civ1alpha1.ClusterProfile,
	remoteConfig *rest.Config,
) (*remoteNamespaceCache, error) {
	key := namespaceCacheKey(set, clusterName, clusterProfile)
	r.mu.Lock()
	if r.caches == nil {
		r.caches = map[string]*remoteNamespaceCache{}
	}
	if existing := r.caches[key]; existing != nil {
		r.mu.Unlock()
		return existing, nil
	}
	r.ensureEventChannelLocked()
	r.mu.Unlock()

	remoteClient, err := kubernetes.NewForConfig(remoteConfig)
	if err != nil {
		return nil, fmt.Errorf("build remote namespace client for ClusterProfile %s/%s: %w", clusterProfile.Namespace, clusterProfile.Name, err)
	}
	cacheCtx, cancel := context.WithCancel(r.cacheParentContext())
	listWatch := &clientgocache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return remoteClient.CoreV1().Namespaces().List(cacheCtx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return remoteClient.CoreV1().Namespaces().Watch(cacheCtx, options)
		},
	}
	informer := clientgocache.NewSharedIndexInformer(listWatch, &corev1.Namespace{}, 0, clientgocache.Indexers{})
	owner := types.NamespacedName{Namespace: set.Namespace, Name: set.Name}
	_, _ = informer.AddEventHandler(clientgocache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.enqueueNamespaceEvent(owner)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			r.enqueueNamespaceEvent(owner)
		},
		DeleteFunc: func(obj interface{}) {
			r.enqueueNamespaceEvent(owner)
		},
	})
	namespaceCache := &remoteNamespaceCache{
		setUID:   set.UID,
		setName:  set.Name,
		setNS:    set.Namespace,
		cluster:  clusterName,
		informer: informer,
		cancel:   cancel,
		done:     cacheCtx.Done(),
	}

	r.mu.Lock()
	if existing := r.caches[key]; existing != nil {
		r.mu.Unlock()
		namespaceCache.stop()
		return existing, nil
	}
	r.caches[key] = namespaceCache
	for existingKey, existingCache := range r.caches {
		if existingKey == key ||
			existingCache == nil ||
			existingCache.setUID != set.UID ||
			existingCache.setNS != set.Namespace ||
			existingCache.setName != set.Name ||
			existingCache.cluster != clusterName {
			continue
		}
		existingCache.stop()
		delete(r.caches, existingKey)
	}
	r.mu.Unlock()

	go informer.Run(namespaceCache.done)
	return namespaceCache, nil
}

func (r *ClusterInventoryNamespaceResolver) StopNamespaceCaches(set *authv1alpha1.ManagedServiceAccountReplicaSet, keepClusters sets.Set[string]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, cache := range r.caches {
		if cache == nil || cache.setUID != set.UID || cache.setNS != set.Namespace || cache.setName != set.Name {
			continue
		}
		if keepClusters.Has(cache.cluster) {
			continue
		}
		cache.stop()
		delete(r.caches, key)
	}
}

func (r *ClusterInventoryNamespaceResolver) Start(ctx context.Context) error {
	r.mu.Lock()
	r.cacheParentCtx = ctx
	r.mu.Unlock()

	<-ctx.Done()

	r.mu.Lock()
	defer r.mu.Unlock()
	for key, cache := range r.caches {
		if cache != nil {
			cache.stop()
		}
		delete(r.caches, key)
	}
	return nil
}

func (r *ClusterInventoryNamespaceResolver) cacheParentContext() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cacheParentCtx != nil {
		return r.cacheParentCtx
	}
	return context.Background()
}

func (c *remoteNamespaceCache) stop() {
	c.cancel()
}

func (r *ClusterInventoryNamespaceResolver) enqueueNamespaceEvent(owner types.NamespacedName) {
	r.mu.Lock()
	events := r.namespaceEvents
	r.mu.Unlock()
	if events == nil {
		return
	}
	set := &authv1alpha1.ManagedServiceAccountReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: owner.Namespace,
			Name:      owner.Name,
		},
	}
	select {
	case events <- event.GenericEvent{Object: set}:
	default:
	}
}

func namespaceCacheKey(set *authv1alpha1.ManagedServiceAccountReplicaSet, clusterName string, clusterProfile *civ1alpha1.ClusterProfile) string {
	return strings.Join([]string{
		string(set.UID),
		set.Namespace,
		set.Name,
		clusterName,
		clusterProfile.Namespace,
		clusterProfile.Name,
		controllerAccessManagedServiceAccountName(set),
	}, "/")
}

func waitForNamespaceCacheSync(ctx context.Context, namespaceCache *remoteNamespaceCache) error {
	syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		select {
		case <-syncCtx.Done():
		case <-namespaceCache.done:
		}
	}()
	if !clientgocache.WaitForCacheSync(stop, namespaceCache.informer.HasSynced) {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-namespaceCache.done:
			return fmt.Errorf("cache stopped")
		default:
			return fmt.Errorf("cache sync timed out")
		}
	}
	return nil
}

func accessConfigForManagedServiceAccount(in *access.Config, managedServiceAccountName, namespace string) (*access.Config, error) {
	out := &access.Config{Providers: slices.Clone(in.Providers)}
	for i := range out.Providers {
		provider := &out.Providers[i]
		if provider.Name != clusterProfileAccessProviderName || provider.ExecConfig == nil {
			continue
		}
		provider.ExecConfig = provider.ExecConfig.DeepCopy()
		provider.ExecConfig.Args = managedServiceAccountArgs(provider.ExecConfig.Args, managedServiceAccountName)
		provider.ExecConfig.Env = execEnv(provider.ExecConfig.Env, "NAMESPACE", namespace)
		return out, nil
	}
	return nil, fmt.Errorf("access config missing %q exec provider", clusterProfileAccessProviderName)
}

func managedServiceAccountArgs(args []string, managedServiceAccountName string) []string {
	desired := "--managed-serviceaccount=" + managedServiceAccountName
	out := slices.Clone(args)
	if i := slices.Index(out, "--managed-serviceaccount"); i >= 0 && i+1 < len(out) {
		out[i+1] = managedServiceAccountName
		return out
	}
	if i := slices.IndexFunc(out, func(a string) bool {
		return strings.HasPrefix(a, "--managed-serviceaccount=")
	}); i >= 0 {
		out[i] = desired
		return out
	}
	return append(out, desired)
}

func execEnv(env []clientcmdapi.ExecEnvVar, name, value string) []clientcmdapi.ExecEnvVar {
	out := slices.Clone(env)
	if i := slices.IndexFunc(out, func(e clientcmdapi.ExecEnvVar) bool { return e.Name == name }); i >= 0 {
		out[i].Value = value
		return out
	}
	return append(out, clientcmdapi.ExecEnvVar{Name: name, Value: value})
}
