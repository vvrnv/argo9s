package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/nulab/autog"
	"github.com/nulab/autog/graph"

	"github.com/vvrnv/argo9s/internal/argo"
)

// Fullscreen resource graph (g on an Application). autog computes a layered
// layout with orthogonal edges; we lay it out top-down and transpose it so the
// tree reads left → right like the Argo CD UI, then paint it on a cell canvas.

const (
	gBoxW, gBoxH = 26, 4
	gLayerGap    = 8 // columns between layers
	gNodeGap     = 1 // rows between sibling boxes
)

type gpt struct{ x, y int }

type graphLayout struct {
	w, h  int
	boxes []gpt   // top-left corner per node index
	edges [][]gpt // polyline parent → node, per node index (nil for root)
}

func computeGraphLayout(g argo.Graph) graphLayout {
	n := len(g.Nodes)
	gl := graphLayout{boxes: make([]gpt, n), edges: make([][]gpt, n)}
	if n == 0 {
		return gl
	}
	id := func(i int) string { return fmt.Sprint(i) }
	var adj [][]string
	for i := 1; i < n; i++ {
		if p := g.Nodes[i].Parent; p >= 0 && p < n {
			adj = append(adj, []string{id(p), id(i)})
		}
	}
	if len(adj) == 0 { // lone app node
		gl.boxes[0] = gpt{1, 0}
		gl.w, gl.h = gBoxW+2, gBoxH
		return gl
	}
	// autog stacks layers vertically: give it transposed sizes (W = box height).
	l := autog.Layout(graph.EdgeSlice(adj),
		autog.WithNodeFixedSize(gBoxH, gBoxW),
		autog.WithLayerSpacing(gLayerGap),
		autog.WithNodeSpacing(gNodeGap),
		autog.WithEdgeRouting(autog.EdgeRoutingOrtho))

	r := func(f float64) int { return int(math.Round(f)) }
	minX, minY := math.MaxInt, math.MaxInt
	for _, nd := range l.Nodes {
		minX, minY = min(minX, r(nd.Y)), min(minY, r(nd.X))
	}
	idx := func(s string) int {
		var i int
		fmt.Sscan(s, &i)
		return i
	}
	for _, nd := range l.Nodes {
		i := idx(nd.ID)
		gl.boxes[i] = gpt{r(nd.Y) - minX + 1, r(nd.X) - minY}
	}
	for _, e := range l.Edges {
		pts := make([]gpt, len(e.Points))
		for k, p := range e.Points {
			pts[k] = gpt{r(p[1]) - minX + 1, r(p[0]) - minY} // transpose
		}
		gl.edges[idx(e.ToID)] = pts
	}
	for _, b := range gl.boxes {
		gl.w, gl.h = max(gl.w, b.x+gBoxW+1), max(gl.h, b.y+gBoxH)
	}
	return gl
}

// --- canvas ---------------------------------------------------------------

type gcell struct {
	r    rune
	fg   lipgloss.Color
	bold bool
}

const (
	mU = 1 << iota
	mD
	mL
	mR
)

var lightJoin = map[int]rune{
	mL: '─', mR: '─', mL | mR: '─', mU: '│', mD: '│', mU | mD: '│',
	mD | mR: '┌', mD | mL: '┐', mU | mR: '└', mU | mL: '┘',
	mU | mD | mR: '├', mU | mD | mL: '┤', mD | mL | mR: '┬', mU | mL | mR: '┴', mU | mD | mL | mR: '┼',
}

var heavyJoin = map[int]rune{
	mL: '━', mR: '━', mL | mR: '━', mU: '┃', mD: '┃', mU | mD: '┃',
	mD | mR: '┏', mD | mL: '┓', mU | mR: '┗', mU | mL: '┛',
	mU | mD | mR: '┣', mU | mD | mL: '┫', mD | mL | mR: '┳', mU | mL | mR: '┻', mU | mD | mL | mR: '╋',
}

var (
	colEdge     = lipgloss.Color("240")
	colEdgeHot  = lipgloss.Color("214")
	colBox      = lipgloss.Color("238")
	colBoxSel   = lipgloss.Color("33")
	colBoxPath  = lipgloss.Color("245")
	colKindText = lipgloss.Color("245")
)

// paintGraph draws the whole graph; the caller crops it to the screen.
func paintGraph(g argo.Graph, gl graphLayout, cursor int, matched map[int]bool) [][]gcell {
	cv := make([][]gcell, gl.h)
	for y := range cv {
		cv[y] = make([]gcell, gl.w)
		for x := range cv[y] {
			cv[y][x] = gcell{r: ' '}
		}
	}
	put := func(x, y int, c gcell) {
		if y >= 0 && y < gl.h && x >= 0 && x < gl.w {
			cv[y][x] = c
		}
	}

	// selected node + ancestors: their edges and borders are highlighted
	onPath := map[int]bool{}
	for i := cursor; i >= 0 && i < len(g.Nodes); i = g.Nodes[i].Parent {
		onPath[i] = true
		if g.Nodes[i].Parent == i {
			break
		}
	}

	light := make(map[gpt]int)
	heavy := make(map[gpt]int)
	for i, pts := range gl.edges {
		masks := light
		if onPath[i] {
			masks = heavy
		}
		for k := 0; k+1 < len(pts); k++ {
			a, b := pts[k], pts[k+1]
			if a.y == b.y {
				x0, x1 := min(a.x, b.x), max(a.x, b.x)
				for x := x0; x <= x1; x++ {
					if x > x0 {
						masks[gpt{x, a.y}] |= mL
					}
					if x < x1 {
						masks[gpt{x, a.y}] |= mR
					}
				}
			} else {
				y0, y1 := min(a.y, b.y), max(a.y, b.y)
				for y := y0; y <= y1; y++ {
					if y > y0 {
						masks[gpt{a.x, y}] |= mU
					}
					if y < y1 {
						masks[gpt{a.x, y}] |= mD
					}
				}
			}
		}
	}
	for p, m := range light {
		if m != 0 && heavy[p] == 0 {
			put(p.x, p.y, gcell{r: lightJoin[m], fg: colEdge})
		}
	}
	for p, m := range heavy {
		if m != 0 {
			put(p.x, p.y, gcell{r: heavyJoin[m|light[p]], fg: colEdgeHot, bold: true})
		}
	}

	for i, n := range g.Nodes {
		paintBox(put, gl.boxes[i], n, i == cursor, onPath[i], matched[i])
	}
	return cv
}

func paintBox(put func(x, y int, c gcell), at gpt, n argo.GraphNode, selected, path, matched bool) {
	border := colBox
	switch {
	case selected:
		border = colBoxSel
	case matched:
		border = colAqua
	case path:
		border = colBoxPath
	}
	b := gcell{fg: border, bold: selected}
	text := func(x, y int, s string, fg lipgloss.Color, bold bool) {
		for i, r := range []rune(s) {
			put(x+i, y, gcell{r: r, fg: fg, bold: bold})
		}
	}
	inner := gBoxW - 2
	// top border carries the kind, like k9s frame titles
	top := "─ " + kindLabel(n) + " "
	top += strings.Repeat("─", max(0, inner-len([]rune(top))))
	text(at.x, at.y, "╭", border, selected)
	text(at.x+1, at.y, top, border, selected)
	text(at.x+gBoxW-1, at.y, "╮", border, selected)
	text(at.x+3, at.y, kindLabel(n), colKindText, false)
	for row := 1; row < gBoxH-1; row++ {
		b.r = '│'
		put(at.x, at.y+row, b)
		put(at.x+gBoxW-1, at.y+row, b)
		text(at.x+1, at.y+row, strings.Repeat(" ", inner), border, false)
	}
	text(at.x, at.y+gBoxH-1, "╰"+strings.Repeat("─", inner)+"╯", border, selected)

	name := n.Name
	if name == "" {
		name = n.Subtitle
	}
	hc := healthColor(n.Health, n.Status)
	text(at.x+2, at.y+1, headTruncate(name, inner-2), lipgloss.Color("255"), selected)
	text(at.x+2, at.y+2, padTruncate(nodeStatusLine(n), inner-2), hc, false)
}

func kindLabel(n argo.GraphNode) string {
	if n.KindShort != "" {
		return n.KindShort
	}
	if n.Title != "" {
		return n.Title
	}
	return strings.ToLower(n.Kind)
}

// headTruncate keeps the end of a name: for pods/replicasets the hash suffix is
// what tells siblings apart (k9s does the same).
func headTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[len(r)-n:])
	}
	return "…" + string(r[len(r)-n+1:])
}

func nodeStatusLine(n argo.GraphNode) string {
	glyph := map[string]string{"healthy": "♥", "degraded": "✖", "progressing": "◐", "suspended": "‖", "missing": "?"}[strings.ToLower(n.Health)]
	if glyph == "" {
		glyph = "·"
	}
	s := glyph + " " + orDash(n.Health)
	if strings.EqualFold(strings.ReplaceAll(n.Status, " ", ""), "outofsync") {
		s += "  ≠ OutOfSync"
	} else if n.Meta != "" {
		s += "  " + n.Meta
	} else if n.Status != "" {
		s += "  ✓ " + n.Status
	}
	return s
}

// --- screen ---------------------------------------------------------------

// graphViewport is the canvas area: full width, minus breadcrumb (1) and
// footer (separator + details + keys = 3).
func (m Model) graphViewport() (w, h int) {
	return max(20, m.width), max(4, m.height-4)
}

// followCursor scrolls the canvas so the selected box stays fully visible.
func (m *Model) followCursor() {
	if m.graphCursor < 0 || m.graphCursor >= len(m.gl.boxes) {
		return
	}
	b := m.gl.boxes[m.graphCursor]
	vw, vh := m.graphViewport()
	const pad = 4
	if b.x < m.gox+pad {
		m.gox = b.x - pad
	}
	if b.x+gBoxW > m.gox+vw-pad {
		m.gox = b.x + gBoxW - vw + pad
	}
	if b.y < m.goy+1 {
		m.goy = b.y - 1
	}
	if b.y+gBoxH > m.goy+vh-1 {
		m.goy = b.y + gBoxH - vh + 1
	}
	m.gox = max(0, min(m.gox, max(0, m.gl.w-vw)))
	m.goy = max(0, min(m.goy, max(0, m.gl.h-vh)))
}

func (m Model) renderGraphScreen() string {
	w, vh := m.graphViewport()
	// breadcrumb: where am I, and the app's overall state
	app := m.selected
	crumb := labelStyle.Render(" argo9s") + keyDescStyle.Render(" ▸ ") + valueStyle.Render(m.context) +
		keyDescStyle.Render(" ▸ ") + lipgloss.NewStyle().Foreground(colAqua).Bold(true).Render(app.Project+" / "+app.Name)
	state := lipgloss.NewStyle().Foreground(healthColor(app.Health, app.Sync)).Render(app.Health + "  " + app.Sync)
	ro := lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true).Render("[RW]")
	if m.ops != nil && m.ops.ReadOnly() {
		ro = lipgloss.NewStyle().Foreground(colOrange).Bold(true).Render("[RO]")
	}
	right := state + "  " + ro + " "
	if m.hiddenRS > 0 {
		right = keyDescStyle.Render(fmt.Sprintf("+%d old rs (o)  ", m.hiddenRS)) + right
	}
	top := crumb + strings.Repeat(" ", max(1, w-lipgloss.Width(crumb)-lipgloss.Width(right))) + right

	ms := graphMatches(m.graph, m.gl, m.graphQuery)
	matched := map[int]bool{}
	for _, i := range ms {
		matched[i] = true
	}
	if m.graphQuery != "" {
		pos := 0
		for k, i := range ms {
			if i == m.graphCursor {
				pos = k + 1
			}
		}
		q := lipgloss.NewStyle().Foreground(colAqua).Render("</") + lipgloss.NewStyle().Foreground(colFuchsia).Render(m.graphQuery) +
			lipgloss.NewStyle().Foreground(colAqua).Render(fmt.Sprintf("> %d/%d  ", pos, len(ms)))
		top = crumb + strings.Repeat(" ", max(1, w-lipgloss.Width(crumb)-lipgloss.Width(q)-lipgloss.Width(right))) + q + right
	}
	cv := paintGraph(m.graph, m.gl, m.graphCursor, matched)
	lines := make([]string, 0, vh)
	for y := m.goy; y < m.goy+vh; y++ {
		if y >= len(cv) {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, renderCells(cv[y], m.gox, w))
	}

	sep := lipgloss.NewStyle().Foreground(colBox).Render(strings.Repeat("─", w))
	details := ""
	switch {
	case m.filtering:
		details = " " + lipgloss.NewStyle().Foreground(colAqua).Bold(true).Render("/") + m.filterIn.View()
	case m.loading != "":
		details = progressBar(m.spin, 24) + " " + lipgloss.NewStyle().Foreground(colAqua).Render(m.loading+"…")
	case m.statusErr:
		details = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(truncate(m.status, w))
	case m.graphCursor >= 0 && m.graphCursor < len(m.graph.Nodes):
		n := m.graph.Nodes[m.graphCursor]
		details = " " + labelStyle.Render(kindLabel(n)) + " " + valueStyle.Render(n.Name) +
			keyDescStyle.Render(fmt.Sprintf("   ns %s · %s · %s", orDash(n.Namespace), orDash(n.Health), orDash(n.Status)))
		if n.Meta != "" {
			details += keyDescStyle.Render(" · " + n.Meta)
		}
	}
	keys := renderKeys(m.keyHints(), w)
	return strings.Join(append(append([]string{top}, lines...), sep, details, " "+keys), "\n")
}

// renderCells renders one canvas row from column x0, grouping runs of equal style.
func renderCells(row []gcell, x0, w int) string {
	var b strings.Builder
	var run []rune
	var cur gcell
	flush := func() {
		if len(run) > 0 {
			st := lipgloss.NewStyle().Foreground(cur.fg).Bold(cur.bold)
			if cur.fg == "" {
				st = lipgloss.NewStyle()
			}
			b.WriteString(st.Render(string(run)))
			run = run[:0]
		}
	}
	for x := x0; x < min(len(row), x0+w); x++ {
		c := row[x]
		if c.fg != cur.fg || c.bold != cur.bold {
			flush()
			cur = c
		}
		run = append(run, c.r)
	}
	flush()
	return b.String()
}

// renderKeys draws "<key> desc" hints on one line, dropping those that don't fit.
func renderKeys(keys [][2]string, width int) string {
	var out []string
	used := 0
	for _, k := range keys {
		plain := "<" + k[0] + "> " + k[1]
		if width > 0 && used+lipgloss.Width(plain)+2 > width {
			break
		}
		used += lipgloss.Width(plain) + 2
		out = append(out, keyStyle.Render("<"+k[0]+">")+" "+keyDescStyle.Render(k[1]))
	}
	return strings.Join(out, "  ")
}

// nextIssue returns the next node (after cur, wrapping) that needs attention.
func nextIssue(nodes []argo.GraphNode, cur, dir int) int {
	n := len(nodes)
	for k := 1; k <= n; k++ {
		i := ((cur+dir*k)%n + n) % n
		if nodeHasIssue(nodes[i]) {
			return i
		}
	}
	return cur
}

func nodeHasIssue(n argo.GraphNode) bool {
	h := strings.ToLower(n.Health)
	s := strings.ToLower(strings.ReplaceAll(n.Status, " ", ""))
	if h == "degraded" || h == "missing" || h == "progressing" || s == "outofsync" {
		return true
	}
	meta := strings.ToLower(n.Meta)
	return strings.EqualFold(n.Kind, "Pod") && meta != "" && !strings.HasPrefix(meta, "running") && !strings.Contains(meta, "completed")
}

// graphStep moves the cursor following the drawing, not the node order:
// "child" picks the child box nearest vertically, "down"/"up" the next box in
// the same column.
func graphStep(g argo.Graph, gl graphLayout, cur int, dir string) int {
	if cur < 0 || cur >= len(g.Nodes) || len(gl.boxes) != len(g.Nodes) {
		return cur
	}
	at := gl.boxes[cur]
	best, bestD := cur, math.MaxInt
	for i, n := range g.Nodes {
		if i == cur {
			continue
		}
		b := gl.boxes[i]
		d := 0
		switch dir {
		case "child":
			if n.Parent != cur {
				continue
			}
			d = abs(b.y - at.y)
		case "down":
			if b.x != at.x || b.y <= at.y {
				continue
			}
			d = b.y - at.y
		case "up":
			if b.x != at.x || b.y >= at.y {
				continue
			}
			d = at.y - b.y
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// graphMatches returns nodes whose "kind name" matches q (k9s "/" semantics),
// in reading order: column by column, top to bottom.
func graphMatches(g argo.Graph, gl graphLayout, q string) []int {
	match := filterMatcher(q)
	if match == nil || len(gl.boxes) != len(g.Nodes) {
		return nil
	}
	var out []int
	for i, n := range g.Nodes {
		if match(n.Kind + " " + kindLabel(n) + " " + n.Name) {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		pa, pb := gl.boxes[out[a]], gl.boxes[out[b]]
		if pa.x != pb.x {
			return pa.x < pb.x
		}
		return pa.y < pb.y
	})
	return out
}

func cycleMatch(ms []int, cur int, forward bool) int {
	for k, i := range ms {
		if i == cur {
			if forward {
				return ms[(k+1)%len(ms)]
			}
			return ms[(k-1+len(ms))%len(ms)]
		}
	}
	return ms[0]
}

// graphColumnJump: page / home / end within the selected node's column.
func graphColumnJump(g argo.Graph, gl graphLayout, cur int, key string, viewH int) int {
	if cur < 0 || cur >= len(g.Nodes) || len(gl.boxes) != len(g.Nodes) {
		return cur
	}
	at := gl.boxes[cur]
	target := at.y
	switch key {
	case "pgdown", "ctrl+f":
		target = at.y + max(gBoxH, viewH-gBoxH)
	case "pgup", "ctrl+b":
		target = at.y - max(gBoxH, viewH-gBoxH)
	case "home":
		target = math.MinInt / 2
	case "end":
		target = math.MaxInt / 2
	}
	best, bestD := cur, math.MaxInt
	for i, b := range gl.boxes {
		if b.x != at.x {
			continue
		}
		if d := abs(b.y - target); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// setGraph stores the full graph and shows it minus old ReplicaSets (unless o
// toggled them on), keeping the cursor on the same object when it survives.
func (m *Model) setGraph(full argo.Graph) {
	curID := ""
	if m.graphCursor >= 0 && m.graphCursor < len(m.graph.Nodes) && len(m.graphFull.Nodes) > 0 {
		curID = m.graph.Nodes[m.graphCursor].ID
	}
	m.graphFull = full
	shown, hidden := full, 0
	if !m.showOldRS {
		shown, hidden = pruneOldReplicaSets(full)
	}
	m.graph, m.hiddenRS = shown, hidden
	m.graphCursor = min(max(0, m.graphCursor), max(0, len(shown.Nodes)-1))
	if curID != "" {
		for i, n := range shown.Nodes {
			if n.ID == curID {
				m.graphCursor = i
			}
		}
	}
}

func (m *Model) toggleOldRS() {
	m.showOldRS = !m.showOldRS
	m.setGraph(m.graphFull)
	m.syncGraphViewport()
	m.clampTreeCursor()
	if m.showOldRS {
		m.status = "showing old ReplicaSets — o to hide"
	} else {
		m.status = fmt.Sprintf("%d old ReplicaSets hidden — o to show", m.hiddenRS)
	}
	m.statusErr = false
}

// pruneOldReplicaSets drops ReplicaSets left over by previous rollouts: no pods
// under them and scaled to 0 ("0/0" or Suspended). Indexes are remapped.
func pruneOldReplicaSets(g argo.Graph) (argo.Graph, int) {
	hasKids := map[int]bool{}
	for _, n := range g.Nodes {
		if n.Parent >= 0 {
			hasKids[n.Parent] = true
		}
	}
	drop := map[int]bool{}
	for i, n := range g.Nodes {
		if i > 0 && strings.EqualFold(n.Kind, "ReplicaSet") && !hasKids[i] &&
			(strings.HasPrefix(strings.TrimSpace(n.Meta), "0/0") || strings.EqualFold(n.Health, "Suspended")) {
			drop[i] = true
		}
	}
	if len(drop) == 0 {
		return g, 0
	}
	newIdx := make([]int, len(g.Nodes))
	out := argo.Graph{Root: g.Root}
	for i, n := range g.Nodes {
		if drop[i] {
			newIdx[i] = -1
			continue
		}
		newIdx[i] = len(out.Nodes)
		out.Nodes = append(out.Nodes, n)
	}
	for i := range out.Nodes {
		if p := out.Nodes[i].Parent; p >= 0 {
			out.Nodes[i].Parent = newIdx[p] // parents are never dropped: they have kids
		}
	}
	return out, len(drop)
}
