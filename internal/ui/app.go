package ui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
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
	viewTree       // managed resources of an Application, expandable
	viewContainers // container picker for pod logs / shell
)

const appVersion = "v0.2.0"

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

	status      string
	statusErr   bool
	selected    argo.Resource
	detailText  string
	detailTitle string // overrides the describe title (tree node describe)
	textFilter  string // "/" filter inside logs / yaml / describe
	vp          viewport.Model

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

	// drill: enter on an appset / project / cluster lists its Applications (k9s-style).
	expanded        map[string]bool // tree view: expanded node IDs
	gl              graphLayout     // fullscreen graph layout (cells)
	gox, goy        int             // graph scroll offset
	graphQuery      string          // "/" search in the graph
	graphFull       argo.Graph      // unfiltered graph; m.graph is what is shown
	showOldRS       bool            // o: show ReplicaSets scaled to 0 (old rollouts)
	hiddenRS        int
	containers      []session.ContainerInfo
	containerCursor int
	containerAction string // "logs" | "shell" — what enter does in the picker
	podContainer    string // container used by the current logs stream
	drill           *argo.Resource
	drillPrevKind   argo.Kind
	drillPrevCursor int
	switchTo        string // connection chosen in :ctx; Run reopens the session with it

	argoVersion, k8sVersion string // shown in the header, fetched once at start

	loading   string // non-empty while an async load/op runs; drives the footer progress bar
	loadStart time.Time
	spin      int
}

type spinMsg struct{}
type versionsMsg struct{ argo, k8s string }
type appsLoadedMsg struct {
	n   int
	err error
}

// startLoading marks an async load; returns the animation tick only if none is running yet.
func (m *Model) startLoading(what string) tea.Cmd {
	wasIdle := m.loading == ""
	m.loading, m.loadStart = what, time.Now()
	if !wasIdle {
		return nil
	}
	return spinTick()
}

func spinTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

// progressBar is an indeterminate bar (a block bouncing in a track) — we can't know
// how many clusters/objects Argo CD will walk, so no percentage.
func progressBar(frame, width int) string {
	const block = 6
	span := width - block
	pos := frame % (2 * span)
	if pos > span {
		pos = 2*span - pos
	}
	track := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	fill := lipgloss.NewStyle().Foreground(colAqua)
	return track.Render(strings.Repeat("━", pos)) + fill.Render(strings.Repeat("━", block)) + track.Render(strings.Repeat("━", span-pos))
}

func New(cfg config.Config, sess *session.Session, events <-chan argo.Event, cancel context.CancelFunc) Model {
	fi := textinput.New()
	fi.Prompt = ""
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
		filterKind: argo.KindApplication, // apps are the base object, like pods in k9s
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
	cmds := []tea.Cmd{waitEvent(m.events), tick()}
	if m.session != nil {
		sess := m.session
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a, k := sess.Versions(ctx)
			return versionsMsg{argo: a, k8s: k}
		})
	}
	if m.loading != "" {
		cmds = append(cmds, spinTick(), preloadApps(m.session))
	}
	return tea.Batch(cmds...)
}

// preloadApps lists Argo CD apps once so the table fills before the poller's first round.
func preloadApps(sess *session.Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		apps, err := sess.Argo.ListApplications(ctx)
		if err != nil {
			return appsLoadedMsg{err: fmt.Errorf("list applications: %w", err)}
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
			return appsLoadedMsg{err: fmt.Errorf("parsed 0/%d application names (%s)", len(apps), argocd.SampleAppDiag(apps))}
		}
		return appsLoadedMsg{n: named}
	}
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
		m.vp.Height = m.innerHeight()
		if m.mode == viewGraph {
			m.syncGraphViewport()
		}
		return m, nil

	case tickMsg:
		m.refreshList()
		return m, tick()

	case podContainersMsg:
		return m.handlePodContainers(msg)

	case versionsMsg:
		m.argoVersion, m.k8sVersion = msg.argo, msg.k8s
		return m, nil

	case spinMsg:
		if m.loading == "" {
			return m, nil
		}
		m.spin++
		return m, spinTick()

	case appsLoadedMsg:
		m.loading = ""
		m.refreshList()
		switch {
		case msg.err != nil:
			m.status, m.statusErr = msg.err.Error(), true
		case len(m.resources) > 0:
			m.status, m.statusErr = fmt.Sprintf("loaded %d resources (store=%d)", len(m.resources), m.store.Len()), false
		default:
			m.status, m.statusErr = fmt.Sprintf("no resources in view (store=%d) — try :ns all", m.store.Len()), true
		}
		return m, nil

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
			// Argo CD rewrites app status on every reconcile; flashing each event is noise (k9s doesn't).
			m.refreshList()
		}
		return m, waitEvent(m.events)

	case logOutMsg:
		return m, m.handleLogMsg(msg)

	case yamlLoadedMsg:
		m.loading = ""
		m.detailText = msg.text
		m.detailTitle = msg.ref.Kind + "/" + msg.ref.Name
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
		m.loading = ""
		if msg.err != nil {
			m.status = msg.err.Error()
			m.statusErr = true
		} else {
			m.status = msg.msg
			m.statusErr = false
		}
		return m, nil

	case graphReadyMsg:
		m.loading = ""
		if msg.err != nil {
			m.status = "graph: " + msg.err.Error()
			m.statusErr = true
			// Still show whatever tree we have (managed resources without live children).
			m.setGraph(msg.graph)
			if m.mode == viewGraph {
				m.syncGraphViewport()
			}
		} else {
			m.setGraph(msg.graph)
			if m.graphCursor >= len(m.graph.Nodes) {
				m.graphCursor = max(0, len(m.graph.Nodes)-1)
			}
			if m.mode == viewTree {
				m.clampTreeCursor()
			}
			if m.mode == viewGraph || m.mode == viewTree {
				if m.mode == viewGraph {
					m.syncGraphViewport()
				}
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
	if msg.String() == "ctrl+c" { // quits from any view or prompt, like k9s
		m.quitting = true
		m.stopLogs()
		if m.cancel != nil {
			m.cancel()
		}
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
			m.filterIn.SetValue("")
			m.setFilter("")
			return m, nil
		case "enter":
			m.filtering = false
			m.filterIn.Blur()
			m.setFilter(m.filterIn.Value())
			return m, nil
		default:
			var cmd tea.Cmd
			m.filterIn, cmd = m.filterIn.Update(msg)
			m.setFilter(m.filterIn.Value())
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

	if capsLockSuspect(msg.String()) {
		m.status, m.statusErr = "⇪ Caps Lock seems on — argo9s keys are lowercase (j k d y l …)", true
		return m, nil
	}
	if m.mode == viewTree {
		if mm, cmd, ok := m.handleTreeKey(msg); ok {
			return mm, cmd
		}
	}
	if m.mode == viewContainers {
		if mm, cmd, ok := m.handleContainersKey(msg); ok {
			return mm, cmd
		}
	}
	if m.mode == viewGraph && len(m.graph.Nodes) > 0 {
		switch msg.String() {
		case "tab", "shift+tab":
			dir := 1
			if msg.String() == "shift+tab" {
				dir = -1
			}
			if next := nextIssue(m.graph.Nodes, m.graphCursor, dir); next != m.graphCursor {
				m.graphCursor = next
				m.followCursor()
			} else {
				m.status, m.statusErr = "no unhealthy / out-of-sync node", false
			}
			return m, nil
		case "s":
			return m.podAction("shell")
		case "o":
			m.toggleOldRS()
			return m, nil
		case "/":
			m.filtering = true
			m.filterIn.SetValue(m.graphQuery)
			m.filterIn.Focus()
			return m, nil
		case "n", "N":
			if ms := graphMatches(m.graph, m.gl, m.graphQuery); len(ms) > 0 {
				m.graphCursor = cycleMatch(ms, m.graphCursor, msg.String() == "n")
				m.followCursor()
			}
			return m, nil
		case "esc":
			if m.graphQuery != "" { // k9s: esc drops the search before leaving
				m.graphQuery = ""
				m.filterIn.SetValue("")
				return m, nil
			}
		case "pgdown", "ctrl+f", "pgup", "ctrl+b", "home", "end":
			_, vh := m.graphViewport()
			m.graphCursor = graphColumnJump(m.graph, m.gl, m.graphCursor, msg.String(), vh)
			m.followCursor()
			return m, nil
		case "l", "right", "j", "down", "k", "up":
			k := msg.String()
			if (k == "l") && strings.EqualFold(m.graph.Nodes[m.graphCursor].Kind, "Pod") {
				return m.openPodLogs()
			}
			dir := map[string]string{"l": "child", "right": "child", "j": "down", "down": "down", "k": "up", "up": "up"}[k]
			m.graphCursor = graphStep(m.graph, m.gl, m.graphCursor, dir)
			m.followCursor()
			m.statusErr = false // moving on dismisses the last error
			return m, nil
		}
	}
	if m.mode == viewHelp || m.mode == viewDetail || m.mode == viewYAML || m.mode == viewGraph || m.mode == viewLogs || m.mode == viewTree || m.mode == viewContainers {
		switch msg.String() {
		case "q", "esc", "backspace":
			if m.textFilter != "" && msg.String() == "esc" { // k9s: esc clears the filter first
				m.setFilter("")
				return m, nil
			}
			if m.mode == viewLogs {
				m.stopLogs()
			}
			m.popView()
			return m, nil
		case "/":
			if m.mode == viewLogs || m.mode == viewYAML || m.mode == viewDetail {
				m.filtering = true
				m.filterIn.SetValue(m.textFilter)
				m.filterIn.Focus()
			}
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
		case "pgdown", "ctrl+f":
			m.vp.PageDown()
			return m, nil
		case "pgup", "ctrl+b":
			m.vp.PageUp()
			return m, nil
		case "g", "home":
			if m.mode == viewGraph {
				m.graphCursor = 0
				m.syncGraphViewport()
				return m, nil
			}
			m.vp.GotoTop()
			return m, nil
		case "G", "end":
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
				if m.graphCursor == 0 { // the app itself: full describe
					m.showDetail("", m.describeText(m.selected))
				} else {
					n := m.graph.Nodes[m.graphCursor]
					m.showDetail(n.Kind+"/"+n.Name, nodeDescribe(n, m.selected))
				}
				return m, nil
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
		if m.filter != "" && msg.String() == "esc" { // k9s: esc drops the active filter
			m.filterIn.SetValue("")
			m.setFilter("")
			return m, nil
		}
		if m.drill != nil { // then leaves the drill-down
			m.drillBack()
			return m, nil
		}
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
	case "pgdown", "ctrl+f":
		m.cursor = min(max(0, len(m.resources)-1), m.cursor+m.innerHeight()-1)
		return m, nil
	case "pgup", "ctrl+b":
		m.cursor = max(0, m.cursor-(m.innerHeight()-1))
		return m, nil
	case "G", "end":
		if len(m.resources) > 0 {
			m.cursor = len(m.resources) - 1
		}
		return m, nil
	case "g":
		return m.openGraph()
	case "enter":
		r, ok := m.current()
		if !ok {
			return m, nil
		}
		switch r.Kind {
		case argo.KindApplication:
			return m.openTree()
		case argo.KindContext:
			if m.session != nil && r.Name == m.session.Name {
				m.status = "already on " + r.Name
				return m, nil
			}
			m.switchTo = r.Name
			m.quitting = true
			m.stopLogs()
			return m, tea.Quit
		default:
			m.drillInto(r)
		}
		return m, nil
	case "d":
		if r, ok := m.current(); ok {
			m.selected = r
			m.showDetail("", m.describeText(r))
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
			m.detailText, m.detailTitle, m.yamlX = text, "", 0
			m.pushView(viewYAML)
			m.syncYAMLViewport()
			m.vp.GotoTop()
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
	return m, tea.Batch(m.startLoading(fmt.Sprintf("working on %s", r.Name)), fn(r))
}

func (m *Model) pushView(next viewMode) {
	if m.mode == next {
		return
	}
	m.viewStack = append(m.viewStack, m.mode)
	m.mode = next
}

func (m *Model) popView() {
	m.textFilter = ""
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
	case viewTree:
		m.status, m.statusErr = fmt.Sprintf("%s/%s", m.selected.Namespace, m.selected.Name), false
	case viewList:
		m.status, m.statusErr = "", false
	}
}

func (m Model) openGraph() (tea.Model, tea.Cmd) { return m.openApp(viewGraph) }
func (m Model) openTree() (tea.Model, tea.Cmd)  { return m.openApp(viewTree) }

// openApp shows the selected Application's resources as a box graph or a tree,
// then enriches it with live children (rs/pods) asynchronously.
func (m Model) openApp(mode viewMode) (tea.Model, tea.Cmd) {
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
	m.graphFull = argo.Graph{} // new app: don't carry the previous cursor over
	m.setGraph(argo.BuildAppGraph(r))
	m.graphCursor = 0
	if mode == viewGraph && m.width > 0 && m.width < 100 {
		mode = viewTree // boxes are unreadable on narrow terminals; the tree isn't
	}
	m.gox, m.goy = 0, 0
	m.pushView(mode)
	if mode == viewTree {
		m.expanded = map[string]bool{}
		m.clampTreeCursor()
	} else {
		m.syncGraphViewport()
	}
	m.status = fmt.Sprintf("graph: %s/%s — loading pods…", r.Namespace, r.Name)
	m.statusErr = false

	client := m.session
	base := m.graph
	spin := m.startLoading(fmt.Sprintf("loading graph %s/%s (walking clusters)", r.Namespace, r.Name))
	return m, tea.Batch(spin, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		g := base
		if err := client.EnrichGraph(ctx, &g); err != nil {
			return graphReadyMsg{graph: g, err: err}
		}
		return graphReadyMsg{graph: g}
	})
}

// syncGraphViewport relayouts the graph (cheap: autog on a few dozen nodes)
// and scrolls to keep the selected node on screen.
func (m *Model) syncGraphViewport() {
	m.gl = computeGraphLayout(m.graph)
	m.followCursor()
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

// nodeDescribe is the describe of one managed resource of an Application.
func nodeDescribe(n argo.GraphNode, app argo.Resource) string {
	line := func(k, v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf("%-14s%s\n", k+":", v)
	}
	gv := n.Version
	if n.Group != "" {
		gv = n.Group + "/" + n.Version
	}
	return line("Kind", n.Kind) + line("Name", n.Name) + line("Namespace", n.Namespace) +
		line("APIVersion", gv) + line("Health", n.Health) + line("Sync", n.Status) +
		line("Info", n.Meta) + line("Application", app.Namespace+"/"+app.Name) +
		"\n" + "YAML:         y  (live manifest)\n"
}

// showDetail opens the describe view; it goes through syncYAMLViewport so keys
// get the same coloring as YAML.
func (m *Model) showDetail(title, text string) {
	m.detailText, m.detailTitle, m.yamlX, m.textFilter = text, title, 0, ""
	m.pushView(viewDetail)
	m.syncYAMLViewport()
	m.vp.GotoTop()
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
		return m, tea.Batch(m.startLoading("deleting "+r.Name), runOp(func(ctx context.Context) (string, error) {
			if err := m.ops.Delete(ctx, r); err != nil {
				return "", err
			}
			return fmt.Sprintf("deleted %s/%s", r.Kind, r.Name), nil
		}))
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
	m.statusErr = false // a new command starts clean; error branches set it again
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
	case "ctx", "context", "contexts":
		m.drill = nil
		m.filterKind = argo.KindContext
		m.cursor = 0
		m.refreshList()
		m.status = "contexts: enter to switch connection"
		return m, nil
	case "cluster", "clusters", "cl":
		m.drill = nil
		m.filterKind = argo.KindCluster
		m.cursor = 0
		m.refreshList()
		m.status = "clusters: enter to list their apps"
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

// belongsTo reports whether app r is a child of the drilled appset / project / cluster.
func belongsTo(r, parent argo.Resource) bool {
	if r.Kind != argo.KindApplication {
		return false
	}
	switch parent.Kind {
	case argo.KindApplicationSet:
		return r.Owner == parent.Name
	case argo.KindAppProject:
		return r.Project == parent.Name
	case argo.KindCluster:
		return r.Cluster == parent.Name
	}
	return false
}

// clusterRows aggregates Applications per destination cluster.
func clusterRows(apps []argo.Resource) []argo.Resource {
	byName := map[string]*argo.Resource{}
	var order []string
	for _, a := range apps {
		c := orDash(a.Cluster)
		row, ok := byName[c]
		if !ok {
			row = &argo.Resource{Kind: argo.KindCluster, Name: c, Namespace: "-", Project: "-", Sync: "Synced", Health: "Healthy", Owner: "-"}
			byName[c] = row
			order = append(order, c)
		}
		if strings.EqualFold(strings.ReplaceAll(a.Sync, " ", ""), "outofsync") {
			row.Sync = "OutOfSync"
		}
		switch strings.ToLower(a.Health) {
		case "degraded", "missing":
			row.Health = "Degraded"
		case "progressing":
			if row.Health == "Healthy" {
				row.Health = "Progressing"
			}
		}
	}
	out := make([]argo.Resource, 0, len(order))
	for _, c := range order {
		row := *byName[c]
		out = append(out, row)
	}
	return out
}

// contextRows lists configured connections (k9s :ctx).
func (m Model) contextRows() []argo.Resource {
	current := ""
	if m.session != nil {
		current = m.session.Name
	}
	conns := m.cfg.Connections
	if m.session != nil && !slices.ContainsFunc(conns, func(c config.Connection) bool { return c.Name == current }) {
		// --server / --demo sessions aren't in the config file but are still "where you are"
		conns = append([]config.Connection{m.session.Conn}, conns...)
		conns[0].Name = current
	}
	out := make([]argo.Resource, 0, len(conns))
	for _, c := range conns {
		target := c.Server
		if c.Type == config.ConnKubernetes {
			target = orDash(c.Context)
		}
		state := "-"
		if c.Name == current {
			state = "active"
		}
		out = append(out, argo.Resource{Kind: argo.KindContext, Name: c.Name, Namespace: "-", Project: c.Type, Sync: state, Health: "-", Cluster: target, Owner: "-", Revision: "-"})
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// drillInto lists the Applications that belong to r.
func (m *Model) drillInto(r argo.Resource) {
	m.drillPrevKind, m.drillPrevCursor = m.filterKind, m.cursor
	m.drill = &r
	m.filterKind = argo.KindApplication
	m.cursor = 0
	m.refreshList()
	m.status, m.statusErr = fmt.Sprintf("%d apps in %s %s", len(m.resources), r.Kind.Short(), r.Name), false
}

func (m *Model) drillBack() {
	m.filterKind, m.cursor = m.drillPrevKind, m.drillPrevCursor
	m.drill = nil
	m.refreshList()
	m.status, m.statusErr = "", false
}

// describeText adds the child Applications for appsets / projects / clusters.
func (m Model) describeText(r argo.Resource) string {
	text := argo.Describe(r)
	if r.Kind == argo.KindContext {
		return text + fmt.Sprintf("Type:         %s\nTarget:       %s\nActive:       %v\n", r.Project, r.Cluster, r.Sync == "active")
	}
	var kids []argo.Resource
	for _, a := range m.store.List(argo.KindApplication, m.namespace, m.allNS) {
		if belongsTo(a, r) {
			kids = append(kids, a)
		}
	}
	if r.Kind == argo.KindApplication || (len(kids) == 0 && r.Kind != argo.KindApplicationSet) {
		return text
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
	text += fmt.Sprintf("\nApplications (%d):\n", len(kids))
	for _, a := range kids {
		text += fmt.Sprintf("  • %-40s %-10s %-11s %s\n", a.Name, a.Sync, a.Health, orDash(a.Cluster))
	}
	return text
}

// capsLockSuspect: terminals can't report Caps Lock, so an uppercase letter
// that is bound nowhere (J, K, D, Y, …) is the tell. Bound ones keep working.
func capsLockSuspect(key string) bool {
	return len(key) == 1 && key[0] >= 'A' && key[0] <= 'Z' && !strings.Contains(boundUpperKeys, key)
}

// boundUpperKeys: Q quit, R sync, F hard refresh, G bottom, N/A/T/S sort.
const boundUpperKeys = "QRFGNATS"

// filterMatcher follows k9s "/" semantics: case-insensitive regex, plain
// substring if the regex is invalid, leading "!" inverts. nil = no filter.
func filterMatcher(f string) func(string) bool {
	inverse := strings.HasPrefix(f, "!")
	f = strings.TrimPrefix(f, "!")
	if f == "" {
		return nil
	}
	match := func(s string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(f)) }
	if re, err := regexp.Compile("(?i)" + f); err == nil {
		match = re.MatchString
	}
	return func(s string) bool { return match(s) != inverse }
}

func filterLines(lines []string, f string) []string {
	match := filterMatcher(f)
	if match == nil {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if match(l) {
			out = append(out, l)
		}
	}
	return out
}

// setFilter routes "/" input to the list or to the open text view.
func (m *Model) setFilter(v string) {
	switch m.mode {
	case viewLogs:
		m.textFilter = v
		m.syncLogViewport()
	case viewYAML, viewDetail:
		m.textFilter = v
		m.syncYAMLViewport()
		m.vp.GotoTop()
	case viewGraph:
		m.graphQuery = v
		if ms := graphMatches(m.graph, m.gl, v); len(ms) > 0 {
			m.graphCursor = ms[0]
			m.followCursor()
		}
	default:
		m.filter = v
		m.refreshList()
	}
}

func (m *Model) refreshList() {
	// Argo API lists apps across namespaces; never filter to local kube ns.
	if m.session != nil && m.session.Type == config.ConnArgoCD {
		m.allNS = true
		m.namespace = "all"
	}
	var items []argo.Resource
	switch m.filterKind {
	case argo.KindCluster:
		items = clusterRows(m.store.List(argo.KindApplication, m.namespace, m.allNS))
	case argo.KindContext:
		items = m.contextRows()
	default:
		items = m.store.List(m.filterKind, m.namespace, m.allNS)
	}
	if m.drill != nil {
		filtered := items[:0:0]
		for _, r := range items {
			if belongsTo(r, *m.drill) {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}
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
	if match := filterMatcher(m.filter); match != nil {
		filtered := make([]argo.Resource, 0, len(items))
		for _, r := range items {
			hay := r.Name + " " + r.Namespace + " " + string(r.Kind) + " " + r.Project + " " + r.Sync + " " + r.Health + " " + r.Message + " " + r.ConditionSummary()
			if match(hay) {
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
	if m.mode == viewGraph {
		return m.renderGraphScreen()
	}

	header := m.renderHeader()
	footer := m.renderFooter()
	var prompt string
	switch {
	case m.filtering:
		prompt = promptBox("/"+m.filterIn.View(), lipgloss.Color("114"), m.width)
	case m.commanding:
		prompt = promptBox(m.cmdIn.View(), lipgloss.Color("51"), m.width)
	}
	innerH := m.innerHeight()
	frameH := innerH + 2
	m.vp.Height = innerH

	var title, body string
	switch m.mode {
	case viewContainers:
		pod := ""
		if m.graphCursor >= 0 && m.graphCursor < len(m.graph.Nodes) {
			pod = m.graph.Nodes[m.graphCursor].Name
		}
		title, body = titleOf("containers", pod, len(m.containers), ""), m.renderContainers()
	case viewTree:
		title, body = titleOf("resources", m.selected.Namespace+"/"+m.selected.Name, len(treeRows(m.graph.Nodes, m.expanded)), ""), m.renderTree(innerH-1)
	case viewGraph:
		title, body = titleOf("graph", m.selected.Namespace+"/"+m.selected.Name, len(m.graph.Nodes), ""), m.vp.View()
	case viewLogs:
		what := m.logNS + "/" + m.logPod
		if m.podContainer != "" {
			what += ":" + m.podContainer
		}
		title, body = titleOf("logs", what, -1, m.textFilter), m.vp.View()
	case viewYAML:
		what := string(m.selected.Kind) + "/" + m.selected.Name
		if m.detailTitle != "" {
			what = m.detailTitle
		}
		title, body = titleOf("yaml", what, -1, m.textFilter), m.vp.View()
	case viewDetail:
		what := string(m.selected.Kind) + "/" + m.selected.Name
		if m.detailTitle != "" {
			what = m.detailTitle
		}
		title, body = titleOf("describe", what, -1, m.textFilter), m.vp.View()
	case viewHelp:
		title, body = titleOf("help", "", -1, ""), m.vp.View()
	case viewConfirm:
		title = titleOf("confirm", "", -1, "")
		body = "\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("204")).Bold(true).Render(
			fmt.Sprintf("Delete %s/%s/%s? [y/n]", m.confirmTarget.Kind, m.confirmTarget.Namespace, m.confirmTarget.Name),
		)
	default:
		ns := m.namespace
		if m.allNS {
			ns = "all"
		}
		kind := "all"
		if m.filterKind != argo.KindAll {
			kind = m.filterKind.Plural()
		}
		scope := ns
		if m.drill != nil {
			scope = m.drill.Kind.Short() + ":" + m.drill.Name
		}
		if m.filterKind == argo.KindContext {
			scope = ""
		}
		title = titleOf(kind, scope, len(m.resources), m.filter)
		inner := m
		inner.width = max(20, m.width-2)
		body = inner.renderTable(innerH - 1) // -1 for the column header row
	}

	parts := []string{header}
	if prompt != "" {
		parts = append(parts, prompt)
	}
	parts = append(parts, frame(title, body, m.width, frameH), footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// innerHeight is the row count inside the body frame: the frame fills whatever the
// header, prompt and footer (2 lines) leave, so the footer stays pinned to the bottom.
func (m Model) innerHeight() int {
	used := lipgloss.Height(m.renderHeader()) + 2 + 2 // footer + frame borders
	if m.filtering || m.commanding {
		used += 3
	}
	return max(3, m.height-used)
}

// k9s default skin colors (256-color approximations).
var (
	colOrange  = lipgloss.Color("214")
	colBlue    = lipgloss.Color("33")
	colAqua    = lipgloss.Color("51")
	colFuchsia = lipgloss.Color("201")

	labelStyle   = lipgloss.NewStyle().Foreground(colOrange).Bold(true)
	valueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true)
	keyStyle     = lipgloss.NewStyle().Foreground(colBlue).Bold(true)
	keyDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)

// titleOf builds a k9s table title: " kind(scope)[count] </filter> ".
func titleOf(kind, scope string, count int, filter string) string {
	s := lipgloss.NewStyle().Foreground(colAqua).Bold(true).Render(kind)
	if scope != "" {
		s += lipgloss.NewStyle().Foreground(colAqua).Render("(") + lipgloss.NewStyle().Foreground(colFuchsia).Bold(true).Render(scope) + lipgloss.NewStyle().Foreground(colAqua).Render(")")
	}
	if count >= 0 {
		s += valueStyle.Render(fmt.Sprintf("[%d]", count))
	}
	if filter != "" {
		s += " " + lipgloss.NewStyle().Foreground(colAqua).Render("</") + lipgloss.NewStyle().Foreground(colFuchsia).Render(filter) + lipgloss.NewStyle().Foreground(colAqua).Render(">")
	}
	return " " + s + " "
}

// frame draws a single-line box with a centered title in the top border, like k9s.
func frame(title, body string, width, height int) string {
	if width <= 0 {
		width = 80
	}
	innerW, innerH := max(1, width-2), max(1, height-2)
	bc := lipgloss.NewStyle().Foreground(colBlue)
	tw := lipgloss.Width(title)
	if tw > innerW {
		title, tw = "", 0
	}
	left := (innerW - tw) / 2
	lines := []string{bc.Render("┌"+strings.Repeat("─", left)) + title + bc.Render(strings.Repeat("─", innerW-tw-left)+"┐")}
	bodyLines := strings.Split(body, "\n")
	clip := lipgloss.NewStyle().MaxWidth(innerW)
	for i := 0; i < innerH; i++ {
		l := ""
		if i < len(bodyLines) {
			l = clip.Render(bodyLines[i])
		}
		lines = append(lines, bc.Render("│")+l+strings.Repeat(" ", max(0, innerW-lipgloss.Width(l)))+bc.Render("│"))
	}
	lines = append(lines, bc.Render("└"+strings.Repeat("─", innerW)+"┘"))
	return strings.Join(lines, "\n")
}

func promptBox(content string, color lipgloss.Color, width int) string {
	if width <= 0 {
		width = 80
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(color).Width(max(1, width-2)).Render(content)
}

func (m Model) renderHeader() string {
	ro, roColor := "RW", lipgloss.Color("114")
	if m.ops.ReadOnly() {
		ro, roColor = "RO", colOrange
	}
	ns := m.namespace
	if m.allNS {
		ns = "all"
	}
	connType := "kubernetes"
	if m.session != nil {
		connType = m.session.Type
	}
	counts := m.countByKind()
	kv := func(label, value string) string {
		return labelStyle.Render(fmt.Sprintf("%-12s", label+":")) + value
	}
	info := []string{
		kv("Context", valueStyle.Render(m.context)+" "+lipgloss.NewStyle().Foreground(roColor).Bold(true).Render("["+ro+"]")),
		kv("Type", valueStyle.Render(connType)),
		kv("Namespace", valueStyle.Render(ns)),
		kv("Argo CD", valueStyle.Render(orDash(m.argoVersion))),
	}
	if m.k8sVersion != "" {
		info = append(info, kv("K8s", valueStyle.Render(m.k8sVersion)))
	}
	info = append(info,
		kv("argo9s", valueStyle.Render(appVersion)),
		kv("Apps", valueStyle.Render(fmt.Sprint(counts[argo.KindApplication]))+
			labelStyle.Render("  AppSets ")+valueStyle.Render(fmt.Sprint(counts[argo.KindApplicationSet]))+
			labelStyle.Render("  Projects ")+valueStyle.Render(fmt.Sprint(counts[argo.KindAppProject]))),
	)
	if m.projectFilter != "" {
		info = append(info, kv("Project", valueStyle.Render(m.projectFilter)))
	}
	if m.nameFilter != "" {
		info = append(info, kv("Name", valueStyle.Render(m.nameFilter)))
	}
	if m.syncFilter != "" {
		info = append(info, kv("Sync", valueStyle.Render(m.syncFilter)))
	}
	left := strings.Join(info, "\n")

	subtitle := "" // versions live in the info block (argo9s / Argo CD / K8s), like k9s
	logo := renderLogoCompact(subtitle)
	if m.width >= 100 {
		logo = renderLogo(subtitle)
	}
	if m.width <= 0 {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", logo)
	}
	if lipgloss.Width(left)+lipgloss.Width(logo)+4 > m.width {
		return lipgloss.JoinVertical(lipgloss.Left, left, logo)
	}
	mid := m.width - lipgloss.Width(left) - lipgloss.Width(logo) - 4
	menu := renderMenu(m.keyHints(), mid, 6)
	gap := strings.Repeat(" ", max(2, mid-lipgloss.Width(menu)+2))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", menu, gap, logo)
}

func (m Model) countByKind() map[argo.Kind]int {
	out := map[argo.Kind]int{}
	for _, r := range m.store.List(argo.KindAll, m.namespace, m.allNS) {
		out[r.Kind]++
	}
	return out
}

func (m Model) keyHints() [][2]string {
	switch m.mode {
	case viewLogs:
		return [][2]string{{"/", "Filter"}, {"h/l", "Scroll"}, {"p", "Previous"}, {"esc", "Stop"}}
	case viewYAML:
		return [][2]string{{"/", "Filter"}, {"h/l", "Scroll"}, {"e", "Edit"}, {"esc", "Back"}}
	case viewDetail:
		return [][2]string{{"/", "Filter"}, {"esc", "Back"}}
	case viewTree:
		return [][2]string{{"enter", "Expand/Collapse"}, {"←/→", "Collapse/Expand"}, {"d", "Describe"}, {"y", "YAML"}, {"e", "Edit"}, {"l", "Logs (pod)"}, {"s", "Shell (pod)"}, {"o", "Old RS"}, {"esc", "Back"}}
	case viewContainers:
		return [][2]string{{"enter", "Open"}, {"l", "Logs"}, {"s", "Shell"}, {"j/k", "Move"}, {"esc", "Back"}}
	case viewGraph:
		return [][2]string{{"/", "Search"}, {"n/N", "Next match"}, {"h/l", "Parent/Child"}, {"j/k", "Siblings"}, {"pgup/pgdn", "Page"}, {"tab", "Next issue"}, {"o", "Old RS"}, {"d", "Describe"}, {"y", "YAML"}, {"e", "Edit"}, {"l", "Logs (pod)"}, {"s", "Shell (pod)"}, {"esc", "Back"}}
	case viewHelp, viewConfirm:
		return [][2]string{{"esc", "Back"}}
	}
	return [][2]string{
		{"enter", "View"}, {"d", "Describe"}, {"y", "YAML"}, {"g", "Graph"}, {"/", "Filter"}, {":", "Command"},
		{"R", "Sync"}, {"f", "Refresh"}, {"F", "Hard Refresh"}, {"s", "Suspend"}, {"r", "Resume"}, {"ctrl-d", "Delete"},
		{"shift-n", "Sort Name"}, {"shift-a", "Sort Age"}, {"shift-s", "Sort Status"}, {"shift-t", "Sort Type"}, {"?", "Help"},
		{":ctx", "Contexts"}, {":clusters", "Clusters"}, {":appsets", "AppSets"}, {":projects", "Projects"},
	}
}

// renderMenu lays "<key> Desc" hints out in columns of `rows`, k9s-style,
// dropping whole columns that don't fit in width.
func renderMenu(keys [][2]string, width, rows int) string {
	var cols []string
	used := 0
	for i := 0; i < len(keys); i += rows {
		chunk := keys[i:min(i+rows, len(keys))]
		kw, dw := 0, 0
		for _, k := range chunk {
			kw, dw = max(kw, lipgloss.Width(k[0])+2), max(dw, lipgloss.Width(k[1]))
		}
		colW := kw + 1 + dw
		if used+colW+3 > width {
			break
		}
		used += colW + 3
		lines := make([]string, len(chunk))
		for j, k := range chunk {
			key := "<" + k[0] + ">"
			lines[j] = keyStyle.Render(key) + strings.Repeat(" ", kw-lipgloss.Width(key)+1) + keyDescStyle.Render(fmt.Sprintf("%-*s", dw, k[1]))
		}
		cols = append(cols, strings.Join(lines, "\n"), "   ")
	}
	if len(cols) == 0 {
		return ""
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols[:len(cols)-1]...)
}

// renderFooter: k9s crumbs (view stack) + flash/status line.
func (m Model) renderFooter() string {
	names := map[viewMode]string{viewList: "apps", viewDetail: "describe", viewYAML: "yaml", viewGraph: "graph", viewHelp: "help", viewConfirm: "confirm", viewLogs: "logs", viewTree: "resources", viewContainers: "containers"}
	crumb := lipgloss.NewStyle().Background(lipgloss.Color("33")).Foreground(lipgloss.Color("16")).Padding(0, 1)
	listName := "all"
	if m.filterKind != argo.KindAll {
		listName = m.filterKind.Plural()
	}
	names[viewList] = listName
	var crumbs []string
	if m.drill != nil {
		crumbs = append(crumbs, crumb.Render("<"+m.drill.Kind.Plural()+">"))
	}
	for _, v := range m.viewStack {
		crumbs = append(crumbs, crumb.Render("<"+names[v]+">"))
	}
	crumbs = append(crumbs, crumb.Background(colOrange).Bold(true).Render("<"+names[m.mode]+">"))

	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	if m.statusErr {
		statusStyle = statusStyle.Foreground(lipgloss.Color("196"))
	}
	status := statusStyle.Render(truncate(m.status, max(10, m.width)))
	if m.loading != "" {
		elapsed := time.Since(m.loadStart).Truncate(time.Second)
		status = progressBar(m.spin, 24) + " " + lipgloss.NewStyle().Foreground(colAqua).Render(fmt.Sprintf("%s… %s", m.loading, elapsed))
	}
	return strings.Join(crumbs, " ") + "\n" + lipgloss.PlaceHorizontal(max(1, m.width), lipgloss.Center, status)
}

func (m Model) renderTable(maxRows int) string {
	if len(m.resources) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("  no resources (is Argo CD installed? try :ns all)")
	}

	cols := layoutColumns(m.columnsFor(), m.width)
	header := formatRow(cols, nil)
	lines := []string{lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true).Render(header)}

	maxRows = max(1, maxRows)
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := min(len(m.resources), start+maxRows)

	for i := start; i < end; i++ {
		r := m.resources[i]
		line := formatRow(cols, &r)
		if r.Suspended {
			line += " ‖" // not ⏸: emoji width varies between terminals and shifts the frame
		}
		if i == m.cursor {
			if r.HasConditions() {
				line += " !"
			}
			if m.width > 0 {
				line = padTruncate(line, m.width)
			}
			lines = append(lines, lipgloss.NewStyle().Background(lipgloss.Color("117")).Foreground(lipgloss.Color("16")).Bold(true).Render(line))
			continue
		}
		line = lipgloss.NewStyle().Foreground(rowColor(r)).Render(line)
		if r.HasConditions() {
			line += lipgloss.NewStyle().Foreground(conditionColor(r)).Bold(true).Render(" !")
		}
		lines = append(lines, line)
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
	// Like the Argo CD UI: the row follows sync/health; conditions only color the "!" marker.
	return healthColor(r.Health, r.Sync)
}

// conditionColor: red when any condition is an error, orange for warnings.
func conditionColor(r argo.Resource) lipgloss.Color {
	for _, c := range r.Conditions {
		if strings.Contains(strings.ToLower(c.Type), "error") {
			return lipgloss.Color("196")
		}
	}
	return lipgloss.Color("214")
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
argo9s — a K9s-style terminal UI for Argo CD

LISTS (apps, appsets, projects, clusters, contexts)
  j/k ↑/↓          move            pgup/pgdn ctrl-f/b   page
  home / end       first / last    esc                  clear filter, then go back
  enter            app → managed resources tree
                   appset / project / cluster → its applications
                   context → switch connection
  d                describe        y                    YAML
  g                app → fullscreen resource graph
  /text            filter (regex, case-insensitive; !text inverts)
  N A T S          sort by name / age / type / status

RESOURCE TREE (enter on an app)
  enter/space      expand / collapse    →/l   expand    ←/h  collapse / parent
  d y e            describe / YAML / edit the object
  l s              pod logs / shell (asks for the container if several)
  o                show / hide old ReplicaSets (0/0)

RESOURCE GRAPH (g on an app — fullscreen)
  h/l              parent / child (follows the drawing)
  j/k              up / down in the column   pgup/pgdn home/end  jump in column
  /text  n/N       search objects, next / previous match
  tab / shift-tab  next / previous unhealthy or out-of-sync object
  d y e l s o      describe / YAML / edit / logs / shell / old ReplicaSets

LOGS · YAML · DESCRIBE
  /text            keep matching lines only    esc  clear, then back
  h/l ←/→  0       horizontal scroll / reset
  p                logs: previous container (after a restart)
  e                YAML: edit in $EDITOR and apply (read-write connections)

ACTIONS on applications (read-write connections only)
  R                sync            f / F    refresh / hard refresh
  s / r            suspend / resume auto-sync
  ctrl+d           delete (asks to confirm)

COMMANDS
  :apps :appsets :projects :all      switch view
  :clusters                          destination clusters
  :ctx                               configured connections
  :project <name> | clear            :p <name>
  :name <text> | clear               :n <text>
  :sync outofsync | synced | clear   :oos
  :ns <namespace|all>
  :graph   :readonly   :nocolor   :q

ctrl+c quits from anywhere. Keys are lowercase: a warning shows when Caps Lock seems on.
`)
}

// Run starts the TUI for an opened session. It returns the connection name picked
// in :ctx ("" when the user just quit), so the caller can reopen and rerun.
func Run(sess *session.Session) (string, error) {
	cfg := sess.Cfg
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ApplyColorMode(cfg.NoColor, sess.Type)

	events := make(chan argo.Event, 256)
	go func() {
		_ = sess.StartWatch(ctx, events)
		close(events)
	}()

	m := New(cfg, sess, events, cancel)
	m.refreshList()
	if sess.Type == config.ConnArgoCD && sess.Argo != nil {
		// Listed inside the TUI (Init) so the user sees progress instead of a frozen terminal.
		m.loading, m.loadStart = "listing applications from Argo CD", time.Now()
	} else if n := len(m.resources); n > 0 {
		m.status = fmt.Sprintf("loaded %d resources (store=%d)", n, sess.Store.Len())
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if fm, ok := final.(Model); ok && err == nil {
		return fm.switchTo, nil
	}
	return "", err
}
