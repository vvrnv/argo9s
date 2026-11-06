package ui

import (
	"fmt"
	"testing"

	"github.com/vvrnv/argo9s/internal/argo"
)

// bigGraph: app with plain objects + deployments → several rs → pods,
// like a real chart (cert-manager has ~57 nodes).
func bigGraph() argo.Graph {
	nodes := []argo.GraphNode{{ID: "app", Parent: -1, Kind: "Application"}}
	add := func(id, kind string, parent, depth int) int {
		nodes = append(nodes, argo.GraphNode{ID: id, Kind: kind, Name: id, Parent: parent, Depth: depth})
		return len(nodes) - 1
	}
	for i := 0; i < 12; i++ {
		add(fmt.Sprintf("cm-%d", i), "ConfigMap", 0, 1)
	}
	for d := 0; d < 3; d++ {
		dep := add(fmt.Sprintf("deploy-%d", d), "Deployment", 0, 1)
		for r := 0; r < 4; r++ {
			rs := add(fmt.Sprintf("rs-%d-%d", d, r), "ReplicaSet", dep, 2)
			if r == 0 {
				add(fmt.Sprintf("pod-%d", d), "Pod", rs, 3)
			}
		}
	}
	add("issuer", "ClusterIssuer", 0, 1)
	return argo.Graph{Nodes: nodes}
}

func TestGraphEveryNodeReachable(t *testing.T) {
	g := bigGraph()
	gl := computeGraphLayout(g)
	seen := map[int]bool{0: true}
	queue := []int{0}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		next := []int{graphStep(g, gl, cur, "child"), graphStep(g, gl, cur, "down"), graphStep(g, gl, cur, "up")}
		if p := g.Nodes[cur].Parent; p >= 0 {
			next = append(next, p)
		}
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	for i, n := range g.Nodes {
		if !seen[i] {
			t.Errorf("node %d (%s) at %+v unreachable", i, n.ID, gl.boxes[i])
		}
	}
	// same layer ⇒ same column
	cols := map[int]map[int]bool{}
	for i, n := range g.Nodes {
		if cols[n.Depth] == nil {
			cols[n.Depth] = map[int]bool{}
		}
		cols[n.Depth][gl.boxes[i].x] = true
	}
	for d, xs := range cols {
		if len(xs) != 1 {
			t.Errorf("depth %d spread over columns %v", d, xs)
		}
	}
}

func TestGraphSearchAndJumps(t *testing.T) {
	g := bigGraph()
	gl := computeGraphLayout(g)
	ms := graphMatches(g, gl, "issuer")
	if len(ms) != 1 || g.Nodes[ms[0]].ID != "issuer" {
		t.Fatalf("search issuer: %v", ms)
	}
	if ms := graphMatches(g, gl, "replicaset"); len(ms) != 12 {
		t.Fatalf("search by kind: %d matches, want 12", len(ms))
	}
	pods := graphMatches(g, gl, "^pod")
	if len(pods) != 3 || cycleMatch(pods, pods[2], true) != pods[0] || cycleMatch(pods, pods[0], false) != pods[2] {
		t.Fatalf("n/N must wrap: %v", pods)
	}
	// end / home stay in the column and reach its extremes
	first := graphStep(g, gl, 0, "child")
	last := graphColumnJump(g, gl, first, "end", 20)
	top := graphColumnJump(g, gl, last, "home", 20)
	for i, b := range gl.boxes {
		if b.x == gl.boxes[first].x && (b.y > gl.boxes[last].y || b.y < gl.boxes[top].y) {
			t.Fatalf("node %d is beyond end/home", i)
		}
	}
	if pg := graphColumnJump(g, gl, top, "pgdown", 20); gl.boxes[pg].y <= gl.boxes[top].y {
		t.Fatalf("pgdown did not move down")
	}
}

func TestPruneOldReplicaSets(t *testing.T) {
	g := argo.Graph{Nodes: []argo.GraphNode{
		{ID: "app", Parent: -1},
		{ID: "deploy", Kind: "Deployment", Parent: 0},
		{ID: "rs-live", Kind: "ReplicaSet", Parent: 1, Meta: "1/1"},
		{ID: "pod", Kind: "Pod", Parent: 2},
		{ID: "rs-old1", Kind: "ReplicaSet", Parent: 1, Meta: "0/0"},
		{ID: "rs-old2", Kind: "ReplicaSet", Parent: 1, Health: "Suspended"},
		{ID: "svc", Kind: "Service", Parent: 0},
	}}
	out, hidden := pruneOldReplicaSets(g)
	if hidden != 2 || len(out.Nodes) != 5 {
		t.Fatalf("hidden=%d nodes=%d", hidden, len(out.Nodes))
	}
	byID := map[string]int{}
	for i, n := range out.Nodes {
		byID[n.ID] = i
	}
	if _, ok := byID["rs-old1"]; ok {
		t.Fatal("old rs kept")
	}
	// parents remapped to the new indexes
	if out.Nodes[byID["pod"]].Parent != byID["rs-live"] || out.Nodes[byID["svc"]].Parent != 0 {
		t.Fatalf("bad remap: %+v", out.Nodes)
	}
}
