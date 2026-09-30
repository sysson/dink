package plugins

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sysson/syskit/logx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

const (
	syncTimeout  = 30 * time.Second
	checkTimeout = 10 * time.Second
)

type Registry struct {
	systemNamespace string
	tlsConfig       *tls.Config
	static          []*Plugin

	// ctx and dynamic are set by Watch, for the background checks and status updates it drives.
	ctx     context.Context
	dynamic dynamic.Interface

	mu          sync.RWMutex
	byNamespace map[string]map[string]*Plugin
}

// New creates a registry. tlsConfig carries dink's client certificate and the
// CA plugins are verified against; without it only plaintext plugins work.
func New(systemNamespace string, static []Static, tlsConfig *tls.Config) (*Registry, error) {
	r := &Registry{
		systemNamespace: systemNamespace,
		tlsConfig:       tlsConfig,
		ctx:             context.Background(),
		byNamespace:     map[string]map[string]*Plugin{},
	}
	for _, s := range static {
		p, err := newPlugin(s.Name, "", s.Types, s.Endpoint, defaultTimeout, tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("configuring plugin %q: %w", s.Name, err)
		}
		r.static = append(r.static, p)
	}
	return r, nil
}

// Watch keeps the registry in sync with Plugin resources until ctx is cancelled.
// It returns once the initial list has loaded, so no request is served without its plugins.
func (r *Registry) Watch(ctx context.Context, client dynamic.Interface) error {
	r.ctx, r.dynamic = ctx, client
	factory := dynamicinformer.NewDynamicSharedInformerFactory(client, 0)
	informer := factory.ForResource(Resource).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.upsert(obj) },
		UpdateFunc: func(_, obj any) { r.upsert(obj) },
		DeleteFunc: r.remove,
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())

	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
		return fmt.Errorf("timed out loading %s; is the CRD installed?", Resource.GroupResource())
	}
	return nil
}

// Auth returns the auth plugins for namespace: cluster plugins first, then the tenant's.
func (r *Registry) Auth(ctx context.Context, namespace string) ([]*Plugin, error) {
	var found []*Plugin
	for _, p := range r.Visible(namespace) {
		if p.has(TypeAuth) {
			found = append(found, p)
		}
	}
	for _, p := range found {
		if err := p.ensureReady(ctx); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// Visible lists every plugin that applies to namespace: static, then cluster, then the tenant's own.
func (r *Registry) Visible(namespace string) []*Plugin {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	found := slices.Clone(r.static)
	found = append(found, sorted(r.byNamespace[r.systemNamespace])...)
	if namespace != "" && namespace != r.systemNamespace {
		found = append(found, sorted(r.byNamespace[namespace])...)
	}
	return found
}

// Lookup finds a plugin by name for namespace, preferring the tenant's own registration.
func (r *Registry) Lookup(ctx context.Context, namespace, name string, t Type) (*Plugin, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: no %s plugin named %q", ErrNotFound, t, name)
	}
	r.mu.RLock()
	p := r.byNamespace[namespace][name]
	if p == nil || !p.has(t) {
		p = r.byNamespace[r.systemNamespace][name]
	}
	if p == nil || !p.has(t) {
		p = nil
		for _, s := range r.static {
			if s.Name == name && s.has(t) {
				p = s
			}
		}
	}
	r.mu.RUnlock()

	if p == nil {
		return nil, fmt.Errorf("%w: no %s plugin named %q", ErrNotFound, t, name)
	}
	if err := p.ensureReady(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func sorted(plugins map[string]*Plugin) []*Plugin {
	out := make([]*Plugin, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Plugin) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (r *Registry) upsert(obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	r.mu.RLock()
	existing := r.byNamespace[u.GetNamespace()][u.GetName()]
	r.mu.RUnlock()
	// Status writes bump only the resourceVersion; keep the plugin and its health.
	if existing != nil && existing.UID == u.GetUID() && existing.generation == u.GetGeneration() {
		return
	}

	p, err := r.fromObject(u)
	if err != nil {
		logx.G(r.ctx).WithError(err).Warn("invalid plugin registration", "namespace", u.GetNamespace(), "name", u.GetName())
		p = &Plugin{Name: u.GetName(), Namespace: u.GetNamespace(), err: err}
	}
	p.UID, p.generation = u.GetUID(), u.GetGeneration()
	p.changed = r.writeStatus

	r.mu.Lock()
	if r.byNamespace[p.Namespace] == nil {
		r.byNamespace[p.Namespace] = map[string]*Plugin{}
	}
	r.byNamespace[p.Namespace][p.Name] = p
	r.mu.Unlock()

	if r.dynamic == nil {
		return
	}
	if p.err != nil {
		go r.writeStatus(p)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, checkTimeout)
		defer cancel()
		_ = p.ensureReady(ctx)
	}()
}

func (r *Registry) remove(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byNamespace[u.GetNamespace()], u.GetName())
}

func (r *Registry) fromObject(u *unstructured.Unstructured) (*Plugin, error) {
	spec, err := SpecFromObject(u)
	if err != nil {
		return nil, err
	}
	timeout, err := spec.timeout()
	if err != nil {
		return nil, err
	}
	return newPlugin(u.GetName(), u.GetNamespace(), spec.Types, spec.Endpoint(u.GetNamespace()), timeout, r.tlsConfig)
}

func (r *Registry) writeStatus(p *Plugin) {
	if r.dynamic == nil {
		return
	}
	ready, version, err := p.Health()
	status := Status{Ready: ready, Version: version, ObservedGeneration: p.generation}
	if err != nil {
		status.Message = err.Error()
	}
	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": p.UID},
		"status":   status,
	})
	_, patchErr := r.dynamic.Resource(Resource).Namespace(p.Namespace).
		Patch(r.ctx, p.Name, types.MergePatchType, body, metav1.PatchOptions{}, "status")
	if patchErr != nil {
		logx.G(r.ctx).WithError(patchErr).Debug("updating plugin status", "plugin", p.String())
	}
}
