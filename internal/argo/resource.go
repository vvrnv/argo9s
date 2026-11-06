package argo

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is a supported Argo CD resource kind.
type Kind string

const (
	KindApplication    Kind = "Application"
	KindApplicationSet Kind = "ApplicationSet"
	KindAppProject     Kind = "AppProject"
	KindAll            Kind = "All"
	// Synthetic kinds: built in the UI, never watched.
	KindCluster Kind = "Cluster" // an Application destination cluster
	KindContext Kind = "Context" // a configured argo9s connection
)

var (
	GVRApplication = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
	}
	GVRApplicationSet = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets",
	}
	GVRAppProject = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "appprojects",
	}
)

func (k Kind) Plural() string {
	switch k {
	case KindApplication:
		return "applications"
	case KindApplicationSet:
		return "applicationsets"
	case KindAppProject:
		return "appprojects"
	case KindCluster:
		return "clusters"
	case KindContext:
		return "contexts"
	default:
		return strings.ToLower(string(k))
	}
}

func (k Kind) Short() string {
	switch k {
	case KindApplication:
		return "app"
	case KindApplicationSet:
		return "appset"
	case KindAppProject:
		return "project"
	case KindAll:
		return "all"
	case KindCluster:
		return "cluster"
	case KindContext:
		return "ctx"
	default:
		return strings.ToLower(string(k))
	}
}

func ParseKind(s string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all", "a":
		return KindAll, true
	case "application", "applications", "app", "apps":
		return KindApplication, true
	case "applicationset", "applicationsets", "appset", "appsets":
		return KindApplicationSet, true
	case "appproject", "appprojects", "proj":
		return KindAppProject, true
	default:
		return "", false
	}
}

func GVRFor(k Kind) (schema.GroupVersionResource, bool) {
	switch k {
	case KindApplication:
		return GVRApplication, true
	case KindApplicationSet:
		return GVRApplicationSet, true
	case KindAppProject:
		return GVRAppProject, true
	default:
		return schema.GroupVersionResource{}, false
	}
}

func WatchKinds(filter Kind) []Kind {
	if filter == KindAll {
		return []Kind{KindApplication, KindApplicationSet, KindAppProject}
	}
	return []Kind{filter}
}

// Condition is an Argo CD Application/ApplicationSet status condition.
type Condition struct {
	Type    string
	Message string
}

// Resource is a normalized Argo CD object for the UI.
type Resource struct {
	Kind       Kind
	Namespace  string
	Name       string
	Project    string
	Sync       string
	Health     string
	Revision   string
	Repo       string
	Age        time.Time
	Suspended  bool
	Message    string
	Conditions []Condition
	Owner      string // ApplicationSet that generated this Application, if any
	Cluster    string // Application destination (name, or server host)
	Raw        *unstructured.Unstructured
}

func (r Resource) HasConditions() bool {
	return len(r.Conditions) > 0
}

func (r Resource) ConditionSummary() string {
	if len(r.Conditions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		if c.Type == "" {
			continue
		}
		parts = append(parts, c.Type)
	}
	return strings.Join(parts, ", ")
}

func (r Resource) Key() string {
	return fmt.Sprintf("%s/%s/%s", r.Kind, r.Namespace, r.Name)
}

func (r Resource) AgeString() string {
	if r.Age.IsZero() {
		return "-"
	}
	d := time.Since(r.Age)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func FromUnstructured(kind Kind, obj *unstructured.Unstructured) Resource {
	return fromUnstructured(kind, obj, true)
}

// FromUnstructuredShared is like FromUnstructured but keeps Raw without DeepCopy.
// Safe for freshly decoded API maps that are not reused by a watch cache.
func FromUnstructuredShared(kind Kind, obj *unstructured.Unstructured) Resource {
	return fromUnstructured(kind, obj, false)
}

func fromUnstructured(kind Kind, obj *unstructured.Unstructured, copyRaw bool) Resource {
	raw := obj
	if copyRaw {
		raw = obj.DeepCopy()
	}
	r := Resource{
		Kind:      kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
		Age:       obj.GetCreationTimestamp().Time,
		Raw:       raw,
	}

	switch kind {
	case KindApplication:
		r.Project, _, _ = unstructured.NestedString(obj.Object, "spec", "project")
		r.Sync, _, _ = unstructured.NestedString(obj.Object, "status", "sync", "status")
		r.Health, _, _ = unstructured.NestedString(obj.Object, "status", "health", "status")
		r.Revision, _, _ = unstructured.NestedString(obj.Object, "status", "sync", "revision")
		if r.Revision == "" {
			r.Revision, _, _ = unstructured.NestedString(obj.Object, "status", "operationState", "syncResult", "revision")
		}
		r.Repo = firstRepo(obj)
		r.Cluster = clusterOf(obj)
		for _, o := range obj.GetOwnerReferences() {
			if o.Kind == "ApplicationSet" {
				r.Owner = o.Name
			}
		}
		r.Suspended = isAppSuspended(obj)
		r.Conditions = parseConditions(obj)
		r.Message, _, _ = unstructured.NestedString(obj.Object, "status", "health", "message")
		if r.Message == "" {
			r.Message, _, _ = unstructured.NestedString(obj.Object, "status", "operationState", "message")
		}
		// Prefer Application conditions (ComparisonError, InvalidSpecError, …) — same as Argo UI.
		if msg := primaryConditionMessage(r.Conditions); msg != "" {
			r.Message = msg
		}
	case KindApplicationSet:
		r.Project = nestedTemplateProject(obj)
		conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		r.Conditions = appSetProblems(conds)
		r.Sync, r.Health, r.Message = summarizeConditions(conds)
		r.Repo = nestedTemplateRepo(obj)
	case KindAppProject:
		r.Project = r.Name
		r.Sync = "N/A"
		r.Health = "N/A"
		desc, _, _ := unstructured.NestedString(obj.Object, "spec", "description")
		r.Message = desc
	}

	if r.Sync == "" {
		r.Sync = "-"
	}
	if r.Health == "" {
		r.Health = "-"
	}
	if r.Project == "" {
		r.Project = "-"
	}
	if r.Revision == "" {
		r.Revision = "-"
	}
	if len(r.Revision) > 7 && r.Revision != "-" {
		r.Revision = r.Revision[:7]
	}
	return r
}

func firstRepo(obj *unstructured.Unstructured) string {
	if repo, ok, _ := unstructured.NestedString(obj.Object, "spec", "source", "repoURL"); ok && repo != "" {
		return repo
	}
	sources, ok, _ := unstructured.NestedSlice(obj.Object, "spec", "sources")
	if !ok || len(sources) == 0 {
		return "-"
	}
	m, ok := sources[0].(map[string]any)
	if !ok {
		return "-"
	}
	if repo, _ := m["repoURL"].(string); repo != "" {
		return repo
	}
	return "-"
}

func nestedTemplateProject(obj *unstructured.Unstructured) string {
	p, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "spec", "project")
	if p == "" {
		return "-"
	}
	return p
}

func nestedTemplateRepo(obj *unstructured.Unstructured) string {
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "template", "spec", "source", "repoURL")
	if repo == "" {
		return "-"
	}
	return repo
}

// isAppSuspended reports whether automated sync is disabled (manual mode).
func isAppSuspended(obj *unstructured.Unstructured) bool {
	_, hasAutomated, _ := unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated")
	return !hasAutomated
}

func summarizeConditions(conds []any) (sync, health, message string) {
	sync, health = "Unknown", "Unknown"
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		status, _ := m["status"].(string)
		msg, _ := m["message"].(string)
		if msg != "" && message == "" {
			message = msg
		}
		switch t {
		case "ErrorOccurred":
			if status == "True" {
				health = "Degraded"
				sync = "Unknown"
			}
		case "ResourcesUpToDate":
			if status == "True" {
				sync = "Synced"
				if health == "Unknown" {
					health = "Healthy"
				}
			} else {
				sync = "OutOfSync"
			}
		case "ApplicationSetUpToDate":
			if status == "True" && health == "Unknown" {
				health = "Healthy"
				sync = "Synced"
			}
		}
	}
	return sync, health, message
}

func parseConditions(obj *unstructured.Unstructured) []Condition {
	if obj == nil {
		return nil
	}
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	return conditionsFromSlice(raw)
}

// appSetProblems keeps only ApplicationSet conditions that signal a problem.
// Healthy appsets always carry informational ones (ErrorOccurred=False,
// ResourcesUpToDate=True, …) which must not paint the row red or add "!".
func appSetProblems(raw []any) []Condition {
	var keep []any
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		status, _ := m["status"].(string)
		isErr := strings.Contains(t, "Error") && status == "True"
		notUpToDate := t == "ResourcesUpToDate" && status == "False"
		if isErr || notUpToDate {
			keep = append(keep, item)
		}
	}
	return conditionsFromSlice(keep)
}

func conditionsFromSlice(raw []any) []Condition {
	if len(raw) == 0 {
		return nil
	}
	out := make([]Condition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		msg, _ := m["message"].(string)
		if t == "" && msg == "" {
			continue
		}
		out = append(out, Condition{Type: t, Message: msg})
	}
	return out
}

// primaryConditionMessage picks the most actionable Application condition message
// (errors first), matching what Argo CD UI highlights under "Application conditions".
func primaryConditionMessage(conds []Condition) string {
	if len(conds) == 0 {
		return ""
	}
	priority := []string{
		"ComparisonError",
		"InvalidSpecError",
		"UnknownError",
		"SharedResourceWarning",
		"OrphanedResourceWarning",
		"ExcludedResourceWarning",
	}
	byType := map[string]string{}
	for _, c := range conds {
		if c.Message == "" {
			continue
		}
		byType[c.Type] = c.Message
	}
	for _, t := range priority {
		if msg := byType[t]; msg != "" {
			return t + ": " + msg
		}
	}
	for _, c := range conds {
		if c.Message != "" {
			if c.Type != "" {
				return c.Type + ": " + c.Message
			}
			return c.Message
		}
	}
	return ""
}

// clusterOf returns a short destination label: destination.name, else the server
// host ("in-cluster" for the local API server).
func clusterOf(obj *unstructured.Unstructured) string {
	if n, _, _ := unstructured.NestedString(obj.Object, "spec", "destination", "name"); n != "" {
		return n
	}
	srv, _, _ := unstructured.NestedString(obj.Object, "spec", "destination", "server")
	if srv == "https://kubernetes.default.svc" {
		return "in-cluster"
	}
	srv = strings.TrimPrefix(strings.TrimPrefix(srv, "https://"), "http://")
	if srv == "" {
		return "-"
	}
	return srv
}
