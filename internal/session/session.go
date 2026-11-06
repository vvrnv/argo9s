package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	Name  string
	Type  string // kubernetes | argocd | demo
	Label string // header context string
	Cfg   config.Config
	Kube  *kube.Client
	Argo  *argocd.Client
	Store *argo.Store
	Ops   argo.Mutator
	Conn  config.Connection
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
	case TypeDemo:
		return demoLogs(podName, container, emit)
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

// ContainerInfo is one container of a pod, for the logs / shell picker.
type ContainerInfo struct {
	Name, Image, State string
	Ready              bool
	Restarts           int32
}

// PodContainers lists a pod's containers (default-container annotation first).
// It reads the live Pod manifest, so it works for both backends.
func (s *Session) PodContainers(ctx context.Context, app argo.Resource, ns, pod string) ([]ContainerInfo, error) {
	if s.Type == TypeDemo { // offline demo: every pod runs an app + a sidecar
		return []ContainerInfo{
			{Name: "app", Image: "ghcr.io/example/" + app.Name + ":1.4.2", State: "running", Ready: true},
			{Name: "envoy", Image: "envoyproxy/envoy:v1.31", State: "running", Ready: true, Restarts: 1},
		}, nil
	}
	text, err := s.GetResourceYAML(ctx, app, ResourceRef{Version: "v1", Kind: "Pod", Namespace: ns, Name: pod})
	if err != nil {
		return nil, err
	}
	return containersFromPodYAML(text, ns, pod)
}

func containersFromPodYAML(text, ns, pod string) ([]ContainerInfo, error) {
	var p corev1.Pod
	if err := yaml.Unmarshal([]byte(text), &p); err != nil {
		return nil, fmt.Errorf("parse pod %s/%s: %w", ns, pod, err)
	}
	status := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.ContainerStatuses {
		status[cs.Name] = cs
	}
	def := p.Annotations["kubectl.kubernetes.io/default-container"]
	var out []ContainerInfo
	for _, c := range p.Spec.Containers {
		cs := status[c.Name]
		state := "waiting"
		switch {
		case cs.State.Running != nil:
			state = "running"
		case cs.State.Terminated != nil:
			state = "terminated: " + cs.State.Terminated.Reason
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
			state = cs.State.Waiting.Reason
		}
		ci := ContainerInfo{Name: c.Name, Image: c.Image, State: state, Ready: cs.Ready, Restarts: cs.RestartCount}
		if c.Name == def {
			out = append([]ContainerInfo{ci}, out...)
		} else {
			out = append(out, ci)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pod %s/%s has no containers", ns, pod)
	}
	return out, nil
}

// PodShell opens an interactive shell in a pod (kubernetes connections only:
// the Argo CD API terminal needs exec.enabled server-side and isn't wired yet).
func (s *Session) PodShell(ctx context.Context, app argo.Resource, ns, pod, container string) (*kube.PodShell, error) {
	if s.Type != config.ConnKubernetes || s.Kube == nil {
		return nil, fmt.Errorf("shell needs a kubernetes connection (not available over the Argo CD API yet)")
	}
	return s.destinationClient(ctx, app).PodShell(ns, pod, container), nil
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

// demoLogs emits plausible access logs for --demo screenshots (no cluster).
func demoLogs(pod, container string, emit func(string) error) error {
	paths := []string{"/healthz", "/api/v1/orders", "/api/v1/users/42", "/readyz", "/api/v1/cart"}
	codes := []int{200, 200, 201, 200, 404, 200, 500, 200}
	base := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		line := fmt.Sprintf("%s %s %s %s %d %dms", base.Add(time.Duration(i)*1300*time.Millisecond).Format(time.RFC3339),
			container, []string{"GET", "POST"}[i%2], paths[i%len(paths)], codes[i%len(codes)], 3+i%40)
		if err := emit(line); err != nil {
			return err
		}
	}
	return nil
}

// Versions returns the Argo CD version and, on kubernetes connections, the
// Kubernetes API version. Empty strings when unknown (never fatal).
func (s *Session) Versions(ctx context.Context) (argoVer, k8sVer string) {
	switch s.Type {
	case TypeDemo:
		return "v2.13.1", "v1.31.2"
	case config.ConnArgoCD:
		if s.Argo != nil {
			argoVer, _ = s.Argo.Version(ctx)
		}
	default:
		if s.Kube == nil {
			return "", ""
		}
		ns := s.Cfg.Namespace
		if ns == "" || ns == "all" {
			ns = "argocd"
		}
		argoVer, _ = s.Kube.ArgoCDVersion(ctx, ns)
		k8sVer, _ = s.Kube.ServerVersion()
	}
	return argoVer, k8sVer
}
