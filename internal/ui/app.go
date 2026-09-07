package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/argo"
	"github.com/vvrnv/argo9s/internal/argocd"
	"github.com/vvrnv/argo9s/internal/config"
	"github.com/vvrnv/argo9s/internal/kube"
	"github.com/vvrnv/argo9s/internal/session"
)

type viewMode int

const (
	viewList viewMode = iota
	viewDetail
	viewYAML
	viewGraph
	viewHelp
	viewConfirm
	viewLogs
)

const appVersion = "v0.1.0"

type sortCol int

const (
	sortName sortCol = iota
	sortAge
	sortType
	sortStatus
)

type resourceEventMsg argo.Event
type opDoneMsg struct {
	msg string
	err error
}
type tickMsg time.Time
type graphReadyMsg struct {
	graph argo.Graph
	err   error
}

// Model is the Bubble Tea application root.
type Model struct {
	cfg     config.Config
	session *session.Session
	store   *argo.Store
	ops     argo.Mutator
	events  <-chan argo.Event
	cancel  context.CancelFunc

	width  int
	height int

	mode       viewMode
	viewStack  []viewMode // back navigation: detail←graph←list, etc.
	filterKind argo.Kind
	namespace  string
	allNS      bool
	context    string

	resources []argo.Resource
	cursor    int
	filter    string
	filtering bool
	filterIn  textinput.Model

	projectFilter string
	syncFilter    string // "", "synced", "outofsync"
	nameFilter    string

	commanding bool
	cmdIn      textinput.Model

	status     string
	statusErr  bool
	selected   argo.Resource
	detailText string
	vp         viewport.Model

	graph       argo.Graph
	graphCursor int

	logCancel   context.CancelFunc
	logCh       <-chan logOutMsg
	logLines    []string
	logPod      string
	logNS       string
	logFollow   bool
	logPrevious bool
	logX        int
	logErr      string

	yamlRef session.ResourceRef
	yamlX   int

	sortBy   sortCol
	sortDesc bool

	confirmAction string
	confirmTarget argo.Resource

	quitting bool
}

func New(cfg config.Config, sess *session.Session, events <-chan argo.Event, cancel context.CancelFunc) Model {
	fi := textinput.New()
	fi.Placeholder = "filter…  (:name / :project / :sync)"
	fi.CharLimit = 128
	fi.Width = 50

	ci := textinput.New()
	ci.Prompt = ":"
	ci.CharLimit = 128
	ci.Width = 60

	ns := cfg.Namespace
	allNS := cfg.AllNamespaces || ns == "all"
	if sess != nil && sess.Type == config.ConnArgoCD {
		// Argo API returns apps across namespaces by default.
		allNS = true
		ns = "all"
	}
	if allNS {
		ns = "all"
	}

	label := "local"
	if sess != nil {
		label = sess.Label
	}

	return Model{
		cfg:        cfg,
		session:    sess,
		store:      sess.Store,
		ops:        sess.Ops,
		events:     events,
		cancel:     cancel,
		filterKind: argo.KindAll,
		namespace:  ns,
		allNS:      allNS,
		context:    label,
		filterIn:   fi,
		cmdIn:      ci,
		vp:         viewport.New(80, 20),
		status:     "watching…",
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.events), tick())
}

func waitEvent(ch <-chan argo.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return opDoneMsg{msg: "watch closed"}
		}
		return resourceEventMsg(ev)
	}
}

func isTransientWatchErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "closed") ||
		strings.Contains(s, "unexpected eof") ||
		strings.Contains(s, "http2: client connection lost") ||
		strings.Contains(s, "connection reset")
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.vp.Width = max(10, msg.Width-2)
		m.vp.Height = max(5, msg.Height-16)
		if m.mode == viewGraph {
			m.syncGraphViewport()
		}
		return m, nil

	case tickMsg:
		m.refreshList()
		return m, tick()

	case resourceEventMsg:
		ev := argo.Event(msg)
		if ev.Err != nil {
			// Kubernetes watches expire ("closed") and reconnect — not a user-facing failure.
			if isTransientWatchErr(ev.Err) {
				m.statusErr = false
				if m.mode == viewList {
					m.status = "watching…"
				}
			} else {
				m.status = ev.Err.Error()
				m.statusErr = true
			}
		} else {
			m.statusErr = false
			m.status = fmt.Sprintf("%s %s/%s", ev.Type, ev.Resource.Kind, ev.Resource.Name)
			m.refreshList()
		}
		return m, waitEvent(m.events)

	case logOutMsg:
		return m, m.handleLogMsg(msg)

	case yamlLoadedMsg:
		m.detailText = msg.text
		m.yamlRef = msg.ref
		m.yamlX = 0
		m.syncYAMLViewport()
		m.vp.GotoTop()
		if m.mode != viewYAML {
			m.pushView(viewYAML)
		}
		m.status = fmt.Sprintf("yaml %s/%s — h/l scroll · e edit · esc back", msg.ref.Kind, msg.ref.Name)
		m.statusErr = false
		if msg.edit {
			return m, m.launchYAMLEditor(msg.text)
		}
		return m, nil

	case editDoneMsg:
		if msg.err != nil {
			m.status = "editor: " + msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		return m, m.applyEditedYAML(msg.path)

	case opDoneMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			m.statusErr = true
		} else {
			m.status = msg.msg
			m.statusErr = false
		}
		return m, nil

	case graphReadyMsg:
		if msg.err != nil {
			m.status = "graph: " + msg.err.Error()
			m.statusErr = true
			// Still show whatever tree we have (managed resources without live children).
			m.graph = msg.graph
			if m.mode == viewGraph {
				m.syncGraphViewport()
			}
		} else {
			m.graph = msg.graph
			if m.graphCursor >= len(m.graph.Nodes) {
				m.graphCursor = max(0, len(m.graph.Nodes)-1)
			}
			if m.mode == viewGraph {
				m.syncGraphViewport()
				children := 0
				for _, n := range m.graph.Nodes {
					if n.Depth >= 2 {
						children++
					}
				}
				m.status = fmt.Sprintf("graph: %s/%s (%d nodes, %d rs/pods)", m.selected.Namespace, m.selected.Name, len(m.graph.Nodes), children)
				m.statusErr = false
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, tea.Quit
	}

	if m.mode == viewConfirm {
		switch msg.String() {
		case "y", "Y":
			return m.runConfirm()
		case "n", "N", "esc", "q":
			m.mode = viewList
			m.status = "cancelled"
			m.statusErr = false
			return m, nil
		}
		return m, nil
	}

	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filterIn.Blur()
			m.filter = ""
			m.filterIn.SetValue("")
			m.refreshList()
			return m, nil
		case "enter":
			m.filtering = false
			m.filterIn.Blur()
			m.filter = m.filterIn.Value()
			m.refreshList()
			return m, nil
		default:
			var cmd tea.Cmd
			m.filterIn, cmd = m.filterIn.Update(msg)
			m.filter = m.filterIn.Value()
			m.refreshList()
			return m, cmd
		}
	}

	if m.commanding {
		switch msg.String() {
		case "esc":
			m.commanding = false
			m.cmdIn.Blur()
			m.cmdIn.SetValue("")
			return m, nil
		case "enter":
			cmd := strings.TrimSpace(m.cmdIn.Value())
			m.commanding = false
			m.cmdIn.Blur()
			m.cmdIn.SetValue("")
			return m.executeCommand(cmd)
		default:
			var c tea.Cmd
			m.cmdIn, c = m.cmdIn.Update(msg)
			return m, c
		}
	}

	if m.mode == viewHelp || m.mode == viewDetail || m.mode == viewYAML || m.mode == viewGraph || m.mode == viewLogs {
		switch msg.String() {
		case "q", "esc", "backspace":
			if m.mode == viewLogs {
				m.stopLogs()
			}
			m.popView()
			return m, nil
		case "j", "down":
			if m.mode == viewGraph {
				m.graphCursor = graphMoveSibling(m.graph.Nodes, m.graphCursor, +1)
				m.syncGraphViewport()
				return m, nil
			}
			m.vp.LineDown(1)
			return m, nil
		case "k", "up":
			if m.mode == viewGraph {
				m.graphCursor = graphMoveSibling(m.graph.Nodes, m.graphCursor, -1)
				m.syncGraphViewport()
				return m, nil
			}
			m.vp.LineUp(1)
			return m, nil
		case "h", "left":
			if m.mode == viewLogs {
				if m.logX > 0 {
					m.logX = max(0, m.logX-8)
					m.syncLogViewport()
				}
				return m, nil
			}
			if m.mode == viewYAML {
				if m.yamlX > 0 {
					m.yamlX = max(0, m.yamlX-8)
					m.syncYAMLViewport()
				}
				return m, nil
			}
			if m.mode == viewGraph {
				m.graphCursor = graphMoveParent(m.graph.Nodes, m.graphCursor)
				m.syncGraphViewport()
			}
			return m, nil
		case "l":
			if m.mode == viewLogs {
				m.logX += 8
				m.syncLogViewport()
				return m, nil
			}
			if m.mode == viewYAML {
				m.yamlX += 8
				m.syncYAMLViewport()
				return m, nil
			}
			if m.mode == viewGraph && m.graphCursor >= 0 && m.graphCursor < len(m.graph.Nodes) {
				if strings.EqualFold(m.graph.Nodes[m.graphCursor].Kind, "Pod") {
					return m.openPodLogs()
				}
				m.graphCursor = graphMoveChild(m.graph.Nodes, m.graphCursor)
				m.syncGraphViewport()
			}
			return m, nil
		case "right":
			if m.mode == viewLogs {
				m.logX += 8
				m.syncLogViewport()
				return m, nil
			}
			if m.mode == viewYAML {
				m.yamlX += 8
				m.syncYAMLViewport()
				return m, nil
			}
			if m.mode == viewGraph {
				m.graphCursor = graphMoveChild(m.graph.Nodes, m.graphCursor)
				m.syncGraphViewport()
			}
			return m, nil
		case "0":
			if m.mode == viewLogs {
				m.logX = 0
				m.syncLogViewport()
				return m, nil
			}
			if m.mode == viewYAML {
				m.yamlX = 0
				m.syncYAMLViewport()
				return m, nil
			}
			return m, nil
		case "p":
			if m.mode == viewLogs {
				return m.startPodLogs(!m.logPrevious)
			}
			return m, nil
		case "g":
			if m.mode == viewGraph {
				m.graphCursor = 0
				m.syncGraphViewport()
				return m, nil
			}
			m.vp.GotoTop()
			return m, nil
		case "G":
			if m.mode == viewGraph {
				if len(m.graph.Nodes) > 0 {
					m.graphCursor = len(m.graph.Nodes) - 1
					m.syncGraphViewport()
				}
				return m, nil
			}
			m.vp.GotoBottom()
			return m, nil
		case "enter", "d":
			if m.mode == viewGraph && len(m.graph.Nodes) > 0 {
				m.detailText = argo.Describe(m.selected) + "\n\n" + graphNodeSummary(m.graph.Nodes[m.graphCursor])
				m.vp.SetContent(m.detailText)
				m.vp.GotoTop()
				m.pushView(viewDetail)
			}
			return m, nil
		case "y":
			if m.mode == viewGraph {
				return m.openGraphNodeYAML(false)
			}
			return m, nil
		case "e":
			if m.mode == viewGraph {
				return m.openGraphNodeYAML(true)
			}
			if m.mode == viewYAML {
				if m.ops.ReadOnly() {
					m.status = "read-only — set readOnly: false on this connection to edit"
					m.statusErr = true
					return m, nil
				}
				return m, m.launchYAMLEditor(m.detailText)
			}
			return m, nil
		case "?":
			if m.mode == viewHelp {
				m.popView()
			} else {
				m.vp.SetContent(helpText())
				m.vp.GotoTop()
				m.pushView(viewHelp)
			}
			return m, nil
		}
		return m, nil
	}

	// list mode
	switch msg.String() {
	case "ctrl+c", "Q":
		m.quitting = true
		m.stopLogs()
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "q", "esc":
		m.status = "press Q or :q to quit"
		return m, nil
	case "?":
		m.vp.SetContent(helpText())
		m.vp.GotoTop()
		m.pushView(viewHelp)
		return m, nil
	case ":":
		m.commanding = true
		m.cmdIn.SetValue("")
		m.cmdIn.Focus()
		return m, nil
	case "/":
		m.filtering = true
		m.filterIn.SetValue(m.filter)
		m.filterIn.Focus()
		return m, nil
	case "j", "down":
		if m.cursor < len(m.resources)-1 {
			m.cursor++
		}
		return m, nil
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "home", "ctrl+a":
		m.cursor = 0
		return m, nil
	case "G", "end":
		if len(m.resources) > 0 {
			m.cursor = len(m.resources) - 1
		}
		return m, nil
	case "g":
		return m.openGraph()
	case "enter", "d":
		if r, ok := m.current(); ok {
			m.selected = r
			m.detailText = argo.Describe(r)
			m.vp.SetContent(m.detailText)
			m.vp.GotoTop()
			m.pushView(viewDetail)
		}
		return m, nil
	case "y":
		if r, ok := m.current(); ok {
			text, err := m.ops.YAML(r)
			if err != nil {
				m.status = err.Error()
				m.statusErr = true
				return m, nil
			}
			m.selected = r
			m.vp.SetContent(text)
			m.vp.GotoTop()
			m.pushView(viewYAML)
		}
		return m, nil
	case "R":
		return m.withCurrentApp(func(r argo.Resource) tea.Cmd {
			return runOp(func(ctx context.Context) (string, error) {
				if err := m.ops.Sync(ctx, r); err != nil {
					return "", err
				}
				return fmt.Sprintf("sync started: %s", r.Name), nil
			})
		})
	case "f":
		return m.withCurrentApp(func(r argo.Resource) tea.Cmd {
			return runOp(func(ctx context.Context) (string, error) {
				if err := m.ops.Refresh(ctx, r, false); err != nil {
					return "", err
				}
				return fmt.Sprintf("refresh: %s", r.Name), nil
			})
		})
	case "F":
		return m.withCurrentApp(func(r argo.Resource) tea.Cmd {
			return runOp(func(ctx context.Context) (string, error) {
				if err := m.ops.Refresh(ctx, r, true); err != nil {
					return "", err
				}
				return fmt.Sprintf("hard refresh: %s", r.Name), nil
			})
		})
	case "s":
		return m.withCurrentApp(func(r argo.Resource) tea.Cmd {
			return runOp(func(ctx context.Context) (string, error) {
				if err := m.ops.Suspend(ctx, r); err != nil {
					return "", err
				}
				return fmt.Sprintf("suspended (manual): %s", r.Name), nil
			})
		})
	case "r":
		return m.withCurrentApp(func(r argo.Resource) tea.Cmd {
			return runOp(func(ctx context.Context) (string, error) {
				if err := m.ops.Resume(ctx, r); err != nil {
					return "", err
				}
				return fmt.Sprintf("resumed (auto-sync): %s", r.Name), nil
			})
		})
	case "ctrl+d":
		if r, ok := m.current(); ok {
			m.confirmAction = "delete"
			m.confirmTarget = r
			m.mode = viewConfirm
		}
		return m, nil
	case "N":
		m.toggleSort(sortName)
		return m, nil
	case "A":
		m.toggleSort(sortAge)
		return m, nil
	case "T":
		m.toggleSort(sortType)
		return m, nil
	case "S":
		m.toggleSort(sortStatus)
		return m, nil
	}

	return m, nil
}

func (m Model) withCurrentApp(fn func(argo.Resource) tea.Cmd) (tea.Model, tea.Cmd) {
	r, ok := m.current()
	if !ok {
		return m, nil
	}
	if r.Kind != argo.KindApplication {
		m.status = "only Applications support this action"
		m.statusErr = true
		return m, nil
	}
	return m, fn(r)
}

func (m *Model) pushView(next viewMode) {
	if m.mode == next {
		return
	}
	m.viewStack = append(m.viewStack, m.mode)
	m.mode = next
}

func (m *Model) popView() {
	if m.mode == viewLogs {
		m.stopLogs()
	}
	if len(m.viewStack) == 0 {
		m.mode = viewList
		return
	}
	prev := m.viewStack[len(m.viewStack)-1]
	m.viewStack = m.viewStack[:len(m.viewStack)-1]
	m.mode = prev
	switch m.mode {
	case viewGraph:
		m.syncGraphViewport()
		m.status = fmt.Sprintf("graph: %s/%s (%d nodes)", m.selected.Namespace, m.selected.Name, len(m.graph.Nodes))
		m.statusErr = false
	case viewList:
		m.status = "list"
		m.statusErr = false
	}
}

func (m Model) openGraph() (tea.Model, tea.Cmd) {
	r, ok := m.current()
	if !ok {
		return m, nil
	}
	if r.Kind != argo.KindApplication {
		m.status = "graph view is for Applications (select an app, press g)"
		m.statusErr = true
		return m, nil
	}
	if fresh, ok := m.store.Get(r.Key()); ok {
		r = fresh
	}
	m.selected = r
	m.graph = argo.BuildAppGraph(r)
	m.graphCursor = 0
	m.pushView(viewGraph)
	m.syncGraphViewport()
	m.status = fmt.Sprintf("graph: %s/%s — loading pods…", r.Namespace, r.Name)
	m.statusErr = false

	client := m.session
	base := m.graph
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		g := base
		if err := client.EnrichGraph(ctx, &g); err != nil {
			return graphReadyMsg{graph: g, err: err}
		}
		return graphReadyMsg{graph: g}
	}
}

func (m *Model) syncGraphViewport() {
	content := renderGraph(m.graph, m.graphCursor, m.width, m.height)
	m.vp.SetContent(content)
	if len(m.graph.Nodes) == 0 {
		return
	}
	rows := layoutRows(m.graph.Nodes)
	if m.graphCursor >= 0 && m.graphCursor < len(rows) {
		// Each row slot ≈ box height + gap (~6 lines).
		m.vp.SetYOffset(max(0, rows[m.graphCursor].row*6-1))
	}
}

// graphMoveParent: h/left — step toward the root.
func graphMoveParent(nodes []argo.GraphNode, cur int) int {
	if cur < 0 || cur >= len(nodes) {
		return cur
	}
	p := nodes[cur].Parent
	if p >= 0 && p < len(nodes) {
		return p
	}
	return cur
}

// graphMoveChild: l/right — first child of the current node.
func graphMoveChild(nodes []argo.GraphNode, cur int) int {
	if cur < 0 || cur >= len(nodes) {
		return cur
	}
	for i, n := range nodes {
		if n.Parent == cur {
			return i
		}
	}
	return cur
}

// graphMoveSibling: j/k — next/prev node with the same parent (visual up/down).
func graphMoveSibling(nodes []argo.GraphNode, cur int, delta int) int {
	if cur < 0 || cur >= len(nodes) || delta == 0 {
		return cur
	}
	parent := nodes[cur].Parent
	var sibs []int
	for i, n := range nodes {
		if n.Parent == parent {
			sibs = append(sibs, i)
		}
	}
	if len(sibs) == 0 {
		return cur
	}
	pos := 0
	for i, s := range sibs {
		if s == cur {
			pos = i
			break
		}
	}
	pos += delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(sibs) {
		pos = len(sibs) - 1
	}
	return sibs[pos]
}

func graphNodeSummary(n argo.GraphNode) string {
	return fmt.Sprintf("Graph node:\n  %s\n  %s\n  health=%s sync=%s", n.Title, n.Subtitle, n.Health, n.Status)
}

func runOp(fn func(context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg, err := fn(ctx)
		return opDoneMsg{msg: msg, err: err}
	}
}

func (m Model) runConfirm() (tea.Model, tea.Cmd) {
	r := m.confirmTarget
	m.mode = viewList
	switch m.confirmAction {
	case "delete":
		return m, runOp(func(ctx context.Context) (string, error) {
			if err := m.ops.Delete(ctx, r); err != nil {
				return "", err
			}
			return fmt.Sprintf("deleted %s/%s", r.Kind, r.Name), nil
		})
	default:
		m.status = "unknown action"
		m.statusErr = true
		return m, nil
	}
}

func (m Model) executeCommand(cmd string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return m, nil
	}
	switch strings.ToLower(parts[0]) {
	case "q", "q!", "quit":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "help":
		m.vp.SetContent(helpText())
		m.vp.GotoTop()
		m.pushView(viewHelp)
		return m, nil
	case "readonly", "ro":
		m.ops.SetReadOnly(!m.ops.ReadOnly())
		m.cfg.ReadOnly = m.ops.ReadOnly()
		_ = config.Save(m.cfg)
		m.status = fmt.Sprintf("readOnly=%v", m.ops.ReadOnly())
		return m, nil
	case "nocolor", "no-color", "mono":
		m.cfg.NoColor = !m.cfg.NoColor
		sessType := ""
		if m.session != nil {
			sessType = m.session.Type
		}
		ApplyColorMode(m.cfg.NoColor, sessType)
		_ = config.Save(m.cfg)
		m.status = fmt.Sprintf("noColor=%v", m.cfg.NoColor)
		return m, nil
	case "ns", "namespace":
		if len(parts) < 2 {
			m.status = "usage: :ns <namespace|all>"
			m.statusErr = true
			return m, nil
		}
		ns := parts[1]
		m.allNS = ns == "all"
		m.namespace = ns
		m.cfg.Namespace = ns
		m.cfg.AllNamespaces = m.allNS
		_ = config.Save(m.cfg)
		m.status = fmt.Sprintf("namespace=%s (restart watch on next launch for scope change)", ns)
		m.refreshList()
		return m, nil
	case "project", "p":
		// Filter Applications by spec.project (Argo CD AppProject name).
		if len(parts) < 2 || parts[1] == "clear" || parts[1] == "-" {
			m.projectFilter = ""
			m.cursor = 0
			m.refreshList()
			m.status = "project filter cleared"
			return m, nil
		}
		m.projectFilter = parts[1]
		m.filterKind = argo.KindApplication
		m.cursor = 0
		m.refreshList()
		m.status = fmt.Sprintf("project filter: %s", m.projectFilter)
		return m, nil
	case "name", "n":
		if len(parts) < 2 || parts[1] == "clear" || parts[1] == "-" {
			m.nameFilter = ""
			m.cursor = 0
			m.refreshList()
			m.status = "name filter cleared"
			return m, nil
		}
		m.nameFilter = strings.Join(parts[1:], " ")
		m.cursor = 0
		m.refreshList()
		m.status = fmt.Sprintf("name filter: %s", m.nameFilter)
		return m, nil
	case "sync":
		if len(parts) < 2 || parts[1] == "clear" || parts[1] == "-" || parts[1] == "all" {
			m.syncFilter = ""
			m.cursor = 0
			m.refreshList()
			m.status = "sync filter cleared"
			return m, nil
		}
		sf, ok := parseSyncFilter(parts[1])
		if !ok {
			m.status = "usage: :sync synced|outofsync|clear"
			m.statusErr = true
			return m, nil
		}
		m.syncFilter = sf
		m.filterKind = argo.KindApplication
		m.cursor = 0
		m.refreshList()
		m.status = fmt.Sprintf("sync filter: %s", m.syncFilter)
		return m, nil
	case "outofsync", "oos":
		m.syncFilter = "outofsync"
		m.filterKind = argo.KindApplication
		m.cursor = 0
		m.refreshList()
		m.status = "sync filter: outofsync"
		return m, nil
	case "synced":
		m.syncFilter = "synced"
		m.filterKind = argo.KindApplication
		m.cursor = 0
		m.refreshList()
		m.status = "sync filter: synced"
		return m, nil
	case "ctx", "context":
		if m.session != nil && m.session.Type == config.ConnArgoCD {
			m.status = fmt.Sprintf("connection=%s  %s  (restart argo9s to switch)", m.session.Name, m.session.Label)
			return m, nil
		}
		if len(parts) < 2 {
			names, current, err := kube.ContextNames()
			if err != nil {
				m.status = err.Error()
				m.statusErr = true
				return m, nil
			}
			m.status = fmt.Sprintf("current=%s available=%s", current, strings.Join(names, ", "))
			return m, nil
		}
		m.status = "switch kube context: argo9s --context <name>  |  switch Argo: argo9s --connection <name>"
		m.statusErr = true
		return m, nil
	case "app", "apps", "application", "applications":
		m.filterKind = argo.KindApplication
		m.cursor = 0
		m.refreshList()
		m.status = "view: Applications"
		return m, nil
	case "appset", "appsets", "applicationset", "applicationsets":
		m.filterKind = argo.KindApplicationSet
		m.cursor = 0
		m.refreshList()
		m.status = "view: ApplicationSets"
		return m, nil
	case "appproject", "appprojects", "projects":
		m.filterKind = argo.KindAppProject
		m.projectFilter = ""
		m.cursor = 0
		m.refreshList()
		m.status = "view: AppProjects"
		return m, nil
	case "graph":
		return m.openGraph()
	case "all":
		m.filterKind = argo.KindAll
		m.cursor = 0
		m.refreshList()
		m.status = "view: All"
		return m, nil
	default:
		if k, ok := argo.ParseKind(parts[0]); ok {
			m.filterKind = k
			m.cursor = 0
			m.refreshList()
			m.status = fmt.Sprintf("view: %s", k)
			return m, nil
		}
		m.status = fmt.Sprintf("unknown command: %s", parts[0])
		m.statusErr = true
		return m, nil
	}
}

func (m *Model) toggleSort(col sortCol) {
	if m.sortBy == col {
		m.sortDesc = !m.sortDesc
	} else {
		m.sortBy = col
		m.sortDesc = false
	}
	m.refreshList()
}

func (m *Model) current() (argo.Resource, bool) {
	if len(m.resources) == 0 || m.cursor < 0 || m.cursor >= len(m.resources) {
		return argo.Resource{}, false
	}
	return m.resources[m.cursor], true
}

func (m *Model) refreshList() {
	// Argo API lists apps across namespaces; never filter to local kube ns.
	if m.session != nil && m.session.Type == config.ConnArgoCD {
		m.allNS = true
		m.namespace = "all"
	}
	items := m.store.List(m.filterKind, m.namespace, m.allNS)
	if m.projectFilter != "" {
		pf := strings.ToLower(m.projectFilter)
		filtered := make([]argo.Resource, 0, len(items))
		for _, r := range items {
			if strings.EqualFold(r.Project, m.projectFilter) || strings.Contains(strings.ToLower(r.Project), pf) {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}
	if m.syncFilter != "" {
		filtered := make([]argo.Resource, 0, len(items))
		for _, r := range items {
			if syncMatches(r.Sync, m.syncFilter) {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}
	if m.nameFilter != "" {
		nf := strings.ToLower(m.nameFilter)
		filtered := make([]argo.Resource, 0, len(items))
		for _, r := range items {
			if strings.Contains(strings.ToLower(r.Name), nf) {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}
	if m.filter != "" {
		f := strings.ToLower(m.filter)
		filtered := make([]argo.Resource, 0, len(items))
		for _, r := range items {
			hay := strings.ToLower(r.Name + " " + r.Namespace + " " + string(r.Kind) + " " + r.Project + " " + r.Sync + " " + r.Health + " " + r.Message + " " + r.ConditionSummary())
			if strings.Contains(hay, f) {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		var less bool
		switch m.sortBy {
		case sortAge:
			less = a.Age.After(b.Age)
			if !m.sortDesc {
				less = a.Age.Before(b.Age)
			}
			return less
		case sortType:
			less = string(a.Kind) < string(b.Kind)
		case sortStatus:
			less = a.Health+a.Sync < b.Health+b.Sync
		default:
			less = a.Name < b.Name
		}
		if m.sortDesc {
			return !less
		}
		return less
	})
	m.resources = items
	if m.cursor >= len(m.resources) {
		m.cursor = max(0, len(m.resources)-1)
	}
	// Graph is enriched async; don't rebuild from store on every tick (would drop pods).
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	header := m.renderHeader()
	footer := m.renderFooter()
	var body string

	switch m.mode {
	case viewHelp, viewDetail, viewYAML, viewGraph, viewLogs:
		if m.mode == viewGraph {
			body = m.vp.View()
		} else if m.mode == viewLogs {
			title := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Logs %s/%s", m.logNS, m.logPod))
			body = title + "\n" + m.vp.View()
		} else {
			title := map[viewMode]string{
				viewHelp:   "Help",
				viewDetail: fmt.Sprintf("Describe %s/%s", m.selected.Kind, m.selected.Name),
				viewYAML:   fmt.Sprintf("YAML %s/%s", m.selected.Kind, m.selected.Name),
			}[m.mode]
			body = lipgloss.NewStyle().Bold(true).Render(title) + "\n" + m.vp.View()
		}
	case viewConfirm:
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Bold(true).Render(
			fmt.Sprintf("Delete %s/%s/%s? [y/n]", m.confirmTarget.Kind, m.confirmTarget.Namespace, m.confirmTarget.Name),
		)
	default:
		body = m.renderTable()
	}

	parts := []string{header, body}
	if m.filtering {
		parts = append(parts, "/"+m.filterIn.View())
	}
	if m.commanding {
		parts = append(parts, m.cmdIn.View())
	}
	parts = append(parts, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) renderHeader() string {
	ro := "RW"
	roStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	if m.ops.ReadOnly() {
		ro = "READONLY"
		roStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	}
	ns := m.namespace
	if m.allNS {
		ns = "all"
	}

	counts := m.countByKind()
	connType := "kubernetes"
	if m.session != nil {
		connType = m.session.Type
	}
	leftLines := []string{
		fmt.Sprintf("Connection: %s (%s)", m.context, connType),
		fmt.Sprintf("Namespace: %s   %s", ns, roStyle.Render(ro)),
		fmt.Sprintf("View: %s", m.filterKind.Short()),
	}
	if m.projectFilter != "" {
		leftLines = append(leftLines, lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("Project: "+m.projectFilter))
	}
	if m.nameFilter != "" {
		leftLines = append(leftLines, lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Render("Name: "+m.nameFilter))
	}
	if m.syncFilter != "" {
		syncColor := lipgloss.Color("114")
		if m.syncFilter == "outofsync" {
			syncColor = lipgloss.Color("214")
		}
		leftLines = append(leftLines, lipgloss.NewStyle().Foreground(syncColor).Render("Sync: "+m.syncFilter))
	}
	leftLines = append(leftLines,
		"",
		fmt.Sprintf("Applications     %d", counts[argo.KindApplication]),
		fmt.Sprintf("ApplicationSets  %d", counts[argo.KindApplicationSet]),
		fmt.Sprintf("AppProjects      %d", counts[argo.KindAppProject]),
		fmt.Sprintf("Listed           %d", len(m.resources)),
	)
	left := strings.Join(leftLines, "\n")

	subtitle := logoSubtitle(appVersion, "Argo CD")
	var right string
	if m.width >= 100 {
		right = renderLogo(subtitle)
	} else {
		right = renderLogoCompact(subtitle)
	}

	gap := 4
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	spacer := max(2, m.width-leftW-rightW-gap)
	if m.width > 0 && leftW+rightW+gap > m.width {
		return lipgloss.JoinVertical(lipgloss.Left, left, right)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", spacer), right)
}

func (m Model) countByKind() map[argo.Kind]int {
	out := map[argo.Kind]int{}
	for _, r := range m.store.List(argo.KindAll, m.namespace, m.allNS) {
		out[r.Kind]++
	}
	return out
}

func (m Model) renderFooter() string {
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	if m.statusErr {
		statusStyle = statusStyle.Foreground(lipgloss.Color("196"))
	}
	keys := "?:help  /:filter  :name  :sync outofsync  :project  g:graph  R:sync  s:suspend  r:resume"
	switch {
	case m.mode == viewLogs:
		keys = "h/l scroll  p:previous  j/k  esc:stop"
	case m.mode == viewYAML:
		keys = "h/l scroll  e:edit  j/k  esc:back"
	case m.mode == viewGraph:
		keys = "y:yaml e:edit  l on pod:logs  → child  esc:back"
	case m.width > 0 && m.width < 100:
		keys = "?:help :name :sync oos g:graph s/r R:sync q:back"
	}
	if m.width > 0 {
		keys = truncate(keys, m.width)
	}
	status := m.status
	if m.width > 0 {
		status = truncate(status, m.width)
	}
	return statusStyle.Render(status) + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(keys)
}

// tableCols sizes list columns to the terminal width. Narrow terminals drop
// NAMESPACE / PROJECT / REV; leftover width goes to NAME.
type tableCols struct {
	kind, ns, name, project, sync, health, rev, age int
	showNS, showProject, showRev                    bool
}

func computeTableCols(termW int) tableCols {
	if termW <= 0 {
		termW = 100
	}
	const indent = 2
	const markers = 6 // room for " ⏸ !" (emoji can be 2 cells)
	avail := termW - indent - markers

	c := tableCols{
		kind: 6, ns: 12, project: 12, sync: 10, health: 10, rev: 8, age: 5,
		showNS: true, showProject: true, showRev: true,
	}

	fixed := func() int {
		n, gaps := c.kind+c.sync+c.health+c.age, 3
		if c.showNS {
			n += c.ns
			gaps++
		}
		if c.showProject {
			n += c.project
			gaps++
		}
		if c.showRev {
			n += c.rev
			gaps++
		}
		return n + gaps // single spaces between columns
	}

	for fixed()+12 > avail { // need at least ~12 for NAME
		switch {
		case c.showRev:
			c.showRev, c.rev = false, 0
		case c.showProject && c.project > 8:
			c.project = 8
		case c.showNS && c.ns > 8:
			c.ns = 8
		case c.showProject:
			c.showProject, c.project = false, 0
		case c.showNS:
			c.showNS, c.ns = false, 0
		case c.sync > 8:
			c.sync = 8
		case c.health > 8:
			c.health = 8
		default:
			c.name = max(8, avail-fixed())
			return c
		}
	}

	c.name = max(12, avail-fixed())
	// Keep NAME compact so SYNC/HEALTH stay near the name, not far right.
	const nameCap = 32
	if c.name > nameCap {
		extra := c.name - nameCap
		c.name = nameCap
		if c.showProject {
			c.project += extra / 2
			extra -= extra / 2
		}
		if c.showNS {
			c.ns += extra
		}
	}
	return c
}

func (c tableCols) formatRow(kind, ns, name, project, sync, health, rev, age string) string {
	parts := []string{padTruncate(kind, c.kind)}
	if c.showNS {
		parts = append(parts, padTruncate(ns, c.ns))
	}
	// NAME then SYNC/HEALTH — statuses sit next to the app name.
	parts = append(parts, padTruncate(name, c.name), padTruncate(sync, c.sync), padTruncate(health, c.health))
	if c.showProject {
		parts = append(parts, padTruncate(project, c.project))
	}
	if c.showRev {
		parts = append(parts, padTruncate(rev, c.rev))
	}
	parts = append(parts, padTruncate(age, c.age))
	return "  " + strings.Join(parts, " ")
}

func (m Model) renderTable() string {
	if len(m.resources) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("  no resources (is Argo CD installed? try :ns all)")
	}

	cols := computeTableCols(m.width)
	header := cols.formatRow("KIND", "NAMESPACE", "NAME", "PROJECT", "SYNC", "HEALTH", "REV", "AGE")
	lines := []string{lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true).Render(header)}

	headerLines := 10
	if m.width > 0 && m.width < 100 {
		headerLines = 8
	}
	maxRows := max(3, m.height-headerLines)
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := min(len(m.resources), start+maxRows)

	for i := start; i < end; i++ {
		r := m.resources[i]
		line := cols.formatRow(
			r.Kind.Short(),
			r.Namespace,
			r.Name,
			r.Project,
			r.Sync,
			r.Health,
			r.Revision,
			r.AgeString(),
		)
		if r.Suspended {
			line += " ⏸"
		}
		if r.HasConditions() {
			line += " !"
		}
		style := lipgloss.NewStyle()
		if i == m.cursor {
			style = style.Background(lipgloss.Color("238")).Foreground(lipgloss.Color("255"))
			if m.width > 0 {
				line = padTruncate(line, m.width)
			}
		} else {
			style = style.Foreground(rowColor(r))
		}
		lines = append(lines, style.Render(line))
	}
	return strings.Join(lines, "\n")
}

func rowColor(r argo.Resource) lipgloss.Color {
	sync := strings.ToLower(strings.ReplaceAll(r.Sync, " ", ""))
	health := strings.ToLower(r.Health)
	// Unknown stays gray even if conditions are present (still shown via "!").
	if sync == "unknown" || health == "unknown" {
		return lipgloss.Color("245")
	}
	for _, c := range r.Conditions {
		t := strings.ToLower(c.Type)
		if strings.Contains(t, "error") {
			return lipgloss.Color("196") // red — ComparisonError / InvalidSpecError
		}
		if strings.Contains(t, "warning") {
			return lipgloss.Color("214")
		}
	}
	return healthColor(r.Health, r.Sync)
}

func healthColor(health, sync string) lipgloss.Color {
	// Sync OutOfSync wins over Healthy — otherwise Healthy+OutOfSync looked green.
	switch strings.ToLower(strings.ReplaceAll(sync, " ", "")) {
	case "outofsync":
		return lipgloss.Color("214") // yellow
	case "unknown":
		return lipgloss.Color("245") // gray
	}
	switch strings.ToLower(health) {
	case "unknown":
		return lipgloss.Color("245") // gray
	case "degraded", "missing":
		return lipgloss.Color("196") // red
	case "progressing":
		return lipgloss.Color("39") // blue
	case "suspended":
		return lipgloss.Color("214") // yellow
	case "healthy":
		return lipgloss.Color("114") // green
	}
	switch strings.ToLower(strings.ReplaceAll(sync, " ", "")) {
	case "synced":
		return lipgloss.Color("114")
	}
	return lipgloss.Color("252")
}

func parseSyncFilter(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "synced", "sync", "ok":
		return "synced", true
	case "outofsync", "oos", "out-of-sync", "out_of_sync", "drift":
		return "outofsync", true
	default:
		return "", false
	}
}

func syncMatches(sync, filter string) bool {
	s := strings.ToLower(strings.ReplaceAll(sync, " ", ""))
	switch filter {
	case "synced":
		return s == "synced"
	case "outofsync":
		return s == "outofsync"
	default:
		return true
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

func padTruncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = truncate(s, n)
	w := lipgloss.Width(s)
	if w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func helpText() string {
	return strings.TrimSpace(`
argo9s — K9s-inspired TUI for Argo CD

NAVIGATION
  j/k, ↑/↓     move
  home / G     top / bottom of list
  g            resource graph (Application)
  enter / d    describe (includes Application conditions)
  /text        filter (also matches condition type/message)
  y            YAML
  /            name filter
  :            command
  ?            help
  esc / q      back
  Q            quit

PROJECT FILTER
  :project <name>     show Applications in that Argo CD project
  :project clear      clear project filter
  :p <name>           short form

NAME FILTER
  :name <substr>      filter by application name
  :n <substr>         short form
  :name clear         clear
  /text               live substring filter (name/ns/project/…)

SYNC FILTER
  :sync outofsync     only OutOfSync apps (yellow)
  :sync synced        only Synced apps
  :outofsync / :oos   shortcuts
  :sync clear         clear sync filter

COLORS
  Healthy+Synced  green
  OutOfSync       yellow
  Progressing     blue
  Degraded        red

GRAPH VIEW (Argo CD UI style)
  Select an Application, press g
  Left→right: app → cm/svc/deploy → rs → pod
  h/← parent    → child (also l on non-pod)
  l on Pod      stream container logs (follow)
  y             live YAML of selected node
  e             edit YAML in $EDITOR (if not read-only)
  j/↓ k/↑       siblings
  enter describe, esc back

LOGS
  From graph: highlight a Pod, press l
  h/l or ←/→    horizontal scroll (long lines)
  0             reset horizontal offset
  p             toggle previous container logs (restarts)
  j/k scroll, esc stop / back

YAML
  y on graph node — view live manifest
  e — edit via $EDITOR then apply (RW connections only)
  h/l horizontal scroll

ACTIONS (Applications; disabled in read-only)
  R            sync
  f / F        refresh / hard refresh
  s / r        suspend (manual) / resume (auto-sync)
  ctrl+d       delete (confirm)

COMMANDS
  :all / :app / :appset / :appproject
  :ns <ns|all>
  :project <name>
  :graph
  :readonly
  :nocolor
  :ctx
  :q

SORT
  N / A / T / S         name / age / type / status

Default namespace: argocd. Read-only is ON by default.
`)
}

// Run starts the TUI for an opened session.
func Run(sess *session.Session) error {
	cfg := sess.Cfg
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ApplyColorMode(cfg.NoColor, sess.Type)

	// Preload Argo API snapshot so the first frame isn't empty.
	if sess.Type == config.ConnArgoCD && sess.Argo != nil {
		warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
		apps, err := sess.Argo.ListApplications(warmCtx)
		warmCancel()
		if err != nil {
			return fmt.Errorf("list applications: %w", err)
		}
		named := 0
		for _, app := range apps {
			r := argocd.ResourceFromAppMap(app)
			if r.Name == "" {
				continue
			}
			sess.Store.Upsert(r)
			named++
		}
		if len(apps) > 0 && named == 0 {
			return fmt.Errorf("parsed 0/%d application names (%s)", len(apps), argocd.SampleAppDiag(apps))
		}
	}

	events := make(chan argo.Event, 256)
	go func() {
		_ = sess.StartWatch(ctx, events)
		close(events)
	}()

	m := New(cfg, sess, events, cancel)
	m.refreshList()
	if n := len(m.resources); n > 0 {
		m.status = fmt.Sprintf("loaded %d resources (store=%d)", n, sess.Store.Len())
	} else if sess.Type == config.ConnArgoCD {
		m.status = fmt.Sprintf("no resources in view (store=%d) — try :ns all", sess.Store.Len())
		m.statusErr = true
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
