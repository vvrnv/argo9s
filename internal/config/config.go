package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	AppName         = "argo9s"
	DefaultNS       = "argocd"
	DefaultReadOnly = true

	ConnKubernetes = "kubernetes"
	ConnArgoCD     = "argocd"
)

// Connection is a named backend: in-cluster/kubeconfig or Argo CD API.
type Connection struct {
	Name string `json:"name"`
	Type string `json:"type"` // kubernetes | argocd

	// Per-connection override; nil means use global Config.ReadOnly.
	ReadOnly *bool `json:"readOnly,omitempty"`

	// kubernetes
	Context string `json:"context,omitempty"`

	// argocd API
	Server   string `json:"server,omitempty"`   // https://argocd.example.com
	Token    string `json:"token,omitempty"`    // prefer tokenEnv for secrets
	TokenEnv string `json:"tokenEnv,omitempty"` // e.g. ARGOCD_PROD_TOKEN
	Insecure bool   `json:"insecure,omitempty"` // skip TLS verify
	GRPCWeb  bool   `json:"grpcWeb,omitempty"`  // reserved / future
}

// EffectiveReadOnly returns connection override or global default.
func (c Connection) EffectiveReadOnly(global bool) bool {
	if c.ReadOnly != nil {
		return *c.ReadOnly
	}
	return global
}

func (c Connection) ResolvedToken() string {
	// People sometimes paste the JWT into tokenEnv by mistake.
	if looksLikeToken(c.TokenEnv) {
		return normalizeConfigToken(c.TokenEnv)
	}
	if c.TokenEnv != "" {
		if v := normalizeConfigToken(os.Getenv(c.TokenEnv)); v != "" {
			return v
		}
	}
	if v := normalizeConfigToken(c.Token); v != "" {
		return v
	}
	for _, key := range []string{"ARGOCD_AUTH_TOKEN", "ARGOCD_TOKEN"} {
		if v := normalizeConfigToken(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func looksLikeToken(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "eyJ") || strings.Count(s, ".") >= 2 && len(s) > 40
}

func (c Connection) TokenSourceHint() string {
	if c.TokenEnv != "" {
		if normalizeConfigToken(os.Getenv(c.TokenEnv)) != "" {
			return "env " + c.TokenEnv
		}
	}
	if normalizeConfigToken(c.Token) != "" {
		return "config token"
	}
	if normalizeConfigToken(os.Getenv("ARGOCD_AUTH_TOKEN")) != "" {
		return "env ARGOCD_AUTH_TOKEN"
	}
	if c.TokenEnv != "" {
		return "env " + c.TokenEnv + " (empty)"
	}
	return "none"
}

func normalizeConfigToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")
	return strings.TrimSpace(token)
}

// ValidateArgo checks required fields before dialing.
func (c Connection) ValidateArgo() error {
	if strings.TrimSpace(c.Server) == "" {
		return fmt.Errorf("connection %q: server URL is required", c.Name)
	}
	if c.ResolvedToken() == "" {
		if c.TokenEnv != "" {
			return fmt.Errorf("connection %q: environment variable %s is empty (export it, or set token: in config)", c.Name, c.TokenEnv)
		}
		return fmt.Errorf("connection %q: no token (set tokenEnv / token, or ARGOCD_AUTH_TOKEN / --auth-token)", c.Name)
	}
	return nil
}

func (c Connection) Display() string {
	switch strings.ToLower(c.Type) {
	case ConnArgoCD:
		return fmt.Sprintf("%s (argocd %s)", c.Name, c.Server)
	default:
		if c.Context != "" {
			return fmt.Sprintf("%s (kubernetes ctx:%s)", c.Name, c.Context)
		}
		return fmt.Sprintf("%s (kubernetes)", c.Name)
	}
}

// Config holds persistent argo9s settings.
type Config struct {
	Namespace             string       `json:"namespace"`
	ReadOnly              bool         `json:"readOnly"`
	NoColor               bool         `json:"noColor,omitempty"`
	ConnectTimeoutSeconds int          `json:"connectTimeoutSeconds"`
	AllNamespaces         bool         `json:"allNamespaces"`
	DefaultConnection     string       `json:"defaultConnection,omitempty"`
	Connections           []Connection `json:"connections,omitempty"`
}

func Default() Config {
	return Config{
		Namespace:             DefaultNS,
		ReadOnly:              DefaultReadOnly,
		ConnectTimeoutSeconds: 15,
		AllNamespaces:         false,
		DefaultConnection:     "kubernetes",
		Connections: []Connection{
			{Name: "kubernetes", Type: ConnKubernetes},
		},
	}
}

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "."+AppName), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	cfg.normalize()
	return cfg, nil
}

func (cfg *Config) normalize() {
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNS
	}
	if cfg.ConnectTimeoutSeconds <= 0 {
		cfg.ConnectTimeoutSeconds = 15
	}
	if len(cfg.Connections) == 0 {
		cfg.Connections = []Connection{{Name: "kubernetes", Type: ConnKubernetes}}
	}
	for i := range cfg.Connections {
		if cfg.Connections[i].Name == "" {
			cfg.Connections[i].Name = fmt.Sprintf("conn-%d", i+1)
		}
		if cfg.Connections[i].Type == "" {
			if cfg.Connections[i].Server != "" {
				cfg.Connections[i].Type = ConnArgoCD
			} else {
				cfg.Connections[i].Type = ConnKubernetes
			}
		}
		cfg.Connections[i].Type = strings.ToLower(cfg.Connections[i].Type)
	}
	if cfg.DefaultConnection == "" {
		cfg.DefaultConnection = cfg.Connections[0].Name
	}
}

func Save(cfg Config) error {
	cfg.normalize()
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (cfg Config) FindConnection(name string) (Connection, bool) {
	name = strings.TrimSpace(name)
	for _, c := range cfg.Connections {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return Connection{}, false
}

// EnsureBuiltinKubernetes makes sure a kubernetes profile exists.
func (cfg *Config) EnsureBuiltinKubernetes() {
	cfg.normalize()
	for _, c := range cfg.Connections {
		if c.Type == ConnKubernetes {
			return
		}
	}
	cfg.Connections = append([]Connection{{Name: "kubernetes", Type: ConnKubernetes}}, cfg.Connections...)
}

func Set(key, value string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	switch key {
	case "namespace":
		cfg.Namespace = value
		cfg.AllNamespaces = value == "all"
	case "readOnly":
		cfg.ReadOnly = value == "true" || value == "1" || value == "yes"
	case "noColor", "nocolor", "no-color":
		cfg.NoColor = value == "true" || value == "1" || value == "yes"
	case "allNamespaces":
		cfg.AllNamespaces = value == "true" || value == "1" || value == "yes"
	case "defaultConnection":
		if _, ok := cfg.FindConnection(value); !ok && value != "" {
			return fmt.Errorf("unknown connection %q", value)
		}
		cfg.DefaultConnection = value
	case "connectTimeoutSeconds":
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err != nil || n <= 0 {
			return fmt.Errorf("connectTimeoutSeconds must be a positive integer")
		}
		cfg.ConnectTimeoutSeconds = n
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return Save(cfg)
}

// ExampleYAML is written by `argo9s config init`.
func ExampleYAML() string {
	return `# ~/.argo9s/config.yaml
namespace: argocd
readOnly: true
noColor: false              # true = monochrome TUI (also: --no-color / NO_COLOR=1)
connectTimeoutSeconds: 15
defaultConnection: kubernetes

connections:
  - name: kubernetes
    type: kubernetes
    readOnly: true          # per-connection (overrides global readOnly)
    # context: my-cluster   # optional kube context override

  - name: argocd-test
    type: argocd
    server: https://argocd.example.com
    readOnly: false
    # Prefer env var (do not commit tokens):
    tokenEnv: ARGOCD_TEST_TOKEN
    # Or inline for local-only:
    # token: "eyJhbGciOi..."
    insecure: false

  - name: argocd-prod
    type: argocd
    server: https://argocd.prod.example.com
    tokenEnv: ARGOCD_PROD_TOKEN
    readOnly: true
    insecure: false

# Create a token: argocd account generate-token
# Or UI: Settings → Accounts → Generate New
`
}
