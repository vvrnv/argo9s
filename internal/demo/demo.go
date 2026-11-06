package demo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/vvrnv/argo9s/internal/argo"
	"github.com/vvrnv/argo9s/internal/config"
	"github.com/vvrnv/argo9s/internal/session"
)

// Open returns an offline session with sample Applications for demos / VHS.
func Open(cfg config.Config) *session.Session {
	cfg.Namespace = "argocd"
	cfg.AllNamespaces = true
	ro := true
	store := argo.NewStore()
	for _, r := range Seed() {
		store.Upsert(r)
	}
	return &session.Session{
		Name:  "demo",
		Type:  session.TypeDemo,
		Label: "demo",
		Cfg:   cfg,
		Store: store,
		Ops:   &Ops{readOnly: true},
		Conn: config.Connection{
			Name:     "demo",
			Type:     session.TypeDemo,
			ReadOnly: &ro,
		},
	}
}

// Ops is a read-only Mutator for demo mode.
type Ops struct{ readOnly bool }

func (o *Ops) SetReadOnly(v bool) { o.readOnly = v }
func (o *Ops) ReadOnly() bool     { return o.readOnly }

func (o *Ops) Refresh(context.Context, argo.Resource, bool) error {
	return fmt.Errorf("demo mode is read-only")
}
func (o *Ops) Sync(context.Context, argo.Resource) error {
	return fmt.Errorf("demo mode is read-only")
}
func (o *Ops) Suspend(context.Context, argo.Resource) error {
	return fmt.Errorf("demo mode is read-only")
}
func (o *Ops) Resume(context.Context, argo.Resource) error {
	return fmt.Errorf("demo mode is read-only")
}
func (o *Ops) Delete(context.Context, argo.Resource) error {
	return fmt.Errorf("demo mode is read-only")
}
func (o *Ops) YAML(r argo.Resource) (string, error) {
	if r.Raw == nil {
		return "", fmt.Errorf("no raw object")
	}
	obj := r.Raw.DeepCopy()
	obj.SetManagedFields(nil)
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var _ argo.Mutator = (*Ops)(nil)

// Seed returns guestbook-style sample apps spanning sync/health states.
func Seed() []argo.Resource {
	now := time.Now()
	return []argo.Resource{
		app("guestbook", "default", "Synced", "Healthy", "abc1234", now.Add(-48*time.Hour), false, guestbookResources("guestbook"), ""),
		app("helm-guestbook", "default", "OutOfSync", "Healthy", "def5678", now.Add(-72*time.Hour), false, guestbookResources("helm-guestbook"), ""),
		app("kustomize-guestbook", "staging", "Synced", "Healthy", "aaa1111", now.Add(-10*24*time.Hour), true, guestbookResources("kustomize-guestbook"), ""),
		app("blue-green-app", "staging", "Synced", "Progressing", "bbb2222", now.Add(-5*time.Hour), false, guestbookResources("blue-green-app"), ""),
		app("canary-demo", "prod", "Synced", "Healthy", "ccc3333", now.Add(-30*24*time.Hour), false, guestbookResources("canary-demo"), ""),
		app("broken-chart", "default", "Unknown", "Healthy", "", now.Add(-2*time.Hour), false, nil, "ComparisonError: chart version not found"),
		app("api-gateway", "prod", "Synced", "Degraded", "ddd4444", now.Add(-14*24*time.Hour), false, guestbookResources("api-gateway"), ""),
		app("redis-ha", "staging", "Synced", "Healthy", "eee5555", now.Add(-20*24*time.Hour), false, guestbookResources("redis-ha"), ""),
		appSet("guestbooks"),
		proj("default"),
		proj("staging"),
		proj("prod"),
	}
}

func app(name, project, sync, health, rev string, age time.Time, suspended bool, resources []any, conditionMsg string) argo.Resource {
	spec := map[string]any{
		"project": project,
		"source": map[string]any{
			"repoURL": "https://github.com/argoproj/argocd-example-apps",
			"path":    name,
		},
		"destination": map[string]any{
			"server":    demoClusters[project],
			"namespace": name,
		},
	}
	if !suspended {
		spec["syncPolicy"] = map[string]any{
			"automated": map[string]any{"prune": true, "selfHeal": true},
		}
	}
	status := map[string]any{
		"sync":   map[string]any{"status": sync, "revision": rev},
		"health": map[string]any{"status": health},
	}
	if len(resources) > 0 {
		status["resources"] = resources
	}
	// Sync history (newest last, like Argo CD) and last operation, for describe.
	status["history"] = []any{
		map[string]any{"id": int64(1), "revision": "0a1b2c3d4e", "deployedAt": age.Add(time.Hour).UTC().Format(time.RFC3339)},
		map[string]any{"id": int64(2), "revision": rev + "000", "deployedAt": age.Add(2 * time.Hour).UTC().Format(time.RFC3339)},
	}
	phase, opMsg := "Succeeded", "successfully synced (all tasks run)"
	if health == "Degraded" {
		phase, opMsg = "Failed", "one or more objects failed to apply: Deployment.apps \"api-gateway\" is invalid: spec.template.spec.containers[0].image: Required value"
	}
	status["operationState"] = map[string]any{
		"phase": phase, "message": opMsg,
		"startedAt":  age.Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"finishedAt": age.Add(2*time.Hour + 30*time.Second).UTC().Format(time.RFC3339),
		"syncResult": map[string]any{"revision": rev},
	}
	if conditionMsg != "" {
		status["conditions"] = []any{
			map[string]any{"type": "ComparisonError", "message": conditionMsg},
		}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         "argocd",
			"creationTimestamp": age.UTC().Format(time.RFC3339),
		},
		"spec":   spec,
		"status": status,
	}}
	if owner := demoOwner(name); owner != nil {
		obj.Object["metadata"].(map[string]any)["ownerReferences"] = owner
	}
	return argo.FromUnstructured(argo.KindApplication, obj)
}

var demoClusters = map[string]string{
	"default": "https://kubernetes.default.svc",
	"staging": "https://staging-par.example.com:6443",
	"prod":    "https://prod-par.example.com:6443",
}

// demoOwner marks the guestbook variants as generated by the "guestbooks" ApplicationSet.
func demoOwner(app string) []any {
	if !strings.Contains(app, "guestbook") {
		return nil
	}
	return []any{map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet", "name": "guestbooks", "uid": "demo-appset"}}
}

func appSet(name string) argo.Resource {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "ApplicationSet",
		"metadata":   map[string]any{"name": name, "namespace": "argocd"},
		"spec": map[string]any{
			"generators": []any{map[string]any{"list": map[string]any{"elements": []any{}}}},
			"template": map[string]any{"spec": map[string]any{
				"project":     "default",
				"source":      map[string]any{"repoURL": "https://github.com/argoproj/argocd-example-apps"},
				"destination": map[string]any{"namespace": "{{name}}"},
			}},
		},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "ErrorOccurred", "status": "False", "message": "Successfully generated parameters for all Applications"},
			map[string]any{"type": "ParametersGenerated", "status": "True"},
			map[string]any{"type": "ResourcesUpToDate", "status": "True", "message": "All applications have been generated successfully"},
		}},
	}}
	return argo.FromUnstructured(argo.KindApplicationSet, obj)
}

func proj(name string) argo.Resource {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AppProject",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "argocd",
		},
		"spec": map[string]any{
			"description":  "demo project " + name,
			"sourceRepos":  []any{"https://github.com/argoproj/argocd-example-apps"},
			"destinations": []any{map[string]any{"server": demoClusters[name], "namespace": "*"}},
		},
	}}
	return argo.FromUnstructured(argo.KindAppProject, obj)
}

func guestbookResources(name string) []any {
	ns := name
	return []any{
		res("ConfigMap", ns, name+"-cm", "Synced", "Healthy"),
		res("Service", ns, name+"-svc", "Synced", "Healthy"),
		res("Deployment", ns, name, "Synced", "Healthy"),
		res("ReplicaSet", ns, name+"-7d9f8c6b5", "Synced", "Healthy"),
		res("Pod", ns, name+"-7d9f8c6b5-xk2pq", "Synced", "Healthy"),
	}
}

func res(kind, ns, name, sync, health string) map[string]any {
	return map[string]any{
		"group":     "",
		"version":   "v1",
		"kind":      kind,
		"namespace": ns,
		"name":      name,
		"status":    sync,
		"health":    map[string]any{"status": health},
	}
}
