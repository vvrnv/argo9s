package argocd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the Argo CD HTTP API (Bearer token).
type Client struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	ServerHost string
	Insecure   bool
}

func New(server, token string, insecure bool, timeout time.Duration) (*Client, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return nil, fmt.Errorf("argocd server URL is required")
	}
	token = normalizeToken(token)
	if token == "" {
		return nil, fmt.Errorf("argocd token is empty — set tokenEnv / token in config, or --auth-token / ARGOCD_AUTH_TOKEN")
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil {
		return nil, fmt.Errorf("parse server url: %w", err)
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// Do not set Client.Timeout — large application lists and watches need long transfers.
	// Per-call deadlines come from context.
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   min(timeout, 30*time.Second),
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit user opt-in
	}
	return &Client{
		BaseURL:    strings.TrimRight(server, "/"),
		Token:      token,
		ServerHost: u.Host,
		Insecure:   insecure,
		HTTP:       &http.Client{Transport: tr},
	}, nil
}

func normalizeToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")
	return strings.TrimSpace(token)
}

// Ping verifies auth with a lightweight endpoint (version is often 404 on real servers).
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/session/userinfo", nil, nil); err == nil {
		return nil
	} else if isAuthError(err) {
		return fmt.Errorf("argocd auth failed for %s: %w", c.ServerHost, err)
	}

	// Fallback: projects list is much smaller than applications.
	if _, err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, nil); err == nil {
		return nil
	} else if isAuthError(err) {
		return fmt.Errorf("argocd auth failed for %s: %w", c.ServerHost, err)
	}

	// Last resort: applications (can be slow on large instances).
	if _, err := c.ListApplications(ctx); err != nil {
		return fmt.Errorf("argocd api unreachable (%s): %w", c.ServerHost, err)
	}
	return nil
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") ||
		strings.Contains(s, "403") ||
		strings.Contains(s, "Unauthenticated") ||
		strings.Contains(s, "no session") ||
		strings.Contains(s, "invalid session") ||
		strings.Contains(s, "permission denied")
}

func (c *Client) url(path string, query url.Values) string {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	var rdr io.Reader
	var contentType string
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
		contentType = "application/json"
	}
	return c.doReader(ctx, method, path, query, rdr, contentType)
}

func (c *Client) doReader(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, query), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509") {
			return nil, fmt.Errorf("%w (try insecure: true in config or --insecure)", err)
		}
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, res.Status, truncate(string(data), 300))
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- Applications ---

func (c *Client) ListApplications(ctx context.Context) ([]map[string]any, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v1/applications", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeItemMaps(data)
}

func decodeItemMaps(data []byte) ([]map[string]any, error) {
	var typed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &typed); err == nil && typed.Items != nil {
		out := make([]map[string]any, 0, len(typed.Items))
		for _, m := range typed.Items {
			out = append(out, unwrapResourceMap(m))
		}
		return out, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode applications: %w", err)
	}
	arr, ok := raw["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("applications response: missing items (keys=%v)", mapKeys(raw))
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, unwrapResourceMap(m))
	}
	return out, nil
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (c *Client) GetApplication(ctx context.Context, name, appNamespace string) (map[string]any, error) {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	data, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name), q, nil)
	if err != nil {
		return nil, err
	}
	var app map[string]any
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, err
	}
	return app, nil
}

func (c *Client) RefreshApplication(ctx context.Context, name, appNamespace string, hard bool) error {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	if hard {
		q.Set("refresh", "hard")
	} else {
		q.Set("refresh", "normal")
	}
	_, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name), q, nil)
	return err
}

func (c *Client) SyncApplication(ctx context.Context, name, appNamespace string) error {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	body := map[string]any{
		"name":     name,
		"prune":    true,
		"dryRun":   false,
		"revision": "",
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v1/applications/"+url.PathEscape(name)+"/sync", q, body)
	return err
}

func (c *Client) DeleteApplication(ctx context.Context, name, appNamespace string) error {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	q.Set("cascade", "true")
	_, err := c.do(ctx, http.MethodDelete, "/api/v1/applications/"+url.PathEscape(name), q, nil)
	return err
}

func (c *Client) UpdateApplication(ctx context.Context, app map[string]any) error {
	name, _ := nestedString(app, "metadata", "name")
	if name == "" {
		return fmt.Errorf("application name missing")
	}
	ns, _ := nestedString(app, "metadata", "namespace")
	q := url.Values{}
	if ns != "" {
		q.Set("appNamespace", ns)
	}
	_, err := c.do(ctx, http.MethodPut, "/api/v1/applications/"+url.PathEscape(name), q, app)
	return err
}

// ResourceTree is Argo CD's live ownership tree (pods included).
type ResourceTree struct {
	Nodes []TreeNode `json:"nodes"`
}

type TreeNode struct {
	Group          string         `json:"group"`
	Version        string         `json:"version"`
	Kind           string         `json:"kind"`
	Namespace      string         `json:"namespace"`
	Name           string         `json:"name"`
	UID            string         `json:"uid"`
	ParentRefs     []ResourceRef  `json:"parentRefs"`
	Health         *TreeHealth    `json:"health"`
	ResourceStatus string         `json:"status"`
	Info           []NodeInfoItem `json:"info"`
}

type ResourceRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type TreeHealth struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type NodeInfoItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c *Client) GetResourceTree(ctx context.Context, name, appNamespace string) (*ResourceTree, error) {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	data, err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name)+"/resource-tree", q, nil)
	if err != nil {
		return nil, err
	}
	var tree ResourceTree
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, err
	}
	return &tree, nil
}

func (c *Client) ListProjects(ctx context.Context) ([]map[string]any, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeItemMaps(data)
}

func (c *Client) ListApplicationSets(ctx context.Context) ([]map[string]any, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v1/applicationsets", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeItemMaps(data)
}

// WatchApplications prefers streaming; falls back to polling.
func (c *Client) WatchApplications(ctx context.Context, out chan<- WatchEvent) error {
	err := c.streamApplications(ctx, out)
	if err == nil || ctx.Err() != nil {
		return err
	}
	return c.pollApplications(ctx, out, 3*time.Second)
}

type WatchEvent struct {
	Type string
	App  map[string]any
	Err  error
}

func (c *Client) streamApplications(ctx context.Context, out chan<- WatchEvent) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/api/v1/stream/applications", nil), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("stream applications: %s: %s", res.Status, string(b))
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Type        string         `json:"type"`
			Application map[string]any `json:"application"`
			Result      map[string]any `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		app := ev.Application
		if app == nil {
			app = ev.Result
		}
		if app == nil {
			continue
		}
		select {
		case out <- WatchEvent{Type: strings.ToUpper(ev.Type), App: app}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sc.Err()
}

func (c *Client) pollApplications(ctx context.Context, out chan<- WatchEvent, every time.Duration) error {
	known := map[string]string{}
	first := true
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	emit := func() error {
		items, err := c.ListApplications(ctx)
		if err != nil {
			select {
			case out <- WatchEvent{Err: err}:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}
		seen := map[string]bool{}
		for _, app := range items {
			key := appKey(app)
			seen[key] = true
			fp := appFingerprint(app)
			prev, ok := known[key]
			typ := "MODIFIED"
			if !ok {
				typ = "ADDED"
			} else if prev == fp && !first {
				continue
			}
			known[key] = fp
			select {
			case out <- WatchEvent{Type: typ, App: app}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if !first {
			for key := range known {
				if seen[key] {
					continue
				}
				delete(known, key)
				parts := strings.SplitN(key, "/", 2)
				ns, name := "", key
				if len(parts) == 2 {
					ns, name = parts[0], parts[1]
				}
				app := map[string]any{
					"metadata": map[string]any{"name": name, "namespace": ns},
				}
				select {
				case out <- WatchEvent{Type: "DELETED", App: app}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		first = false
		return nil
	}

	if err := emit(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := emit(); err != nil {
				return err
			}
		}
	}
}

func appKey(app map[string]any) string {
	ns, _ := nestedString(app, "metadata", "namespace")
	name, _ := nestedString(app, "metadata", "name")
	return ns + "/" + name
}

func appFingerprint(app map[string]any) string {
	rv, _ := nestedString(app, "metadata", "resourceVersion")
	sync, _ := nestedString(app, "status", "sync", "status")
	health, _ := nestedString(app, "status", "health", "status")
	return rv + "|" + sync + "|" + health
}

func nestedString(m map[string]any, path ...string) (string, bool) {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = obj[p]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

// Version returns the Argo CD server version (GET /api/version, no auth needed).
func (c *Client) Version(ctx context.Context) (string, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/version", nil, nil)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	return strings.SplitN(v.Version, "+", 2)[0], nil // drop the "+commit" suffix
}
