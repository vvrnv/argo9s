package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/vvrnv/argo9s/internal/argo"
	"github.com/vvrnv/argo9s/internal/argocd"
	"github.com/vvrnv/argo9s/internal/config"
	"github.com/vvrnv/argo9s/internal/kube"
)

const (
	TypeKubernetes = config.ConnKubernetes
	TypeArgoCD     = config.ConnArgoCD
	TypeDemo       = "demo"
)

// Session is a resolved connection ready for the TUI.
type Session struct {
	Name    string
	Type    string // kubernetes | argocd | demo
	Label   string // header context string
	Cfg     config.Config
	Kube    *kube.Client
	Argo    *argocd.Client
	Store   *argo.Store
	Ops     argo.Mutator
	Conn    config.Connection
}

func Open(ctx context.Context, cfg config.Config, conn config.Connection, kubeContextFlag string, timeout time.Duration) (*Session, error) {
	store := argo.NewStore()
	readOnly := conn.EffectiveReadOnly(cfg.ReadOnly)
	s := &Session{
		Name:  conn.Name,
		Type:  conn.Type,
		Cfg:   cfg,
		Store: store,
		Conn:  conn,
	}

	switch conn.Type {
	case config.ConnArgoCD:
		if err := conn.ValidateArgo(); err != nil {
			return nil, err
		}
		token := conn.ResolvedToken()
		client, err := argocd.New(conn.Server, token, conn.Insecure, timeout)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", conn.Name, err)
		}
		if err := client.Ping(ctx); err != nil {
			return nil, fmt.Errorf("connection %q (%s via %s): %w", conn.Name, conn.Server, conn.TokenSourceHint(), err)
		}
		s.Argo = client
		s.Ops = argocd.NewOperator(client, readOnly)
		s.Label = fmt.Sprintf("argocd:%s", client.ServerHost)
		return s, nil

	default: // kubernetes
		ctxName := conn.Context
		if kubeContextFlag != "" {
			ctxName = kubeContextFlag
		}
		ns := cfg.Namespace
		if cfg.AllNamespaces {
			ns = ""
		}
		client, err := kube.New(ctxName, ns, timeout)
		if err != nil {
			return nil, err
		}
		if err := client.Ping(ctx); err != nil {
			return nil, err
		}
		s.Kube = client
		s.Ops = argo.NewOperator(client, readOnly)
		s.Label = client.Context
		if s.Label == "" {
			s.Label = "kubernetes"
		}
		return s, nil
	}
}

// StartWatch begins pushing events into out (caller closes out when done).
func (s *Session) StartWatch(ctx context.Context, out chan<- argo.Event) error {
	switch s.Type {
	case config.ConnArgoCD:
		w := argocd.NewWatcher(s.Argo, s.Store)
		return w.Start(ctx, out)
	case TypeDemo:
		<-ctx.Done()
		return ctx.Err()
	default:
		ns := s.Cfg.Namespace
		allNS := s.Cfg.AllNamespaces || ns == "all"
		w := argo.NewWatcher(s.Kube, s.Store)
		return w.Start(ctx, argo.KindAll, ns, allNS, out)
	}
}

func (s *Session) EnrichGraph(ctx context.Context, g *argo.Graph) error {
	switch s.Type {
	case config.ConnArgoCD:
		return argocd.EnrichGraph(ctx, s.Argo, g)
	case TypeDemo:
		// Seeded status.resources already include RS/Pods.
		return nil
	default:
		return argo.EnrichGraphWithLiveWorkloads(ctx, s.Kube, g)
	}
}

// GetApplication loads a single Application as a Resource.
func (s *Session) GetApplication(ctx context.Context, name, namespace string) (argo.Resource, error) {
	if name == "" {
		return argo.Resource{}, fmt.Errorf("application name required")
	}
	if namespace == "" {
		namespace = s.Cfg.Namespace
	}
	if namespace == "" || namespace == "all" {
		namespace = "argocd"
	}
	switch s.Type {
	case config.ConnArgoCD:
		if s.Argo == nil {
			return argo.Resource{}, fmt.Errorf("argocd client not configured")
		}
		app, err := s.Argo.GetApplication(ctx, name, namespace)
		if err != nil {
			return argo.Resource{}, err
		}
		r := argocd.ResourceFromAppMap(app)
		if r.Name == "" {
			return argo.Resource{}, fmt.Errorf("application %q not found or unreadable", name)
		}
		return r, nil
	default:
		if s.Kube == nil {
			return argo.Resource{}, fmt.Errorf("kubernetes client not configured")
		}
		gvr, _ := argo.GVRFor(argo.KindApplication)
		obj, err := s.Kube.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return argo.Resource{}, err
		}
		return argo.FromUnstructured(argo.KindApplication, obj), nil
	}
}

// StreamPodLogs streams container logs for a Pod under an Application.
func (s *Session) StreamPodLogs(ctx context.Context, app argo.Resource, podNS, podName, container string, follow bool, previous bool, tailLines int64, emit func(line string) error) error {
	if emit == nil {
		return fmt.Errorf("nil emit")
	}
	switch s.Type {
	case config.ConnArgoCD:
		if s.Argo == nil {
			return fmt.Errorf("argocd client not configured")
		}
		return s.Argo.StreamPodLogs(ctx, argocd.PodLogOptions{
			AppName:      app.Name,
			AppNamespace: app.Namespace,
			PodNamespace: podNS,
			PodName:      podName,
			Container:    container,
			Follow:       follow,
			TailLines:    tailLines,
			Previous:     previous,
		}, emit)
	default:
		if s.Kube == nil {
			return fmt.Errorf("kubernetes client not configured")
		}
		dest := kube.Destination{}
		if app.Raw != nil {
			dest.Server, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "server")
			dest.Name, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "name")
			dest.Namespace, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "namespace")
		}
		client := s.Kube
		if tc, err := s.Kube.ClientForDestination(ctx, dest, app.Namespace, "argocd"); err == nil && tc != nil {
			client = tc
		}
		var tail *int64
		if tailLines > 0 {
			t := tailLines
			tail = &t
		}
		return client.StreamPodLogs(ctx, kube.PodLogOptions{
			Namespace: podNS,
			Pod:       podName,
			Container: container,
			Follow:    follow,
			Previous:  previous,
			TailLines: tail,
		}, emit)
	}
}

func nestedStr(obj map[string]any, path ...string) (string, bool, error) {
	cur := any(obj)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur, ok = m[p]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}

// ResourceRef identifies a live Kubernetes object under an Application.
type ResourceRef struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
}

// GetResourceYAML returns live YAML for an Application-managed resource (or the Application itself).
func (s *Session) GetResourceYAML(ctx context.Context, app argo.Resource, ref ResourceRef) (string, error) {
	if strings.EqualFold(ref.Kind, "Application") || ref.Kind == "" && ref.Name == app.Name {
		return s.Ops.YAML(app)
	}
	group, version := ref.Group, ref.Version
	if version == "" {
		group, version = defaultGVK(ref.Kind)
		if ref.Group != "" {
			group = ref.Group
		}
	}
	switch s.Type {
	case config.ConnArgoCD:
		if s.Argo == nil {
			return "", fmt.Errorf("argocd client not configured")
		}
		manifest, err := s.Argo.GetResourceManifest(ctx, app.Name, app.Namespace, group, version, ref.Kind, ref.Namespace, ref.Name)
		if err != nil {
			return "", err
		}
		// Manifest is often JSON — reformat as YAML when possible.
		var obj any
		if json.Unmarshal([]byte(manifest), &obj) == nil {
			if b, err := yaml.Marshal(obj); err == nil {
				return string(b), nil
			}
		}
		return manifest, nil
	default:
		if s.Kube == nil {
			return "", fmt.Errorf("kubernetes client not configured")
		}
		client := s.destinationClient(ctx, app)
		return client.GetResourceYAML(ctx, group, version, ref.Kind, ref.Namespace, ref.Name)
	}
}

// ApplyResourceYAML writes edited YAML back to the live resource.
func (s *Session) ApplyResourceYAML(ctx context.Context, app argo.Resource, ref ResourceRef, yamlText string) error {
	if s.Ops != nil && s.Ops.ReadOnly() {
		return fmt.Errorf("read-only mode")
	}
	group, version := ref.Group, ref.Version
	if version == "" {
		group, version = defaultGVK(ref.Kind)
		if ref.Group != "" {
			group = ref.Group
		}
	}
	switch s.Type {
	case config.ConnArgoCD:
		if s.Argo == nil {
			return fmt.Errorf("argocd client not configured")
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(yamlText), &obj); err != nil {
			return fmt.Errorf("parse yaml: %w", err)
		}
		patch, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		return s.Argo.PatchResource(ctx, app.Name, app.Namespace, group, version, ref.Kind, ref.Namespace, ref.Name, string(patch), "application/merge-patch+json")
	default:
		if s.Kube == nil {
			return fmt.Errorf("kubernetes client not configured")
		}
		client := s.destinationClient(ctx, app)
		return client.ApplyResourceYAML(ctx, group, version, ref.Kind, ref.Namespace, ref.Name, yamlText)
	}
}

func (s *Session) destinationClient(ctx context.Context, app argo.Resource) *kube.Client {
	dest := kube.Destination{}
	if app.Raw != nil {
		dest.Server, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "server")
		dest.Name, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "name")
		dest.Namespace, _, _ = nestedStr(app.Raw.Object, "spec", "destination", "namespace")
	}
	if tc, err := s.Kube.ClientForDestination(ctx, dest, app.Namespace, "argocd"); err == nil && tc != nil {
		return tc
	}
	return s.Kube
}

func defaultGVK(kind string) (group, version string) {
	switch strings.ToLower(kind) {
	case "pod", "service", "configmap", "secret", "persistentvolumeclaim", "serviceaccount":
		return "", "v1"
	case "deployment", "replicaset", "statefulset", "daemonset":
		return "apps", "v1"
	case "job", "cronjob":
		return "batch", "v1"
	case "ingress":
		return "networking.k8s.io", "v1"
	case "rollout", "application", "applicationset", "appproject":
		return "argoproj.io", "v1alpha1"
	default:
		return "", "v1"
	}
}
