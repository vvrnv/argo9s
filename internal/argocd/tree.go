package argocd

import (
	"context"
	"fmt"
	"strings"

	"github.com/vvrnv/argo9s/internal/argo"
)

// BuildGraphFromResourceTree builds an Argo UI–style graph from the API resource-tree.
func BuildGraphFromResourceTree(app argo.Resource, tree *ResourceTree) argo.Graph {
	g := argo.Graph{Root: app}
	root := argo.GraphNode{
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
	if tree == nil || len(tree.Nodes) == 0 {
		g.Nodes = append(g.Nodes, argo.GraphNode{
			ID: "empty", Title: "?", Subtitle: "empty resource tree", Depth: 1, Parent: 0, IsGroup: true,
		})
		return g
	}

	// Index by UID
	byUID := map[string]int{} // uid -> index in tree.Nodes
	for i, n := range tree.Nodes {
		if n.UID != "" {
			byUID[n.UID] = i
		}
	}

	// Find roots (no parentRefs or parent not in tree) — attach under Application.
	type pending struct {
		treeIdx int
		depth   int
		parent  int // index in g.Nodes
	}
	var queue []pending
	placed := map[string]int{} // uid -> g.Nodes index
	visited := map[int]bool{}

	isOrphan := func(n TreeNode) bool {
		if len(n.ParentRefs) == 0 {
			return true
		}
		for _, p := range n.ParentRefs {
			if _, ok := byUID[p.UID]; ok {
				return false
			}
		}
		return true
	}

	for i, n := range tree.Nodes {
		if isOrphan(n) {
			queue = append(queue, pending{treeIdx: i, depth: 1, parent: 0})
		}
	}

	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if visited[p.treeIdx] {
			continue
		}
		visited[p.treeIdx] = true
		n := tree.Nodes[p.treeIdx]
		if n.UID != "" {
			if _, ok := placed[n.UID]; ok {
				continue
			}
		}
		health := ""
		if n.Health != nil {
			health = n.Health.Status
		}
		meta := ""
		for _, info := range n.Info {
			if meta != "" {
				meta += " "
			}
			meta += info.Value
		}
		idx := len(g.Nodes)
		g.Nodes = append(g.Nodes, argo.GraphNode{
			ID:        fmt.Sprintf("%s/%s/%s/%s", n.Group, n.Kind, n.Namespace, n.Name),
			Group:     n.Group,
			Version:   n.Version,
			Kind:      n.Kind,
			KindShort: argo.KindShortName(n.Kind),
			Name:      n.Name,
			Namespace: n.Namespace,
			Title:     argo.KindShortName(n.Kind),
			Subtitle:  n.Name,
			Status:    n.ResourceStatus,
			Health:    health,
			Depth:     p.depth,
			Parent:    p.parent,
			Meta:      meta,
		})
		if n.UID != "" {
			placed[n.UID] = idx
		}
		for j, child := range tree.Nodes {
			if visited[j] {
				continue
			}
			for _, pref := range child.ParentRefs {
				match := false
				if pref.UID != "" && pref.UID == n.UID {
					match = true
				}
				if !match && pref.UID == "" && strings.EqualFold(pref.Kind, n.Kind) && pref.Name == n.Name && pref.Namespace == n.Namespace {
					match = true
				}
				if match {
					queue = append(queue, pending{treeIdx: j, depth: p.depth + 1, parent: idx})
					break
				}
			}
		}
	}

	// Any unplaced nodes as depth-1 under app
	for i, n := range tree.Nodes {
		if n.UID != "" {
			if _, ok := placed[n.UID]; ok {
				continue
			}
		} else {
			// name-based skip if already present
			found := false
			for _, gn := range g.Nodes {
				if gn.Kind == n.Kind && gn.Name == n.Name && gn.Namespace == n.Namespace {
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		_ = i
		health := ""
		if n.Health != nil {
			health = n.Health.Status
		}
		g.Nodes = append(g.Nodes, argo.GraphNode{
			ID:        fmt.Sprintf("%s/%s/%s/%s", n.Group, n.Kind, n.Namespace, n.Name),
			Group:     n.Group,
			Version:   n.Version,
			Kind:      n.Kind,
			KindShort: argo.KindShortName(n.Kind),
			Name:      n.Name,
			Namespace: n.Namespace,
			Title:     argo.KindShortName(n.Kind),
			Subtitle:  n.Name,
			Status:    n.ResourceStatus,
			Health:    health,
			Depth:     1,
			Parent:    0,
		})
	}
	return g
}

// EnrichGraph loads resource-tree for the application and replaces graph nodes.
func EnrichGraph(ctx context.Context, client *Client, g *argo.Graph) error {
	if client == nil || g == nil {
		return nil
	}
	tree, err := client.GetResourceTree(ctx, g.Root.Name, g.Root.Namespace)
	if err != nil {
		return err
	}
	*g = BuildGraphFromResourceTree(g.Root, tree)
	return nil
}
