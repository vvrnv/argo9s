package argo_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/vvrnv/argo9s/internal/argo"
)

func TestBuildAppGraph(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "guestbook",
			"namespace": "argocd",
		},
		"spec": map[string]any{
			"project":     "default",
			"source":      map[string]any{"repoURL": "https://example.com/repo"},
			"destination": map[string]any{"namespace": "demo"},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced"},
			"health": map[string]any{"status": "Healthy"},
			"resources": []any{
				map[string]any{
					"group": "apps", "kind": "Deployment", "namespace": "demo", "name": "guestbook-ui",
					"status": "Synced", "health": map[string]any{"status": "Healthy"},
				},
				map[string]any{
					"kind": "Service", "namespace": "demo", "name": "guestbook-ui",
					"status": "Synced", "health": map[string]any{"status": "Healthy"},
				},
				map[string]any{
					"kind": "ConfigMap", "namespace": "demo", "name": "guestbook-ui-cm",
					"status": "OutOfSync",
				},
				map[string]any{
					"group": "apps", "kind": "ReplicaSet", "namespace": "demo", "name": "guestbook-ui-abc",
					"status": "Synced", "health": map[string]any{"status": "Healthy"},
				},
				map[string]any{
					"group": "apps", "kind": "ReplicaSet", "namespace": "demo", "name": "guestbook-ui-old",
					"status": "Synced", "health": map[string]any{"status": "Suspended"},
				},
				map[string]any{
					"kind": "Pod", "namespace": "demo", "name": "guestbook-ui-abc-xyz",
					"status": "Synced", "health": map[string]any{"status": "Healthy"},
				},
			},
		},
	}}

	app := argo.FromUnstructured(argo.KindApplication, obj)
	g := argo.BuildAppGraph(app)
	if len(g.Nodes) < 6 {
		t.Fatalf("expected root + tree, got %d: %+v", len(g.Nodes), g.Nodes)
	}
	if g.Nodes[0].KindShort != "app" || g.Nodes[0].Parent != -1 {
		t.Fatalf("root=%+v", g.Nodes[0])
	}

	var deployIdx, rsActive, podIdx = -1, -1, -1
	for i, n := range g.Nodes {
		switch {
		case n.Kind == "Deployment" && n.Name == "guestbook-ui":
			deployIdx = i
			if n.Parent != 0 || n.Depth != 1 {
				t.Fatalf("deploy parent/depth: %+v", n)
			}
		case n.Kind == "ReplicaSet" && n.Name == "guestbook-ui-abc":
			rsActive = i
		case n.Kind == "Pod" && n.Name == "guestbook-ui-abc-xyz":
			podIdx = i
		}
	}
	if deployIdx < 0 || rsActive < 0 || podIdx < 0 {
		t.Fatalf("missing nodes deploy=%d rs=%d pod=%d in %+v", deployIdx, rsActive, podIdx, g.Nodes)
	}
	if g.Nodes[rsActive].Parent != deployIdx || g.Nodes[rsActive].Depth != 2 {
		t.Fatalf("rs parent want deploy=%d depth2, got %+v", deployIdx, g.Nodes[rsActive])
	}
	if g.Nodes[podIdx].Parent != rsActive || g.Nodes[podIdx].Depth != 3 {
		t.Fatalf("pod parent want rs=%d depth3, got %+v", rsActive, g.Nodes[podIdx])
	}
}

func TestKindShortName(t *testing.T) {
	if argo.KindShortName("Deployment") != "deploy" {
		t.Fatal(argo.KindShortName("Deployment"))
	}
	if argo.KindShortName("ReplicaSet") != "rs" {
		t.Fatal(argo.KindShortName("ReplicaSet"))
	}
}
