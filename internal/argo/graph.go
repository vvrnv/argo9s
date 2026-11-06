package argo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/vvrnv/argo9s/internal/kube"
)

// ManagedResource is one entry from Application.status.resources.
type ManagedResource struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
	Status    string
	Health    string
	Hook      bool
}

// GraphNode is a navigable node in the application resource graph.
type GraphNode struct {
	ID        string
	Group     string
	Version   string
	Kind      string // Deployment, Pod, …
	KindShort string
	Name      string
	Namespace string
	Title     string // display kind label (short)
	Subtitle  string
	Status    string
	Health    string
	Depth     int
	Parent    int // index of parent in Nodes, -1 for root
	IsGroup   bool
	Count     int
	Meta      string // e.g. "running 1/1"
}

// Graph is a flattened application resource tree (Argo UI analogue).
type Graph struct {
	Root  Resource
	Nodes []GraphNode
}

func ParseManagedResources(obj *unstructured.Unstructured) []ManagedResource {
	if obj == nil {
		return nil
	}
	raw, ok, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
	if !ok {
		return nil
	}
	out := make([]ManagedResource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mr := ManagedResource{
			Group:     asString(m["group"]),
			Version:   asString(m["version"]),
			Kind:      asString(m["kind"]),
			Namespace: asString(m["namespace"]),
			Name:      asString(m["name"]),
			Status:    asString(m["status"]),
			Hook:      asBool(m["hook"]),
		}
		if h, ok := m["health"].(map[string]any); ok {
			mr.Health = asString(h["status"])
		}
		if mr.Kind == "" || mr.Name == "" {
			continue
		}
		out = append(out, mr)
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// KindShortName mirrors Argo CD UI abbreviations.
func KindShortName(kind string) string {
	switch strings.ToLower(kind) {
	case "application":
		return "app"
	case "configmap":
		return "cm"
	case "secret":
		return "secret"
	case "service":
		return "svc"
	case "deployment":
		return "deploy"
	case "replicaset":
		return "rs"
	case "statefulset":
		return "sts"
	case "daemonset":
		return "ds"
	case "pod":
		return "pod"
	case "ingress":
		return "ing"
	case "ingressroute":
		return "ingressroute"
	case "serviceaccount":
		return "sa"
	case "persistentvolumeclaim":
		return "pvc"
	case "horizontalpodautoscaler":
		return "hpa"
	case "job":
		return "job"
	case "cronjob":
		return "cronjob"
	default:
		return strings.ToLower(kind)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func nodeFromManaged(mr ManagedResource, depth, parent int) GraphNode {
	ns := mr.Namespace
	if ns == "" {
		ns = "cluster"
	}
	sub := mr.Name
	if mr.Hook {
		sub += " (hook)"
	}
	return GraphNode{
		ID:        fmt.Sprintf("%s/%s/%s/%s", mr.Group, mr.Kind, mr.Namespace, mr.Name),
		Group:     mr.Group,
		Version:   mr.Version,
		Kind:      mr.Kind,
		KindShort: KindShortName(mr.Kind),
		Name:      mr.Name,
		Namespace: mr.Namespace,
		Title:     KindShortName(mr.Kind),
		Subtitle:  sub,
		Status:    mr.Status,
		Health:    mr.Health,
		Depth:     depth,
		Parent:    parent,
	}
}

// BuildAppGraph builds Application → managed resources in an Argo UI–like tree.
// ReplicaSets/Pods from status.resources are parented under Deployments (etc.) by
// name prefix so the graph works even when live kube enrichment can't reach the
// destination cluster. EnrichGraphWithLiveWorkloads can refine with live data.
func BuildAppGraph(app Resource) Graph {
	g := Graph{Root: app}
	root := GraphNode{
		ID:        app.Key(),
		Kind:      "Application",
		KindShort: "app",
		Name:      app.Name,
		Namespace: app.Namespace,
		Title:     "app",
		Subtitle:  app.Name,
		Status:    app.Sync,
		Health:    app.Health,
		Depth:     0,
		Parent:    -1,
	}
	g.Nodes = append(g.Nodes, root)

	managed := ParseManagedResources(app.Raw)
	if len(managed) == 0 {
		g.Nodes = append(g.Nodes, GraphNode{
			ID:       "empty",
			Title:    "?",
			Subtitle: "no status.resources yet",
			Depth:    1,
			Parent:   0,
			IsGroup:  true,
		})
		return g
	}

	sort.SliceStable(managed, func(i, j int) bool {
		if managed[i].Kind != managed[j].Kind {
			return managed[i].Kind < managed[j].Kind
		}
		return managed[i].Name < managed[j].Name
	})

	var (
		workloads []ManagedResource
		runtimes  []ManagedResource // ReplicaSet, Pod
		others    []ManagedResource
	)
	for _, mr := range managed {
		switch strings.ToLower(mr.Kind) {
		case "deployment", "statefulset", "daemonset", "rollout":
			workloads = append(workloads, mr)
		case "replicaset", "pod":
			runtimes = append(runtimes, mr)
		default:
			others = append(others, mr)
		}
	}

	for _, mr := range others {
		g.Nodes = append(g.Nodes, nodeFromManaged(mr, 1, 0))
	}

	workloadIdx := map[string]int{} // name -> node index
	for _, mr := range workloads {
		idx := len(g.Nodes)
		g.Nodes = append(g.Nodes, nodeFromManaged(mr, 1, 0))
		workloadIdx[mr.Name] = idx
	}

	// Parent ReplicaSets under the longest matching workload name prefix.
	rsIdx := map[string]int{}
	var rsList []ManagedResource
	var pods []ManagedResource
	for _, mr := range runtimes {
		if strings.EqualFold(mr.Kind, "ReplicaSet") {
			rsList = append(rsList, mr)
		} else {
			pods = append(pods, mr)
		}
	}
	sort.SliceStable(rsList, func(i, j int) bool {
		// Prefer names that look "active" later when pods attach; stable by name.
		return rsList[i].Name < rsList[j].Name
	})

	for _, rs := range rsList {
		parent := findWorkloadParent(rs.Name, workloadIdx)
		if parent < 0 {
			g.Nodes = append(g.Nodes, nodeFromManaged(rs, 1, 0))
			rsIdx[rs.Name] = len(g.Nodes) - 1
			continue
		}
		idx := len(g.Nodes)
		g.Nodes = append(g.Nodes, nodeFromManaged(rs, 2, parent))
		rsIdx[rs.Name] = idx
	}

	for _, pod := range pods {
		parent := findRSParent(pod.Name, rsIdx)
		if parent < 0 {
			// Orphan pod: try workload prefix, else under app.
			if wp := findWorkloadParent(pod.Name, workloadIdx); wp >= 0 {
				g.Nodes = append(g.Nodes, nodeFromManaged(pod, 2, wp))
			} else {
				g.Nodes = append(g.Nodes, nodeFromManaged(pod, 1, 0))
			}
			continue
		}
		g.Nodes = append(g.Nodes, nodeFromManaged(pod, 3, parent))
	}

	return g
}

func findWorkloadParent(childName string, workloadIdx map[string]int) int {
	best, bestLen := -1, -1
	for name, idx := range workloadIdx {
		prefix := name + "-"
		if strings.HasPrefix(childName, prefix) && len(name) > bestLen {
			best, bestLen = idx, len(name)
		}
		if childName == name && len(name) > bestLen {
			best, bestLen = idx, len(name)
		}
	}
	return best
}

func findRSParent(podName string, rsIdx map[string]int) int {
	best, bestLen := -1, -1
	for name, idx := range rsIdx {
		prefix := name + "-"
		if strings.HasPrefix(podName, prefix) && len(name) > bestLen {
			best, bestLen = idx, len(name)
		}
	}
	return best
}

// EnrichGraphWithLiveWorkloads attaches ReplicaSet → Pod under Deployments/STS/DS
// using the Application destination cluster (hub kube or Argo CD cluster secret).
func EnrichGraphWithLiveWorkloads(ctx context.Context, client *kube.Client, g *Graph) error {
	if client == nil || client.Typed == nil || g == nil {
		return nil
	}
	dest := appDestination(g.Root)
	destNS := dest.Namespace

	target, destErr := client.ClientForDestination(ctx, dest, g.Root.Namespace, "argocd")
	if target == nil {
		target = client
	}

	type work struct {
		idx  int
		kind string
		ns   string
		name string
	}
	var workloads []work
	var errs []string
	for i, n := range g.Nodes {
		if n.Depth != 1 {
			continue
		}
		switch strings.ToLower(n.Kind) {
		case "deployment", "statefulset", "daemonset", "rollout":
			ns := firstNonEmpty(n.Namespace, destNS)
			if ns == "" {
				errs = append(errs, fmt.Sprintf("%s/%s: empty namespace", n.Kind, n.Name))
				continue
			}
			workloads = append(workloads, work{idx: i, kind: n.Kind, ns: ns, name: n.Name})
		}
	}

	liveOK := false
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].idx > workloads[j].idx })
	for _, w := range workloads {
		children, err := liveChildren(ctx, target, w.kind, w.ns, w.name)
		if err != nil && destNS != "" && destNS != w.ns {
			children, err = liveChildren(ctx, target, w.kind, destNS, w.name)
		}
		// If destination client failed resolution, also try hub once.
		if err != nil && target != client {
			children, err = liveChildren(ctx, client, w.kind, w.ns, w.name)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s@%s: %v", w.kind, w.name, w.ns, err))
			continue
		}
		stripDescendants(g, w.idx)
		attachChildren(g, w.idx, children, 2)
		liveOK = true
	}

	if liveOK {
		return nil
	}
	if len(errs) > 0 && !hasWorkloadChildren(g) {
		msg := strings.Join(errs, "; ")
		if destErr != nil {
			msg = fmt.Sprintf("%s; destination client: %v", msg, destErr)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func stripDescendants(g *Graph, parentIdx int) {
	if g == nil || parentIdx < 0 {
		return
	}
	remove := map[int]bool{}
	var mark func(int)
	mark = func(p int) {
		for i, n := range g.Nodes {
			if n.Parent == p {
				remove[i] = true
				mark(i)
			}
		}
	}
	mark(parentIdx)
	if len(remove) == 0 {
		return
	}
	oldToNew := map[int]int{}
	out := make([]GraphNode, 0, len(g.Nodes)-len(remove))
	for i, n := range g.Nodes {
		if remove[i] {
			continue
		}
		oldToNew[i] = len(out)
		out = append(out, n)
	}
	for i := range out {
		if out[i].Parent >= 0 {
			if np, ok := oldToNew[out[i].Parent]; ok {
				out[i].Parent = np
			} else {
				out[i].Parent = 0
			}
		}
	}
	g.Nodes = out
}

func hasWorkloadChildren(g *Graph) bool {
	for _, n := range g.Nodes {
		if n.Depth >= 2 {
			return true
		}
	}
	return false
}

func appDestination(app Resource) kube.Destination {
	d := kube.Destination{}
	if app.Raw == nil {
		return d
	}
	d.Server, _, _ = unstructured.NestedString(app.Raw.Object, "spec", "destination", "server")
	d.Name, _, _ = unstructured.NestedString(app.Raw.Object, "spec", "destination", "name")
	d.Namespace, _, _ = unstructured.NestedString(app.Raw.Object, "spec", "destination", "namespace")
	return d
}

func appDestinationNamespace(app Resource) string {
	return appDestination(app).Namespace
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type liveNode struct {
	Kind, Name, Namespace, Health, Status, Meta string
	Children                                    []liveNode
}

func liveChildren(ctx context.Context, client *kube.Client, kind, ns, name string) ([]liveNode, error) {
	switch strings.ToLower(kind) {
	case "deployment":
		return deploymentTree(ctx, client, ns, name)
	case "statefulset":
		return statefulSetPods(ctx, client, ns, name)
	case "daemonset":
		return daemonSetPods(ctx, client, ns, name)
	case "rollout":
		// Argo Rollouts: treat like Deployment owner of ReplicaSets when possible.
		return deploymentTree(ctx, client, ns, name)
	default:
		return nil, nil
	}
}

func deploymentTree(ctx context.Context, client *kube.Client, ns, name string) ([]liveNode, error) {
	deploy, err := client.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	opts := metav1.ListOptions{}
	if sel := deploy.Spec.Selector; sel != nil {
		if s, err := metav1.LabelSelectorAsSelector(sel); err == nil {
			opts.LabelSelector = s.String()
		}
	}
	rsList, err := client.Typed.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return nil, err
	}

	var out []liveNode
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !ownedBy(rs.OwnerReferences, "Deployment", deploy.UID) {
			continue
		}
		// Keep ALL ReplicaSets (including scaled-to-zero), matching Argo CD UI.
		pods, _ := podsOwnedBy(ctx, client, ns, "ReplicaSet", rs.UID)
		want := derefInt32(rs.Spec.Replicas)
		node := liveNode{
			Kind:      "ReplicaSet",
			Name:      rs.Name,
			Namespace: ns,
			Health:    rsHealth(rs),
			Status:    "Synced",
			Meta:      fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, want),
			Children:  pods,
		}
		out = append(out, node)
	}
	// Fallback: match by name prefix if ownerRefs didn't match (rare RBAC / API quirks).
	if len(out) == 0 {
		for i := range rsList.Items {
			rs := &rsList.Items[i]
			if !strings.HasPrefix(rs.Name, name+"-") {
				continue
			}
			pods, _ := podsOwnedBy(ctx, client, ns, "ReplicaSet", rs.UID)
			out = append(out, liveNode{
				Kind: "ReplicaSet", Name: rs.Name, Namespace: ns,
				Health: rsHealth(rs), Status: "Synced",
				Meta:     fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, derefInt32(rs.Spec.Replicas)),
				Children: pods,
			})
		}
	}
	// Active ReplicaSets (with pods / replicas) first — so `l` lands on a useful child.
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := len(out[i].Children), len(out[j].Children)
		if pi != pj {
			return pi > pj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func statefulSetPods(ctx context.Context, client *kube.Client, ns, name string) ([]liveNode, error) {
	sts, err := client.Typed.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	pods, err := podsOwnedBy(ctx, client, ns, "StatefulSet", sts.UID)
	return pods, err
}

func daemonSetPods(ctx context.Context, client *kube.Client, ns, name string) ([]liveNode, error) {
	ds, err := client.Typed.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	pods, err := podsOwnedBy(ctx, client, ns, "DaemonSet", ds.UID)
	return pods, err
}

func podsOwnedBy(ctx context.Context, client *kube.Client, ns, kind string, uid types.UID) ([]liveNode, error) {
	list, err := client.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []liveNode
	for i := range list.Items {
		p := &list.Items[i]
		if !ownedBy(p.OwnerReferences, kind, uid) {
			continue
		}
		ready, total := podReadyCounts(p)
		out = append(out, liveNode{
			Kind:      "Pod",
			Name:      p.Name,
			Namespace: ns,
			Health:    podHealth(p),
			Status:    "Synced",
			Meta:      fmt.Sprintf("%s %d/%d", strings.ToLower(string(p.Status.Phase)), ready, total),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func ownedBy(refs []metav1.OwnerReference, kind string, uid types.UID) bool {
	for _, r := range refs {
		if r.Kind == kind && r.UID == uid {
			return true
		}
	}
	return false
}

func attachChildren(g *Graph, parentIdx int, children []liveNode, depth int) {
	for _, c := range children {
		idx := len(g.Nodes)
		group, version := kindAPI(c.Kind)
		g.Nodes = append(g.Nodes, GraphNode{
			ID:        fmt.Sprintf("%s/%s/%s/%s", group, c.Kind, c.Namespace, c.Name),
			Group:     group,
			Version:   version,
			Kind:      c.Kind,
			KindShort: KindShortName(c.Kind),
			Name:      c.Name,
			Namespace: c.Namespace,
			Title:     KindShortName(c.Kind),
			Subtitle:  c.Name,
			Status:    c.Status,
			Health:    c.Health,
			Depth:     depth,
			Parent:    parentIdx,
			Meta:      c.Meta,
		})
		if len(c.Children) > 0 {
			attachChildren(g, idx, c.Children, depth+1)
		}
	}
}

func kindAPI(kind string) (group, version string) {
	switch strings.ToLower(kind) {
	case "pod", "service", "configmap", "secret", "persistentvolumeclaim", "serviceaccount", "endpoints":
		return "", "v1"
	case "deployment", "replicaset", "statefulset", "daemonset":
		return "apps", "v1"
	case "job", "cronjob":
		return "batch", "v1"
	case "ingress":
		return "networking.k8s.io", "v1"
	case "rollout":
		return "argoproj.io", "v1alpha1"
	default:
		return "", "v1"
	}
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func rsHealth(rs *appsv1.ReplicaSet) string {
	want := derefInt32(rs.Spec.Replicas)
	if want == 0 && rs.Status.Replicas == 0 {
		return "Suspended" // scaled-down / old revision
	}
	if rs.Status.ReadyReplicas >= want && want > 0 {
		return "Healthy"
	}
	if rs.Status.ReadyReplicas > 0 {
		return "Progressing"
	}
	return "Progressing"
}

func podHealth(p *corev1.Pod) string {
	switch p.Status.Phase {
	case corev1.PodRunning:
		ready, total := podReadyCounts(p)
		if total > 0 && ready == total {
			return "Healthy"
		}
		return "Progressing"
	case corev1.PodSucceeded:
		return "Healthy"
	case corev1.PodFailed:
		return "Degraded"
	case corev1.PodPending:
		return "Progressing"
	default:
		return "Unknown"
	}
}

func podReadyCounts(p *corev1.Pod) (ready, total int) {
	for _, c := range p.Status.ContainerStatuses {
		total++
		if c.Ready {
			ready++
		}
	}
	return ready, total
}

// AgeString for live resources — kept for future badges.
func AgeSince(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
