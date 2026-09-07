package argocd

import (
	"context"
	"fmt"
	"time"

	"github.com/vvrnv/argo9s/internal/argo"
)

// Watcher fills the shared argo.Store from the Argo CD API.
type Watcher struct {
	client *Client
	store  *argo.Store
}

func NewWatcher(client *Client, store *argo.Store) *Watcher {
	return &Watcher{client: client, store: store}
}

func (w *Watcher) Start(ctx context.Context, out chan<- argo.Event) error {
	// Snapshot first — stream endpoints often don't replay current apps.
	if err := w.seedApplications(ctx, out); err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	w.seedExtras(ctx, out)

	appCh := make(chan WatchEvent, 128)
	go func() {
		_ = w.client.pollApplications(ctx, appCh, 3*time.Second)
		close(appCh)
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.seedExtras(ctx, out)
		case ev, ok := <-appCh:
			if !ok {
				// Poller exited; restart unless cancelled.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				appCh = make(chan WatchEvent, 128)
				go func(ch chan WatchEvent) {
					_ = w.client.pollApplications(ctx, ch, 3*time.Second)
					close(ch)
				}(appCh)
				continue
			}
			if ev.Err != nil {
				select {
				case out <- argo.Event{Type: argo.EventError, Err: ev.Err}:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			r := ResourceFromAppMap(ev.App)
			if r.Name == "" {
				continue
			}
			switch ev.Type {
			case "DELETED", "DELETE":
				w.store.Delete(r)
				select {
				case out <- argo.Event{Type: argo.EventDeleted, Resource: r}:
				case <-ctx.Done():
					return ctx.Err()
				}
			default:
				w.store.Upsert(r)
				typ := argo.EventModified
				if ev.Type == "ADDED" || ev.Type == "ADD" {
					typ = argo.EventAdded
				}
				select {
				case out <- argo.Event{Type: typ, Resource: r}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func (w *Watcher) seedApplications(ctx context.Context, out chan<- argo.Event) error {
	items, err := w.client.ListApplications(ctx)
	if err != nil {
		return err
	}
	resources := make([]argo.Resource, 0, len(items))
	for _, app := range items {
		r := ResourceFromAppMap(app)
		if r.Name == "" {
			continue
		}
		w.store.Upsert(r)
		resources = append(resources, r)
	}
	if len(items) > 0 && len(resources) == 0 {
		return fmt.Errorf("parsed 0/%d application names (%s)", len(items), SampleAppDiag(items))
	}
	// Notify UI without blocking the rest of the seed on a full event buffer.
	for _, r := range resources {
		select {
		case out <- argo.Event{Type: argo.EventAdded, Resource: r}:
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}

func (w *Watcher) seedExtras(ctx context.Context, out chan<- argo.Event) {
	if projects, err := w.client.ListProjects(ctx); err == nil {
		for _, p := range projects {
			r := ResourceFromProjectMap(p)
			if r.Name == "" {
				continue
			}
			w.store.Upsert(r)
			select {
			case out <- argo.Event{Type: argo.EventAdded, Resource: r}:
			case <-ctx.Done():
				return
			default:
			}
		}
	}
	if sets, err := w.client.ListApplicationSets(ctx); err == nil {
		for _, s := range sets {
			r := ResourceFromAppSetMap(s)
			if r.Name == "" {
				continue
			}
			w.store.Upsert(r)
			select {
			case out <- argo.Event{Type: argo.EventAdded, Resource: r}:
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}
