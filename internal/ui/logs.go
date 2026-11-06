package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vvrnv/argo9s/internal/argo"
	"github.com/vvrnv/argo9s/internal/session"
)

const maxLogLines = 8000

type logOutMsg struct {
	line string
	err  error
	done bool
}

type yamlLoadedMsg struct {
	text string
	ref  session.ResourceRef
	edit bool // open editor after load
}

type editDoneMsg struct {
	path string
	err  error
}

func (m *Model) stopLogs() {
	if m.logCancel != nil {
		m.logCancel()
		m.logCancel = nil
	}
	m.logCh = nil
}

func (m Model) openPodLogs() (tea.Model, tea.Cmd) {
	return m.podAction("logs") // picks the container first when the pod has several
}

func (m Model) startPodLogs(previous bool) (tea.Model, tea.Cmd) {
	if m.session == nil {
		m.status = "no session"
		m.statusErr = true
		return m, nil
	}
	if m.graphCursor < 0 || m.graphCursor >= len(m.graph.Nodes) {
		return m, nil
	}
	n := m.graph.Nodes[m.graphCursor]
	if !strings.EqualFold(n.Kind, "Pod") {
		m.status = "logs: select a Pod node (l on pod)"
		m.statusErr = true
		return m, nil
	}
	if m.selected.Kind != argo.KindApplication {
		m.status = "logs require an Application graph"
		m.statusErr = true
		return m, nil
	}

	m.stopLogs()
	ctx, cancel := context.WithCancel(context.Background())
	m.logCancel = cancel
	m.logPod = n.Name
	m.logNS = n.Namespace
	m.logLines = nil
	m.logFollow = !previous
	m.logPrevious = previous
	m.logX = 0
	m.logErr = ""

	ch := make(chan logOutMsg, 256)
	m.logCh = ch
	app := m.selected
	podNS, podName := n.Namespace, n.Name
	sess := m.session
	follow := !previous

	go func() {
		err := sess.StreamPodLogs(ctx, app, podNS, podName, m.podContainer, follow, previous, 200, func(line string) error {
			select {
			case ch <- logOutMsg{line: line}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		select {
		case ch <- logOutMsg{done: true, err: err}:
		case <-ctx.Done():
		}
	}()

	mode := "follow"
	if previous {
		mode = "previous"
	}
	m.vp.SetContent(fmt.Sprintf("streaming logs %s/%s (%s) …", podNS, podName, mode))
	m.vp.GotoTop()
	if m.mode != viewLogs {
		m.pushView(viewLogs)
	}
	m.status = fmt.Sprintf("logs: %s/%s (%s) — h/l scroll · p previous · esc stop", podNS, podName, mode)
	m.statusErr = false
	return m, waitLogMsg(ch)
}

func waitLogMsg(ch <-chan logOutMsg) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return logOutMsg{done: true}
		}
		msg, ok := <-ch
		if !ok {
			return logOutMsg{done: true}
		}
		return msg
	}
}

func (m *Model) appendLogLine(line string) {
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > maxLogLines {
		m.logLines = m.logLines[len(m.logLines)-maxLogLines:]
	}
	m.syncLogViewport()
}

func (m *Model) syncLogViewport() {
	m.vp.SetContent(shiftLines(filterLines(m.logLines, m.textFilter), m.logX))
	if m.logFollow && !m.logPrevious {
		m.vp.GotoBottom()
	}
}

func (m *Model) syncYAMLViewport() {
	lines := strings.Split(m.detailText, "\n")
	shown := strings.Split(shiftLines(filterLines(lines, m.textFilter), m.yamlX), "\n")
	for i, l := range shown {
		shown[i] = highlightYAML(l)
	}
	m.vp.SetContent(strings.Join(shown, "\n"))
}

func shiftLines(lines []string, xOff int) string {
	if xOff < 0 {
		xOff = 0
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		r := []rune(line)
		if xOff >= len(r) {
			out[i] = ""
			continue
		}
		out[i] = string(r[xOff:])
	}
	return strings.Join(out, "\n")
}

func (m *Model) handleLogMsg(msg logOutMsg) tea.Cmd {
	if msg.done {
		m.logCh = nil
		if msg.err != nil && msg.err != context.Canceled && !strings.Contains(msg.err.Error(), "context canceled") {
			m.logErr = msg.err.Error()
			m.status = "logs: " + m.logErr
			m.statusErr = true
			if len(m.logLines) == 0 {
				m.vp.SetContent("log error: " + m.logErr)
			}
		} else {
			tag := "current"
			if m.logPrevious {
				tag = "previous"
			}
			m.status = fmt.Sprintf("logs: %s/%s (%s) — ended · p toggle previous", m.logNS, m.logPod, tag)
			m.statusErr = false
		}
		return nil
	}
	m.appendLogLine(msg.line)
	tag := "follow"
	if m.logPrevious {
		tag = "previous"
	}
	m.status = fmt.Sprintf("logs: %s/%s (%s, %d lines, x=%d)", m.logNS, m.logPod, tag, len(m.logLines), m.logX)
	m.statusErr = false
	return waitLogMsg(m.logCh)
}

func (m Model) openGraphNodeYAML(editAfter bool) (tea.Model, tea.Cmd) {
	if m.session == nil || m.graphCursor < 0 || m.graphCursor >= len(m.graph.Nodes) {
		return m, nil
	}
	n := m.graph.Nodes[m.graphCursor]
	if n.IsGroup || n.Name == "" {
		m.status = "yaml: select a concrete resource node"
		m.statusErr = true
		return m, nil
	}
	if editAfter && m.ops.ReadOnly() {
		m.status = "read-only — set readOnly: false on this connection to edit"
		m.statusErr = true
		return m, nil
	}
	ref := session.ResourceRef{
		Group: n.Group, Version: n.Version, Kind: n.Kind,
		Namespace: n.Namespace, Name: n.Name,
	}
	m.yamlRef = ref
	m.yamlX = 0
	m.status = fmt.Sprintf("loading yaml %s/%s…", n.Kind, n.Name)
	m.statusErr = false
	app := m.selected
	sess := m.session
	spin := m.startLoading(fmt.Sprintf("loading yaml %s/%s", n.Kind, n.Name))
	return m, tea.Batch(spin, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := sess.GetResourceYAML(ctx, app, ref)
		if err != nil {
			return opDoneMsg{err: err}
		}
		return yamlLoadedMsg{text: text, ref: ref, edit: editAfter}
	})
}

func (m Model) launchYAMLEditor(content string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	name := fmt.Sprintf("argo9s-%s-%s.yaml", m.yamlRef.Kind, m.yamlRef.Name)
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return func() tea.Msg { return editDoneMsg{err: err} }
	}
	c := exec.Command(editor, path) //nolint:gosec // intentional user EDITOR
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editDoneMsg{path: path, err: err}
	})
}

func (m Model) applyEditedYAML(path string) tea.Cmd {
	data, err := os.ReadFile(path)
	_ = os.Remove(path)
	if err != nil {
		return func() tea.Msg { return opDoneMsg{err: err} }
	}
	text := string(data)
	if text == m.detailText {
		return func() tea.Msg { return opDoneMsg{msg: "yaml unchanged"} }
	}
	app := m.selected
	ref := m.yamlRef
	sess := m.session
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := sess.ApplyResourceYAML(ctx, app, ref, text); err != nil {
			return opDoneMsg{err: err}
		}
		return yamlLoadedMsg{text: text, ref: ref, edit: false}
	}
}
