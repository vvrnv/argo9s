package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/argo"
)

func appCols() []column {
	return Model{filterKind: argo.KindApplication, store: argo.NewStore()}.columnsFor()
}

func titles(cols []column) map[string]bool {
	out := map[string]bool{}
	for _, c := range cols {
		out[c.title] = true
	}
	return out
}

func TestLayoutColumnsFitsWidth(t *testing.T) {
	r := argo.Resource{Name: "guestbook", Project: "default", Sync: "OutOfSync", Health: "Healthy", Cluster: "in-cluster", Owner: "my-appset", Revision: "abcdef1"}
	for _, w := range []int{60, 80, 100, 120, 160, 200} {
		row := formatRow(layoutColumns(appCols(), w), &r) + " ‖ !"
		if got := lipgloss.Width(row); got > w {
			t.Fatalf("width %d: row is %d wide\n%s", w, got, row)
		}
	}
}

func TestLayoutColumnsDropsOnNarrow(t *testing.T) {
	narrow := titles(layoutColumns(appCols(), 70))
	if narrow["REV"] || narrow["APPSET"] || !narrow["NAME"] || !narrow["SYNC"] || !narrow["HEALTH"] {
		t.Fatalf("narrow columns: %v", narrow)
	}
	wide := titles(layoutColumns(appCols(), 180))
	for _, c := range []string{"NAME", "SYNC", "HEALTH", "CLUSTER", "APPSET", "PROJECT", "REV", "AGE"} {
		if !wide[c] {
			t.Fatalf("wide terminal missing %s: %v", c, wide)
		}
	}
	if wide["KIND"] || wide["NAMESPACE"] {
		t.Fatalf("apps view must not show KIND/NAMESPACE: %v", wide)
	}
}

func TestColumnsPerView(t *testing.T) {
	m := Model{store: argo.NewStore()}
	m.filterKind = argo.KindAppProject
	if got := titles(m.columnsFor()); got["SYNC"] || got["CLUSTER"] || !got["APPS"] {
		t.Fatalf("projects columns: %v", got)
	}
	m.filterKind = argo.KindAll
	if got := titles(m.columnsFor()); !got["KIND"] {
		t.Fatalf("all view needs KIND: %v", got)
	}
	m.resources = []argo.Resource{{Namespace: "argocd"}, {Namespace: "team-a"}}
	m.filterKind = argo.KindApplication
	if got := titles(m.columnsFor()); !got["NAMESPACE"] {
		t.Fatalf("multi-namespace list needs NAMESPACE: %v", got)
	}
}

func TestRowColorFollowsSyncHealthNotConditions(t *testing.T) {
	r := argo.Resource{Sync: "OutOfSync", Health: "Healthy", Conditions: []argo.Condition{{Type: "SyncError"}}}
	if got := rowColor(r); got != lipgloss.Color("214") {
		t.Fatalf("OutOfSync+Healthy with SyncError: got %v, want orange 214", got)
	}
	if got := conditionColor(r); got != lipgloss.Color("196") {
		t.Fatalf("error condition marker: got %v, want red", got)
	}
	if got := rowColor(argo.Resource{Sync: "Synced", Health: "Degraded"}); got != lipgloss.Color("196") {
		t.Fatalf("Degraded: got %v, want red", got)
	}
}
