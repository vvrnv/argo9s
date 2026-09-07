package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GetResourceManifest returns live manifest YAML/JSON text for a managed resource.
func (c *Client) GetResourceManifest(ctx context.Context, appName, appNamespace, group, version, kind, namespace, resourceName string) (string, error) {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	if namespace != "" {
		q.Set("namespace", namespace)
	}
	q.Set("resourceName", resourceName)
	q.Set("kind", kind)
	if group != "" {
		q.Set("group", group)
	}
	if version != "" {
		q.Set("version", version)
	}
	data, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(appName)+"/resource", q, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("decode resource: %w", err)
	}
	if resp.Manifest == "" {
		return "", fmt.Errorf("empty manifest for %s/%s", kind, resourceName)
	}
	return resp.Manifest, nil
}

// PatchResource applies a patch to a live managed resource.
func (c *Client) PatchResource(ctx context.Context, appName, appNamespace, group, version, kind, namespace, resourceName, patch, patchType string) error {
	if patchType == "" {
		patchType = "application/merge-patch+json"
	}
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	if namespace != "" {
		q.Set("namespace", namespace)
	}
	q.Set("resourceName", resourceName)
	q.Set("kind", kind)
	q.Set("patchType", patchType)
	if group != "" {
		q.Set("group", group)
	}
	if version != "" {
		q.Set("version", version)
	}
	// Body is the patch field (raw merge-patch JSON).
	_, err := c.doReader(ctx, http.MethodPost, "/api/v1/applications/"+url.PathEscape(appName)+"/resource", q, strings.NewReader(patch), patchType)
	return err
}
