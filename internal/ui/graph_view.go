package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/argo"
)

const (
	graphBoxW   = 28
	graphColGap = 4
)

// renderGraph draws an Argo CD UI–style left-to-right resource tree, centered.
func renderGraph(g argo.Graph, cursor, width, _ int) string {
	if width < 40 {
		width = 40
	}
	if len(g.Nodes) == 0 {
		return centerLine("no graph", width)
	}

	rows := layoutRows(g.Nodes)
	maxCol, maxRow := 0, 0
	for _, r := range rows {
		if r.col > maxCol {
			maxCol = r.col
		}
		if r.row > maxRow {
			maxRow = r.row
		}
	}

	body := composeHorizontalGraph(g, rows, cursor, maxCol, maxRow)
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render("Resource Graph")
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("h/l parent/child · j/k siblings · enter describe · esc back")
	header := centerLine(title, width) + "\n" + centerLine(hint, width)
	return header + "\n\n" + centerBlock(body, width)
}

type rowPos struct{ col, row int }

func layoutRows(nodes []argo.GraphNode) []rowPos {
	pos := make([]rowPos, len(nodes))
	children := make([][]int, len(nodes))
	for i, n := range nodes {
		pos[i].col = n.Depth
		if n.Parent >= 0 && n.Parent < len(nodes) {
			children[n.Parent] = append(children[n.Parent], i)
		}
	}

	leafCounter := 0
	var place func(i int) (minRow, maxRow int)
	place = func(i int) (int, int) {
		ch := children[i]
		if len(ch) == 0 {
			r := leafCounter
			leafCounter++
			pos[i].row = r
			return r, r
		}
		minR, maxR := -1, -1
		for _, c := range ch {
			a, b := place(c)
			if minR < 0 || a < minR {
				minR = a
			}
			if b > maxR {
				maxR = b
			}
		}
		pos[i].row = (minR + maxR) / 2
		return minR, maxR
	}
	if len(nodes) > 0 {
		place(0)
	}
	return pos
}

func composeHorizontalGraph(g argo.Graph, rows []rowPos, cursor, maxCol, maxRow int) string {
	matrix := make([][]int, maxRow+1)
	for r := range matrix {
		matrix[r] = make([]int, maxCol+1)
		for c := range matrix[r] {
			matrix[r][c] = -1
		}
	}
	for i, p := range rows {
		if p.row >= 0 && p.row <= maxRow && p.col >= 0 && p.col <= maxCol {
			matrix[p.row][p.col] = i
		}
	}

	var sections []string
	for r := 0; r <= maxRow; r++ {
		boxes := make([]string, maxCol+1)
		for c := 0; c <= maxCol; c++ {
			idx := matrix[r][c]
			if idx < 0 {
				boxes[c] = blankBox(graphBoxW, 5)
			} else {
				boxes[c] = renderArgoNodeBox(g.Nodes[idx], graphBoxW, idx == cursor)
			}
		}

		boxLines := make([][]string, maxCol+1)
		maxH := 5
		for c := 0; c <= maxCol; c++ {
			boxLines[c] = strings.Split(boxes[c], "\n")
			if len(boxLines[c]) > maxH {
				maxH = len(boxLines[c])
			}
		}
		for c := 0; c <= maxCol; c++ {
			for len(boxLines[c]) < maxH {
				boxLines[c] = append(boxLines[c], strings.Repeat(" ", graphBoxW))
			}
			for li := range boxLines[c] {
				w := lipgloss.Width(boxLines[c][li])
				if w < graphBoxW {
					boxLines[c][li] += strings.Repeat(" ", graphBoxW-w)
				}
			}
		}

		for li := 0; li < maxH; li++ {
			var line strings.Builder
			for c := 0; c <= maxCol; c++ {
				line.WriteString(boxLines[c][li])
				if c == maxCol {
					continue
				}
				gap := strings.Repeat(" ", graphColGap)
				if li == maxH/2 {
					left, right := matrix[r][c], matrix[r][c+1]
					conn := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
					if left >= 0 && right >= 0 && g.Nodes[right].Parent == left {
						gap = conn.Render(" ── ")
					} else if left >= 0 && hasChildInCol(g, rows, left, c+1) {
						gap = conn.Render(" ─┬ ")
					} else if right >= 0 && g.Nodes[right].Parent >= 0 && rows[g.Nodes[right].Parent].col == c {
						gap = conn.Render(" ─┴ ")
					} else if left < 0 && right >= 0 {
						gap = conn.Render(" ·· ")
					}
				}
				line.WriteString(gap)
			}
			sections = append(sections, line.String())
		}

		if r < maxRow {
			var vline strings.Builder
			for c := 0; c <= maxCol; c++ {
				stub := strings.Repeat(" ", graphBoxW)
				idx := matrix[r][c]
				below := -1
				if r+1 <= maxRow {
					below = matrix[r+1][c]
				}
				draw := false
				if idx >= 0 && hasChildOtherRow(g, rows, idx, r) {
					draw = true
				}
				if below >= 0 && g.Nodes[below].Parent >= 0 && rows[g.Nodes[below].Parent].col < c {
					draw = true
				}
				if draw {
					mid := graphBoxW / 2
					runes := []rune(strings.Repeat(" ", graphBoxW))
					runes[mid] = '│'
					stub = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(string(runes))
				}
				vline.WriteString(stub)
				if c < maxCol {
					vline.WriteString(strings.Repeat(" ", graphColGap))
				}
			}
			sections = append(sections, vline.String())
		}
	}
	return strings.Join(sections, "\n")
}

func hasChildInCol(g argo.Graph, rows []rowPos, parentIdx, col int) bool {
	for i, n := range g.Nodes {
		if n.Parent == parentIdx && rows[i].col == col {
			return true
		}
	}
	return false
}

func hasChildOtherRow(g argo.Graph, rows []rowPos, parentIdx, row int) bool {
	for i, n := range g.Nodes {
		if n.Parent == parentIdx && rows[i].row != row {
			return true
		}
	}
	return false
}

func blankBox(w, h int) string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", w)
	}
	return strings.Join(lines, "\n")
}

func renderArgoNodeBox(n argo.GraphNode, width int, selected bool) string {
	border := lipgloss.Color("240")
	if selected {
		border = lipgloss.Color("39")
	}

	inner := max(8, width-4)
	kind := n.Title
	if kind == "" {
		kind = n.KindShort
	}
	kind = truncate(kind, 12)
	name := n.Subtitle
	if name == "" {
		name = n.Name
	}
	name = truncate(name, inner)

	status := fmt.Sprintf("%s %s", statusGlyph(n.Health, n.Status), shortStatus(n))
	if n.Meta != "" {
		status = truncate(fmt.Sprintf("%s  %s", status, n.Meta), inner)
	} else {
		status = truncate(status, inner)
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Width(width - 2).
		Padding(0, 1)

	content := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(kind) + "\n" +
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true).Render(name) + "\n" +
		lipgloss.NewStyle().Foreground(healthColor(n.Health, n.Status)).Render(status)

	return boxStyle.Render(content)
}

func shortStatus(n argo.GraphNode) string {
	h, s := n.Health, n.Status
	if h == "" {
		h = "-"
	}
	if s == "" {
		s = "-"
	}
	return h + " " + s
}

func centerBlock(content string, width int) string {
	lines := strings.Split(content, "\n")
	maxW := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > maxW {
			maxW = w
		}
	}
	pad := max(0, (width-maxW)/2)
	prefix := strings.Repeat(" ", pad)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(ansiStripRough(line)) == "" && lipgloss.Width(line) == 0 {
			out = append(out, "")
			continue
		}
		out = append(out, prefix+line)
	}
	return strings.Join(out, "\n")
}

func ansiStripRough(s string) string {
	return s // lipgloss Width handles ansi; TrimSpace on styled blanks is ok
}

func centerLine(s string, width int) string {
	pad := max(0, (width-lipgloss.Width(s))/2)
	return strings.Repeat(" ", pad) + s
}

func statusGlyph(health, sync string) string {
	switch strings.ToLower(strings.ReplaceAll(sync, " ", "")) {
	case "outofsync":
		return "!"
	}
	switch strings.ToLower(health) {
	case "healthy":
		return "❤"
	case "degraded", "missing":
		return "✖"
	case "progressing":
		return "…"
	case "suspended":
		return "⏸"
	}
	switch strings.ToLower(strings.ReplaceAll(sync, " ", "")) {
	case "synced":
		return "✔"
	}
	return "·"
}
