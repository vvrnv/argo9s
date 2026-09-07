package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func Describe(r Resource) string {
	var b []byte
	b = fmt.Appendf(b, "Kind:       %s\n", r.Kind)
	b = fmt.Appendf(b, "Name:       %s\n", r.Name)
	b = fmt.Appendf(b, "Namespace:  %s\n", r.Namespace)
	b = fmt.Appendf(b, "Project:    %s\n", r.Project)
	b = fmt.Appendf(b, "Sync:       %s\n", r.Sync)
	b = fmt.Appendf(b, "Health:     %s\n", r.Health)
	b = fmt.Appendf(b, "Revision:   %s\n", r.Revision)
	b = fmt.Appendf(b, "Repo:       %s\n", r.Repo)
	b = fmt.Appendf(b, "Age:        %s\n", r.AgeString())
	b = fmt.Appendf(b, "Suspended:  %v\n", r.Suspended)
	if r.Message != "" {
		b = fmt.Appendf(b, "Message:    %s\n", r.Message)
	}
	if len(r.Conditions) > 0 {
		b = fmt.Appendf(b, "\nApplication conditions (%d):\n", len(r.Conditions))
		for _, c := range r.Conditions {
			typ := c.Type
			if typ == "" {
				typ = "?"
			}
			b = fmt.Appendf(b, "  • %s\n", typ)
			if c.Message != "" {
				for _, line := range wrapWords(c.Message, 88) {
					b = fmt.Appendf(b, "      %s\n", line)
				}
			}
		}
	}
	return string(b)
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
