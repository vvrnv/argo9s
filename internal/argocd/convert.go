package argocd

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/vvrnv/argo9s/internal/argo"
)

func MapToUnstructured(kind argo.Kind, m map[string]any) *unstructured.Unstructured {
	m = unwrapResourceMap(m)
	ensureStringMetadata(m)
	obj := &unstructured.Unstructured{Object: m}
	if obj.GetAPIVersion() == "" {
		obj.SetAPIVersion("argoproj.io/v1alpha1")
	}
	if obj.GetKind() == "" {
		obj.SetKind(string(kind))
	}
	return obj
}

func ResourceFromAppMap(m map[string]any) argo.Resource {
	u := MapToUnstructured(argo.KindApplication, m)
	r := argo.FromUnstructuredShared(argo.KindApplication, u)
	if r.Name == "" {
		r.Name = nestedName(m)
	}
	if r.Namespace == "" {
		r.Namespace = nestedNamespace(m)
	}
	return r
}

func ResourceFromProjectMap(m map[string]any) argo.Resource {
	if p, ok := m["project"].(map[string]any); ok {
		m = p
	}
	u := MapToUnstructured(argo.KindAppProject, m)
	r := argo.FromUnstructuredShared(argo.KindAppProject, u)
	if r.Name == "" {
		r.Name = nestedName(m)
	}
	return r
}

func ResourceFromAppSetMap(m map[string]any) argo.Resource {
	u := MapToUnstructured(argo.KindApplicationSet, m)
	r := argo.FromUnstructuredShared(argo.KindApplicationSet, u)
	if r.Name == "" {
		r.Name = nestedName(m)
	}
	if r.Namespace == "" {
		r.Namespace = nestedNamespace(m)
	}
	return r
}

func unwrapResourceMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	for _, key := range []string{"item", "application", "Application", "project", "Project"} {
		if inner, ok := m[key].(map[string]any); ok {
			return inner
		}
	}
	return m
}

func ensureStringMetadata(m map[string]any) {
	md := asStringMap(m["metadata"])
	if md == nil {
		name := stringify(m["name"])
		ns := stringify(m["namespace"])
		if name == "" && ns == "" {
			return
		}
		md = map[string]any{}
		if name != "" {
			md["name"] = name
		}
		if ns != "" {
			md["namespace"] = ns
		}
		m["metadata"] = md
		return
	}
	if v := stringify(md["name"]); v != "" {
		md["name"] = v
	}
	if v := stringify(md["namespace"]); v != "" {
		md["namespace"] = v
	}
	m["metadata"] = md
}

// asStringMap coerces JSON objects that arrived with non-string keys or as
// map[string]string into map[string]any for unstructured helpers.
func asStringMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = val
		}
		return out
	default:
		return nil
	}
}

func nestedName(m map[string]any) string {
	m = unwrapResourceMap(m)
	if md := asStringMap(m["metadata"]); md != nil {
		if v := stringify(md["name"]); v != "" {
			return v
		}
	}
	return stringify(m["name"])
}

func nestedNamespace(m map[string]any) string {
	m = unwrapResourceMap(m)
	if md := asStringMap(m["metadata"]); md != nil {
		if v := stringify(md["namespace"]); v != "" {
			return v
		}
	}
	return stringify(m["namespace"])
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case json.Number:
		return t.String()
	default:
		s := fmt.Sprint(t)
		if s == "<nil>" {
			return ""
		}
		return strings.TrimSpace(s)
	}
}

// ParseCount returns how many maps yield a non-empty name (diagnostics).
func ParseCount(apps []map[string]any) (named int) {
	for _, app := range apps {
		if ResourceFromAppMap(app).Name != "" {
			named++
		}
	}
	return named
}

// SampleAppDiag returns a short diagnostic for the first application map.
func SampleAppDiag(apps []map[string]any) string {
	if len(apps) == 0 {
		return "none"
	}
	app := unwrapResourceMap(apps[0])
	keys := mapKeys(app)
	r := ResourceFromAppMap(app)
	return fmt.Sprintf("keys=%v name=%q ns=%q", keys, r.Name, r.Namespace)
}
