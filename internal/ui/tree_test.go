package ui

import (
	"reflect"
	"testing"

	"github.com/vvrnv/argo9s/internal/argo"
)

func TestTreeRowsExpandCollapse(t *testing.T) {
	// app ─┬ svc
	//      └ deploy ─ rs ─ pod   (pod appended last, like async enrichment)
	nodes := []argo.GraphNode{
		{ID: "app", Parent: -1},
		{ID: "svc", Parent: 0, Depth: 1},
		{ID: "deploy", Parent: 0, Depth: 1},
		{ID: "rs", Parent: 2, Depth: 2},
		{ID: "pod", Parent: 3, Depth: 3},
	}
	exp := map[string]bool{}
	if got := treeRows(nodes, exp); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("collapsed: %v", got)
	}
	exp["deploy"] = true
	if got := treeRows(nodes, exp); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("deploy open: %v", got)
	}
	exp["rs"] = true
	if got := treeRows(nodes, exp); !reflect.DeepEqual(got, []int{1, 2, 3, 4}) {
		t.Fatalf("all open: %v", got)
	}
	exp["deploy"] = false // collapsing a parent hides grandchildren too
	if got := treeRows(nodes, exp); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("deploy closed: %v", got)
	}
}

func TestGraphLayoutAndStep(t *testing.T) {
	// app → cm, svc, deploy → rs → pod  (node order ≠ drawing order)
	g := argo.Graph{Nodes: []argo.GraphNode{
		{ID: "app", Parent: -1},
		{ID: "cm", Parent: 0, Depth: 1},
		{ID: "svc", Parent: 0, Depth: 1},
		{ID: "deploy", Parent: 0, Depth: 1},
		{ID: "rs", Parent: 3, Depth: 2},
		{ID: "pod", Parent: 4, Depth: 3, Kind: "Pod"},
	}}
	gl := computeGraphLayout(g)
	// layers read left → right
	if !(gl.boxes[0].x < gl.boxes[3].x && gl.boxes[3].x < gl.boxes[4].x && gl.boxes[4].x < gl.boxes[5].x) {
		t.Fatalf("not left-to-right: %+v", gl.boxes)
	}
	// boxes never overlap
	for i := range gl.boxes {
		for j := i + 1; j < len(gl.boxes); j++ {
			a, b := gl.boxes[i], gl.boxes[j]
			if a.x < b.x+gBoxW && b.x < a.x+gBoxW && a.y < b.y+gBoxH && b.y < a.y+gBoxH {
				t.Fatalf("boxes %d and %d overlap: %+v %+v", i, j, a, b)
			}
		}
	}
	// every non-root node has a routed edge
	for i := 1; i < len(g.Nodes); i++ {
		if len(gl.edges[i]) < 2 {
			t.Fatalf("node %d has no edge", i)
		}
	}
	// "child" follows the drawing: the nearest child row, then down the column
	c := graphStep(g, gl, 0, "child")
	if gl.boxes[c].y != gl.boxes[0].y && abs(gl.boxes[c].y-gl.boxes[0].y) > gBoxH {
		t.Fatalf("child %d is not the nearest one", c)
	}
	seen := map[int]bool{c: true}
	for k := 0; k < 3; k++ {
		c = graphStep(g, gl, c, "down")
		seen[c] = true
	}
	if !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("down should walk all app children, got %v", seen)
	}
}
