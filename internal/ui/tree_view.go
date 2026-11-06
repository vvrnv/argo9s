package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/argo"
)

// Resource tree of an Application (enter on an app): managed objects as an
// expandable list — Deployment ▸ ReplicaSet ▸ Pod, CRD objects, Services, …
// It reuses m.graph / m.graphCursor (node index); m.expanded is keyed by node ID
// so it survives the async enrichment that appends live children.

// treeRows returns visible node indexes in depth-first order, root excluded.
func treeRows(nodes []argo.GraphNode, expanded map[string]bool) []int {
	children := map[int][]int{}
	for i, n := range nodes {
		if i > 0 {
			children[n.Parent] = append(children[n.Parent], i)
		}
	}
	var out []int
	var walk func(p int)
	walk = func(p int) {
		for _, c := range children[p] {
			out = append(out, c)
			if expanded[nodes[c].ID] {
				walk(c)
			}
		}
	}
	walk(0)
	return out
}

func hasChildren(nodes []argo.GraphNode, i int) bool {
	for j, n := range nodes {
		if j > 0 && n.Parent == i {
			return true
		}
	}
	return false
}

func indexOf(rows []int, v int) int {
	for i, r := range rows {
		if r == v {
			return i
		}
	}
	return -1
}

// clampTreeCursor keeps graphCursor on a visible row.
func (m *Model) clampTreeCursor() {
	rows := treeRows(m.graph.Nodes, m.expanded)
	if len(rows) > 0 && indexOf(rows, m.graphCursor) < 0 {
		m.graphCursor = rows[0]
	}
}

func (m Model) handleTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	nodes := m.graph.Nodes
	rows := treeRows(nodes, m.expanded)
	pos := indexOf(rows, m.graphCursor)
	move := func(p int) {
		if len(rows) > 0 {
			m.graphCursor = rows[min(max(0, p), len(rows)-1)]
		}
	}
	cur := m.graphCursor
	valid := cur > 0 && cur < len(nodes)
	switch msg.String() {
	case "j", "down":
		move(pos + 1)
	case "k", "up":
		move(pos - 1)
	case "home", "g":
		move(0)
	case "end", "G":
		move(len(rows) - 1)
	case "pgdown", "ctrl+f":
		move(pos + m.innerHeight() - 1)
	case "pgup", "ctrl+b":
		move(pos - m.innerHeight() + 1)
	case "enter", " ", "right", "l":
		if !valid {
			break
		}
		if msg.String() == "l" && strings.EqualFold(nodes[cur].Kind, "Pod") {
			mm, c := m.openPodLogs()
			return mm, c, true
		}
		if hasChildren(nodes, cur) {
			id := nodes[cur].ID
			if msg.String() == "right" || msg.String() == "l" {
				m.expanded[id] = true
			} else {
				m.expanded[id] = !m.expanded[id]
			}
		}
	case "left", "h":
		if !valid {
			break
		}
		if id := nodes[cur].ID; m.expanded[id] {
			m.expanded[id] = false
		} else if p := nodes[cur].Parent; p > 0 {
			m.graphCursor = p
		}
	case "d":
		if valid {
			m.showDetail(nodes[cur].Kind+"/"+nodes[cur].Name, nodeDescribe(nodes[cur], m.selected))
		}
	case "o":
		m.toggleOldRS()
	case "s":
		if valid && strings.EqualFold(nodes[cur].Kind, "Pod") {
			mm, c := m.podAction("shell")
			return mm, c, true
		}
	case "y", "e":
		if valid {
			mm, c := m.openGraphNodeYAML(msg.String() == "e")
			return mm, c, true
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m Model) renderTree(maxRows int) string {
	nodes := m.graph.Nodes
	rows := treeRows(nodes, m.expanded)
	if len(rows) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("  no managed resources yet")
	}
	w := max(40, m.width-2)
	const kindW, syncW, healthW, infoW = 22, 11, 12, 18 // kindW includes tree indent + ▸
	nameW := max(12, w-kindW-syncW-healthW-infoW-5)
	row := func(tree, kind, name, sync, health, info string) string {
		return padTruncate(tree+kind, kindW) + " " + padTruncate(name, nameW) + " " +
			padTruncate(sync, syncW) + " " + padTruncate(health, healthW) + " " + padTruncate(info, infoW)
	}
	lines := []string{lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true).Render(row("  ", "KIND", "NAME", "SYNC", "HEALTH", "INFO"))}

	maxRows = max(1, maxRows)
	pos := max(0, indexOf(rows, m.graphCursor))
	start := 0
	if pos >= maxRows {
		start = pos - maxRows + 1
	}
	for _, i := range rows[start:min(len(rows), start+maxRows)] {
		n := nodes[i]
		marker := "  "
		if hasChildren(nodes, i) {
			marker = "▸ "
			if m.expanded[n.ID] {
				marker = "▾ "
			}
		}
		indent := strings.Repeat("  ", max(0, n.Depth-1))
		kind := n.KindShort
		if kind == "" {
			kind = n.Title
		}
		name := n.Name
		if name == "" {
			name = n.Subtitle
		}
		info := n.Meta
		if n.IsGroup && n.Count > 0 {
			info = fmt.Sprintf("%d items", n.Count)
		}
		line := row(indent+marker, kind, name, orDash(n.Status), orDash(n.Health), info)
		style := lipgloss.NewStyle().Foreground(healthColor(n.Health, n.Status))
		if i == m.graphCursor {
			style = lipgloss.NewStyle().Background(lipgloss.Color("117")).Foreground(lipgloss.Color("16")).Bold(true)
			line = padTruncate(line, w)
		}
		lines = append(lines, style.Render(line))
	}
	return strings.Join(lines, "\n")
}
