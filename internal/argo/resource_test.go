package argo_test

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/vvrnv/argo9s/internal/argo"
)

func TestFromUnstructuredApplication(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "guestbook",
			"namespace": "argocd",
		},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL": "https://github.com/argoproj/argocd-example-apps",
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{"prune": true},
			},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Synced", "revision": "abcdef1234567890"},
			"health": map[string]any{"status": "Healthy"},
		},
	}}

	r := argo.FromUnstructured(argo.KindApplication, obj)
	if r.Name != "guestbook" || r.Namespace != "argocd" {
		t.Fatalf("identity: %+v", r)
	}
	if r.Project != "default" {
		t.Fatalf("project=%q", r.Project)
	}
	if r.Sync != "Synced" || r.Health != "Healthy" {
		t.Fatalf("status sync=%q health=%q", r.Sync, r.Health)
	}
	if r.Revision != "abcdef1" {
		t.Fatalf("revision=%q", r.Revision)
	}
	if r.Suspended {
		t.Fatal("expected auto-sync enabled => not suspended")
	}
}

func TestFromUnstructuredApplicationConditions(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      "broken-chart",
			"namespace": "argocd",
		},
		"spec": map[string]any{
			"project": "default",
			"source":  map[string]any{"repoURL": "https://github.com/example/charts"},
		},
		"status": map[string]any{
			"sync":   map[string]any{"status": "Unknown"},
			"health": map[string]any{"status": "Healthy"},
			"conditions": []any{
				map[string]any{
					"type":    "ComparisonError",
					"message": `Failed to load target state: chart "broken-chart" version "1.2.3" not found`,
				},
			},
		},
	}}
	r := argo.FromUnstructured(argo.KindApplication, obj)
	if !r.HasConditions() || r.Conditions[0].Type != "ComparisonError" {
		t.Fatalf("conditions=%+v", r.Conditions)
	}
	if !strings.Contains(r.Message, "ComparisonError") || !strings.Contains(r.Message, "not found") {
		t.Fatalf("message=%q", r.Message)
	}
	d := argo.Describe(r)
	if !strings.Contains(d, "Application conditions") || !strings.Contains(d, "ComparisonError") {
		t.Fatalf("describe=\n%s", d)
	}
}

func TestParseKind(t *testing.T) {
	cases := map[string]argo.Kind{
		"app":        argo.KindApplication,
		"apps":       argo.KindApplication,
		"appset":     argo.KindApplicationSet,
		"appproject": argo.KindAppProject,
		"proj":       argo.KindAppProject,
		"all":        argo.KindAll,
	}
	for in, want := range cases {
		got, ok := argo.ParseKind(in)
		if !ok || got != want {
			t.Fatalf("%s: got %q ok=%v want %q", in, got, ok, want)
		}
	}
	if _, ok := argo.ParseKind("project"); ok {
		t.Fatal(":project is a filter command, not a kind")
	}
}

func TestStoreFilter(t *testing.T) {
	s := argo.NewStore()
	s.Upsert(argo.Resource{Kind: argo.KindApplication, Namespace: "argocd", Name: "a"})
	s.Upsert(argo.Resource{Kind: argo.KindAppProject, Namespace: "argocd", Name: "p"})
	s.Upsert(argo.Resource{Kind: argo.KindApplication, Namespace: "other", Name: "b"})

	apps := s.List(argo.KindApplication, "argocd", false)
	if len(apps) != 1 || apps[0].Name != "a" {
		t.Fatalf("namespaced apps: %+v", apps)
	}
	all := s.List(argo.KindAll, "", true)
	if len(all) != 3 {
		t.Fatalf("all: %d", len(all))
	}
}
