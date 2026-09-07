package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const clusterSecretLabel = "argocd.argoproj.io/secret-type=cluster"

// Destination describes where an Argo Application deploys.
type Destination struct {
	Server    string
	Name      string // optional cluster name (destination.name)
	Namespace string
}

// clusterConfig mirrors Argo CD's secret `config` JSON (subset we support).
type clusterConfig struct {
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"`
	BearerToken        string `json:"bearerToken,omitempty"`
	TLSClientConfig    tlsCfg `json:"tlsClientConfig"`
	DisableCompression bool   `json:"disableCompression,omitempty"`
}

type tlsCfg struct {
	Insecure   bool   `json:"insecure"`
	ServerName string `json:"serverName,omitempty"`
	CertData   []byte `json:"certData,omitempty"`
	KeyData    []byte `json:"keyData,omitempty"`
	CAData     []byte `json:"caData,omitempty"`
}

var destClientCache sync.Map // server|name -> *Client

// ClientForDestination returns a kube client for the Application destination.
// Local destinations reuse hub; remote ones use Argo CD cluster secrets.
func (c *Client) ClientForDestination(ctx context.Context, dest Destination, secretNamespaces ...string) (*Client, error) {
	if c == nil || c.Typed == nil {
		return nil, fmt.Errorf("nil kube client")
	}

	// In-cluster / same API server, and no named remote cluster.
	if isLocalDestination(c, dest.Server) && strings.TrimSpace(dest.Name) == "" {
		return c, nil
	}

	cacheKey := normalizeServer(dest.Server) + "|" + strings.TrimSpace(dest.Name)
	if v, ok := destClientCache.Load(cacheKey); ok {
		return v.(*Client), nil
	}

	nss := make([]string, 0, len(secretNamespaces)+1)
	seen := map[string]bool{}
	for _, ns := range secretNamespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		nss = append(nss, ns)
	}
	if !seen["argocd"] {
		nss = append(nss, "argocd")
	}

	var lastErr error
	for _, ns := range nss {
		client, err := c.clientFromClusterSecrets(ctx, ns, dest)
		if err != nil {
			lastErr = err
			continue
		}
		destClientCache.Store(cacheKey, client)
		return client, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no Argo CD cluster secret for server=%q name=%q", dest.Server, dest.Name)
}

func (c *Client) clientFromClusterSecrets(ctx context.Context, namespace string, dest Destination) (*Client, error) {
	list, err := c.Typed.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: clusterSecretLabel,
	})
	if err != nil {
		return nil, fmt.Errorf("list cluster secrets in %s: %w", namespace, err)
	}
	wantServer := normalizeServer(dest.Server)
	wantName := strings.TrimSpace(dest.Name)

	var byServer, byName *corev1.Secret
	for i := range list.Items {
		s := &list.Items[i]
		server := normalizeServer(string(s.Data["server"]))
		name := strings.TrimSpace(string(s.Data["name"]))
		if wantServer != "" && server == wantServer {
			byServer = s
			break
		}
		if wantName != "" && name == wantName {
			byName = s
		}
	}
	match := byServer
	if match == nil {
		match = byName
	}
	if match == nil {
		// Local URL but named destination didn't match — fall back to hub only if local.
		if isLocalDestination(c, dest.Server) {
			return c, nil
		}
		return nil, fmt.Errorf("cluster secret not found in %s (server=%q name=%q, have %d)", namespace, dest.Server, dest.Name, len(list.Items))
	}

	restCfg, err := restConfigFromClusterSecret(match)
	if err != nil {
		return nil, err
	}
	label := string(match.Data["name"])
	if label == "" {
		label = match.Name
	}
	return clientFromREST(restCfg, label)
}

func restConfigFromClusterSecret(s *corev1.Secret) (*rest.Config, error) {
	server := strings.TrimSpace(string(s.Data["server"]))
	if server == "" {
		return nil, fmt.Errorf("cluster secret %s/%s: empty server", s.Namespace, s.Name)
	}
	raw := s.Data["config"]
	if len(raw) == 0 {
		return nil, fmt.Errorf("cluster secret %s/%s: empty config", s.Namespace, s.Name)
	}
	var cc clusterConfig
	if err := json.Unmarshal(raw, &cc); err != nil {
		return nil, fmt.Errorf("cluster secret %s/%s: decode config: %w", s.Namespace, s.Name, err)
	}

	cfg := &rest.Config{
		Host:        server,
		BearerToken: cc.BearerToken,
		Username:    cc.Username,
		Password:    cc.Password,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   cc.TLSClientConfig.Insecure,
			ServerName: cc.TLSClientConfig.ServerName,
			CertData:   cc.TLSClientConfig.CertData,
			KeyData:    cc.TLSClientConfig.KeyData,
			CAData:     cc.TLSClientConfig.CAData,
		},
		DisableCompression: cc.DisableCompression,
	}
	if cfg.BearerToken == "" && cfg.Username == "" && len(cfg.CertData) == 0 {
		return nil, fmt.Errorf("cluster secret %s/%s: no bearerToken/user/cert (exec/aws auth not supported yet)", s.Namespace, s.Name)
	}
	return cfg, nil
}

func clientFromREST(restCfg *rest.Config, label string) (*Client, error) {
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("destination typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("destination dynamic client: %w", err)
	}
	if label == "" {
		label = restCfg.Host
	}
	return &Client{
		Config:  restCfg,
		Typed:   typed,
		Dynamic: dyn,
		Context: "argocd-dest:" + label,
	}, nil
}

func isLocalDestination(hub *Client, server string) bool {
	server = normalizeServer(server)
	if server == "" {
		return true
	}
	if strings.Contains(server, "kubernetes.default.svc") {
		return true
	}
	if hub != nil && hub.Config != nil && normalizeServer(hub.Config.Host) == server {
		return true
	}
	return false
}

func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/")
	return s
}

// ClearDestinationCache drops cached destination clients (tests / context switch).
func ClearDestinationCache() {
	destClientCache.Range(func(k, _ any) bool {
		destClientCache.Delete(k)
		return true
	})
}
