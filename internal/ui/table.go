package ui

import (
	"fmt"
	"strings"

	"github.com/vvrnv/argo9s/internal/argo"
)

// column is one list column. NAME has w=0 and takes the leftover width.
// drop > 0 columns are removed on narrow terminals, highest drop first.
type column struct {
	title string
	w     int
	drop  int
	grow  bool // receives spare width once NAME is capped
	get   func(argo.Resource) string
}

// columnsFor returns the columns of the current view — each kind shows only
// what matters for it (k9s-style), instead of one wide generic table.
func (m Model) columnsFor() []column {
	name := column{title: "NAME", get: func(r argo.Resource) string { return r.Name }}
	sync := column{title: "SYNC", w: 10, get: func(r argo.Resource) string { return r.Sync }}
	health := column{title: "HEALTH", w: 11, get: func(r argo.Resource) string { return r.Health }}
	age := column{title: "AGE", w: 5, drop: 1, get: argo.Resource.AgeString}
	project := column{title: "PROJECT", w: 14, drop: 3, grow: true, get: func(r argo.Resource) string { return r.Project }}
	apps := column{title: "APPS", w: 5, get: func(r argo.Resource) string { return fmt.Sprint(m.childCount(r)) }}

	var cols []column
	switch m.filterKind {
	case argo.KindApplicationSet:
		sync.title = "STATUS"
		cols = []column{name, sync, health, apps, project, age}
	case argo.KindAppProject:
		cols = []column{name, apps,
			{title: "DESCRIPTION", w: 30, drop: 2, grow: true, get: func(r argo.Resource) string { return r.Message }}, age}
	case argo.KindCluster:
		cols = []column{name, sync, health, apps}
	case argo.KindContext:
		cols = []column{name,
			{title: "TYPE", w: 11, get: func(r argo.Resource) string { return r.Project }},
			{title: "TARGET", w: 30, drop: 2, grow: true, get: func(r argo.Resource) string { return r.Cluster }},
			{title: "ACTIVE", w: 6, get: func(r argo.Resource) string { return r.Sync }}}
	default: // applications, and "all"
		cols = []column{name, sync, health,
			{title: "CLUSTER", w: 18, drop: 4, grow: true, get: func(r argo.Resource) string { return orDash(r.Cluster) }},
			{title: "APPSET", w: 18, drop: 6, grow: true, get: func(r argo.Resource) string { return orDash(r.Owner) }},
			project,
			{title: "REV", w: 8, drop: 7, get: func(r argo.Resource) string { return r.Revision }},
			age}
		if m.filterKind == argo.KindAll {
			cols = append([]column{{title: "KIND", w: 7, get: func(r argo.Resource) string { return r.Kind.Short() }}}, cols...)
		}
	}
	if m.multiNamespace() {
		cols = append([]column{{title: "NAMESPACE", w: 12, drop: 5, get: func(r argo.Resource) string { return r.Namespace }}}, cols...)
	}
	return cols
}

// multiNamespace: only show NAMESPACE when the list actually spans several.
func (m Model) multiNamespace() bool {
	seen := ""
	for _, r := range m.resources {
		if r.Namespace == "-" {
			continue
		}
		if seen != "" && r.Namespace != seen {
			return true
		}
		seen = r.Namespace
	}
	return false
}

// childCount: Applications belonging to an appset / project / cluster.
func (m Model) childCount(parent argo.Resource) int {
	n := 0
	for _, a := range m.store.List(argo.KindApplication, m.namespace, m.allNS) {
		if belongsTo(a, parent) {
			n++
		}
	}
	return n
}

// layoutColumns fits cols into termW: drops columns by priority, sizes NAME,
// and hands spare width to growable columns.
func layoutColumns(cols []column, termW int) []column {
	if termW <= 0 {
		termW = 100
	}
	const indent, markers, nameMin, nameCap = 2, 6, 16, 36
	avail := termW - indent - markers
	fixed := func() int {
		n := len(cols) - 1 // single-space gaps
		for _, c := range cols {
			n += c.w
		}
		return n
	}
	for fixed()+nameMin > avail {
		worst := -1
		for i, c := range cols {
			if c.drop > 0 && (worst < 0 || c.drop > cols[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		cols = append(cols[:worst:worst], cols[worst+1:]...)
	}
	spare := max(nameMin, avail-fixed())
	nameW := min(spare, nameCap)
	spare -= nameW
	var growers []int
	for i, c := range cols {
		if c.title == "NAME" {
			cols[i].w = nameW
		} else if c.grow {
			growers = append(growers, i)
		}
	}
	for k, i := range growers {
		share := spare / (len(growers) - k)
		cols[i].w += share
		spare -= share
	}
	return cols
}

// formatRow renders r's values (or the headers when r is nil).
func formatRow(cols []column, r *argo.Resource) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		v := c.title
		if r != nil {
			v = c.get(*r)
		}
		parts[i] = padTruncate(v, c.w)
	}
	return "  " + strings.Join(parts, " ")
}
