package argo

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/vvrnv/argo9s/internal/kube"
)

// EventType mirrors Kubernetes watch event types for the UI.
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
	EventError    EventType = "ERROR"
	EventBookmark EventType = "BOOKMARK"
)

// Event is delivered to the TUI when a watched resource changes.
type Event struct {
	Type     EventType
	Resource Resource
	Err      error
}

// Store is a thread-safe in-memory cache of Argo resources.
type Store struct {
	mu    sync.RWMutex
	byKey map[string]Resource
}

func NewStore() *Store {
	return &Store{byKey: make(map[string]Resource)}
}

func (s *Store) Upsert(r Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[r.Key()] = r
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byKey)
}

func (s *Store) Delete(r Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byKey, r.Key())
}

func (s *Store) List(filter Kind, namespace string, allNS bool) []Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Resource, 0, len(s.byKey))
	for _, r := range s.byKey {
		if filter != KindAll && r.Kind != filter {
			continue
		}
		if !allNS && namespace != "" && namespace != "all" && r.Namespace != namespace {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s *Store) Get(key string) (Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byKey[key]
	return r, ok
}

// Watcher watches Argo CRDs and pushes events.
type Watcher struct {
	client *kube.Client
	store  *Store
}

func NewWatcher(client *kube.Client, store *Store) *Watcher {
	return &Watcher{client: client, store: store}
}

// Start blocks until ctx is cancelled. Missing CRDs are reported as events, not fatal.
func (w *Watcher) Start(ctx context.Context, filter Kind, namespace string, allNS bool, out chan<- Event) error {
	kinds := WatchKinds(filter)
	var wg sync.WaitGroup
	for _, kind := range kinds {
		gvr, ok := GVRFor(kind)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(kind Kind, gvr schema.GroupVersionResource) {
			defer wg.Done()
			w.watchLoop(ctx, kind, gvr, namespace, allNS, out)
		}(kind, gvr)
	}
	wg.Wait()
	return ctx.Err()
}

func (w *Watcher) watchLoop(ctx context.Context, kind Kind, gvr schema.GroupVersionResource, namespace string, allNS bool, out chan<- Event) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := w.watchOne(ctx, kind, gvr, namespace, allNS, out)
		if ctx.Err() != nil {
			return
		}
		// Watch channels close periodically; reconnect quietly.
		if err != nil && (err.Error() == "closed" || strings.Contains(err.Error(), "closed")) {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		select {
		case out <- Event{Type: EventError, Err: fmt.Errorf("%s: %w (retrying)", kind, err)}:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (w *Watcher) watchOne(ctx context.Context, kind Kind, gvr schema.GroupVersionResource, namespace string, allNS bool, out chan<- Event) error {
	var resource dynamic.ResourceInterface
	if allNS || namespace == "" || namespace == "all" {
		resource = w.client.Dynamic.Resource(gvr)
	} else {
		resource = w.client.Dynamic.Resource(gvr).Namespace(namespace)
	}

	list, err := resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for i := range list.Items {
		item := list.Items[i]
		r := FromUnstructured(kind, &item)
		w.store.Upsert(r)
		select {
		case out <- Event{Type: EventAdded, Resource: r}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	wi, err := resource.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion()})
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer wi.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return fmt.Errorf("closed")
			}
			if ev.Type == watch.Error {
				return fmt.Errorf("api error")
			}
			obj, ok := ev.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			r := FromUnstructured(kind, obj)
			switch ev.Type {
			case watch.Added, watch.Modified:
				w.store.Upsert(r)
				select {
				case out <- Event{Type: EventType(ev.Type), Resource: r}:
				case <-ctx.Done():
					return ctx.Err()
				}
			case watch.Deleted:
				w.store.Delete(r)
				select {
				case out <- Event{Type: EventDeleted, Resource: r}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
