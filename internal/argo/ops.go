package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/vvrnv/argo9s/internal/kube"
)

// Operator performs mutating actions on Argo resources via the Kubernetes API.
type Operator struct {
	client   *kube.Client
	readOnly bool
}

func NewOperator(client *kube.Client, readOnly bool) *Operator {
	return &Operator{client: client, readOnly: readOnly}
}

func (o *Operator) SetReadOnly(v bool) { o.readOnly = v }
func (o *Operator) ReadOnly() bool     { return o.readOnly }

func (o *Operator) guard() error {
	if o.readOnly {
		return fmt.Errorf("read-only mode (toggle with :readonly)")
	}
	return nil
}

func (o *Operator) Refresh(ctx context.Context, r Resource, hard bool) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != KindApplication {
		return fmt.Errorf("refresh is only supported for Applications")
	}
	gvr, _ := GVRFor(r.Kind)
	value := "normal"
	if hard {
		value = "hard"
	}
	patch := []byte(fmt.Sprintf(
		`[{"op":"add","path":"/metadata/annotations/argocd.argoproj.io~1refresh","value":%q}]`,
		value,
	))
	_, err := o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Patch(
		ctx, r.Name, types.JSONPatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		// Annotation may already exist — merge-patch fallback.
		merge := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":%q}}}`, value))
		_, err = o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Patch(
			ctx, r.Name, types.MergePatchType, merge, metav1.PatchOptions{},
		)
	}
	return err
}

func (o *Operator) Sync(ctx context.Context, r Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != KindApplication {
		return fmt.Errorf("sync is only supported for Applications")
	}
	gvr, _ := GVRFor(r.Kind)
	op := map[string]any{
		"operation": map[string]any{
			"initiatedBy": map[string]any{"username": "argo9s"},
			"sync": map[string]any{
				"prune":    true,
				"revision": "HEAD",
			},
		},
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Patch(
		ctx, r.Name, types.MergePatchType, data, metav1.PatchOptions{},
	)
	return err
}

func (o *Operator) Suspend(ctx context.Context, r Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != KindApplication {
		return fmt.Errorf("suspend is only supported for Applications")
	}
	return o.patchAutomated(ctx, r, false)
}

func (o *Operator) Resume(ctx context.Context, r Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	if r.Kind != KindApplication {
		return fmt.Errorf("resume is only supported for Applications")
	}
	return o.patchAutomated(ctx, r, true)
}

func (o *Operator) patchAutomated(ctx context.Context, r Resource, enable bool) error {
	gvr, _ := GVRFor(r.Kind)
	var patch []byte
	if enable {
		patch = []byte(`{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}`)
	} else {
		// Remove automated sync (manual mode).
		patch = []byte(`[{"op":"remove","path":"/spec/syncPolicy/automated"}]`)
		_, err := o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Patch(
			ctx, r.Name, types.JSONPatchType, patch, metav1.PatchOptions{},
		)
		if err == nil {
			return nil
		}
		// If path missing, set syncPolicy without automated.
		patch = []byte(`{"spec":{"syncPolicy":{"automated":null}}}`)
	}
	_, err := o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Patch(
		ctx, r.Name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

func (o *Operator) Delete(ctx context.Context, r Resource) error {
	if err := o.guard(); err != nil {
		return err
	}
	gvr, ok := GVRFor(r.Kind)
	if !ok {
		return fmt.Errorf("unsupported kind %s", r.Kind)
	}
	return o.client.Dynamic.Resource(gvr).Namespace(r.Namespace).Delete(ctx, r.Name, metav1.DeleteOptions{})
}

func (o *Operator) YAML(r Resource) (string, error) {
	if r.Raw == nil {
		return "", fmt.Errorf("no raw object")
	}
	// Drop managed fields for readability.
	obj := r.Raw.DeepCopy()
	obj.SetManagedFields(nil)
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Describe renders a kubectl-describe-like summary, with sections per kind.
func Describe(r Resource) string {
	var b []byte
	line := func(k, v string) {
		if v != "" && v != "-" {
			b = fmt.Appendf(b, "%-14s%s\n", k+":", v)
		}
	}
	section := func(title string) { b = fmt.Appendf(b, "\n%s:\n", title) }
	item := func(format string, a ...any) { b = fmt.Appendf(b, "  • "+format+"\n", a...) }
	var obj map[string]any
	if r.Raw != nil {
		obj = r.Raw.Object
	}
	str := func(m map[string]any, path ...string) string {
		v, _, _ := unstructured.NestedString(m, path...)
		return v
	}

	line("Kind", string(r.Kind))
	line("Name", r.Name)
	line("Namespace", r.Namespace)
	line("Age", r.AgeString())

	switch r.Kind {
	case KindApplication:
		line("Project", r.Project)
		line("AppSet", r.Owner)
		line("Sync", r.Sync)
		line("Health", r.Health)
		line("Revision", r.Revision)
		line("Message", r.Message)
		dest := str(obj, "spec", "destination", "server")
		if dest == "" {
			dest = str(obj, "spec", "destination", "name")
		}
		section("Destination")
		item("cluster %s  namespace %s", orDash(dest), orDash(str(obj, "spec", "destination", "namespace")))
		section("Sources")
		srcs, _, _ := unstructured.NestedSlice(obj, "spec", "sources")
		if src, ok, _ := unstructured.NestedMap(obj, "spec", "source"); ok {
			srcs = append([]any{src}, srcs...)
		}
		for _, s := range srcs {
			if m, ok := s.(map[string]any); ok {
				path := str(m, "path")
				if path == "" {
					path = "chart " + str(m, "chart")
				}
				item("%s  %s  @%s", str(m, "repoURL"), path, orDash(str(m, "targetRevision")))
			}
		}
		section("Sync policy")
		if auto, ok, _ := unstructured.NestedMap(obj, "spec", "syncPolicy", "automated"); ok {
			item("automated  prune=%v  selfHeal=%v", auto["prune"] == true, auto["selfHeal"] == true)
		} else {
			item("manual")
		}
		if op, ok, _ := unstructured.NestedMap(obj, "status", "operationState"); ok {
			section("Last sync")
			item("%s  rev %s  %s → %s", orDash(str(op, "phase")), orDash(shortRev(str(op, "syncResult", "revision"))), orDash(str(op, "startedAt")), orDash(str(op, "finishedAt")))
			for _, l := range wrapWords(str(op, "message"), 88) {
				b = fmt.Appendf(b, "      %s\n", l)
			}
			// Per-resource failures — the error list the Argo CD UI shows under "Sync status".
			res, _, _ := unstructured.NestedSlice(op, "syncResult", "resources")
			for _, x := range res {
				m, ok := x.(map[string]any)
				if !ok || str(m, "status") == "Synced" || str(m, "status") == "" {
					continue
				}
				item("%s %s/%s: %s", str(m, "status"), str(m, "kind"), str(m, "name"), str(m, "message"))
			}
		}
		if hist, _, _ := unstructured.NestedSlice(obj, "status", "history"); len(hist) > 0 {
			section(fmt.Sprintf("Sync history (%d, newest first)", len(hist)))
			for i := len(hist) - 1; i >= 0; i-- {
				m, ok := hist[i].(map[string]any)
				if !ok {
					continue
				}
				id, _, _ := unstructured.NestedFieldNoCopy(m, "id")
				item("#%v  %s  rev %s", id, orDash(str(m, "deployedAt")), orDash(shortRev(str(m, "revision"))))
			}
		}
	case KindApplicationSet:
		line("Project", r.Project)
		line("Status", r.Sync)
		line("Message", r.Message)
		section("Generators")
		gens, _, _ := unstructured.NestedSlice(obj, "spec", "generators")
		for _, g := range gens {
			if m, ok := g.(map[string]any); ok {
				for k := range m {
					item("%s", k)
				}
			}
		}
		section("Template")
		item("project %s", orDash(str(obj, "spec", "template", "spec", "project")))
		item("repo %s", orDash(r.Repo))
		item("destination namespace %s", orDash(str(obj, "spec", "template", "spec", "destination", "namespace")))
	case KindAppProject:
		line("Description", r.Message)
		section("Source repos")
		repos, _, _ := unstructured.NestedStringSlice(obj, "spec", "sourceRepos")
		for _, s := range repos {
			item("%s", s)
		}
		section("Destinations")
		dests, _, _ := unstructured.NestedSlice(obj, "spec", "destinations")
		for _, d := range dests {
			if m, ok := d.(map[string]any); ok {
				srv := str(m, "server")
				if srv == "" {
					srv = str(m, "name")
				}
				item("cluster %s  namespace %s", orDash(srv), orDash(str(m, "namespace")))
			}
		}
	}

	if len(r.Conditions) > 0 {
		section(fmt.Sprintf("Conditions (%d)", len(r.Conditions)))
		for _, c := range r.Conditions {
			typ := c.Type
			if typ == "" {
				typ = "?"
			}
			item("%s", typ)
			for _, l := range wrapWords(c.Message, 88) {
				b = fmt.Appendf(b, "      %s\n", l)
			}
		}
	}
	return string(b)
}

func shortRev(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func wrapWords(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if width < 20 {
		width = 20
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
			continue
		}
		if cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
