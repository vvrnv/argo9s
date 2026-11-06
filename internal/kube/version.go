package kube

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServerVersion is the Kubernetes API server version (k9s "K8s Rev").
func (c *Client) ServerVersion() (string, error) {
	v, err := c.Typed.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// ArgoCDVersion reads the installed Argo CD version from the argocd-server
// Deployment: its app.kubernetes.io/version label (Helm / manifests), else the
// image tag.
func (c *Client) ArgoCDVersion(ctx context.Context, namespace string) (string, error) {
	list, err := c.Typed.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=argocd-server",
	})
	if err != nil {
		return "", err
	}
	for _, d := range list.Items {
		if v := d.Labels["app.kubernetes.io/version"]; v != "" {
			return v, nil
		}
		for _, ct := range d.Spec.Template.Spec.Containers {
			if i := strings.LastIndex(ct.Image, ":"); i > 0 && !strings.Contains(ct.Image[i:], "/") {
				return strings.SplitN(ct.Image[i+1:], "@", 2)[0], nil
			}
		}
	}
	return "", nil
}
