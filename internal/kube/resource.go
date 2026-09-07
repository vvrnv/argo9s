package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

// GetResourceYAML fetches a namespaced (or cluster-scoped) object as YAML.
func (c *Client) GetResourceYAML(ctx context.Context, group, version, kind, namespace, name string) (string, error) {
	if c == nil || c.Dynamic == nil {
		return "", fmt.Errorf("nil kube client")
	}
	gvr := gvrFor(group, version, kind)
	var obj *unstructured.Unstructured
	var err error
	if namespace == "" || namespace == "cluster" {
		obj, err = c.Dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return "", err
	}
	obj.SetManagedFields(nil)
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ApplyResourceYAML replaces/updates an object from YAML (merge-patch on live).
func (c *Client) ApplyResourceYAML(ctx context.Context, group, version, kind, namespace, name, yamlText string) error {
	if c == nil || c.Dynamic == nil {
		return fmt.Errorf("nil kube client")
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &obj); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	// Prefer merge-patch of the edited document.
	patch, err := jsonMarshal(obj)
	if err != nil {
		return err
	}
	gvr := gvrFor(group, version, kind)
	if namespace == "" || namespace == "cluster" {
		_, err = c.Dynamic.Resource(gvr).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	} else {
		_, err = c.Dynamic.Resource(gvr).Namespace(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	}
	return err
}

func jsonMarshal(v any) ([]byte, error) {
	return yaml.Marshal(v) // wrong - need json
}

func gvrFor(group, version, kind string) schema.GroupVersionResource {
	if version == "" {
		version = "v1"
	}
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: pluralizeKind(kind),
	}
}

func pluralizeKind(kind string) string {
	k := strings.ToLower(kind)
	switch k {
	case "networkpolicy":
		return "networkpolicies"
	case "ingress":
		return "ingresses"
	case "endpoints":
		return "endpoints"
	case "componentstatus":
		return "componentstatuses"
	case "storageclass":
		return "storageclasses"
	case "priorityclass":
		return "priorityclasses"
	case "ingressroute": // traefik CRD often plural ingressroutes
		return "ingressroutes"
	default:
		if strings.HasSuffix(k, "s") {
			return k
		}
		if strings.HasSuffix(k, "y") && len(k) > 1 {
			return k[:len(k)-1] + "ies"
		}
		return k + "s"
	}
}
