package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/session"
)

// Pod actions (logs / shell) need a container. Multi-container pods open a
// picker (k9s-style); single-container pods go straight to the action.

type podContainersMsg struct {
	action     string // "logs" | "shell"
	containers []session.ContainerInfo
	err        error
}

// podAction starts logs or shell on the pod under graphCursor.
func (m Model) podAction(action string) (tea.Model, tea.Cmd) {
	if m.session == nil || m.graphCursor < 0 || m.graphCursor >= len(m.graph.Nodes) {
		return m, nil
	}
	n := m.graph.Nodes[m.graphCursor]
	if !strings.EqualFold(n.Kind, "Pod") {
		m.status, m.statusErr = action+": select a Pod", true
		return m, nil
	}
	sess, app, ns, pod := m.session, m.selected, n.Namespace, n.Name
	spin := m.startLoading(fmt.Sprintf("reading containers of %s", pod))
	return m, tea.Batch(spin, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cs, err := sess.PodContainers(ctx, app, ns, pod)
		return podContainersMsg{action: action, containers: cs, err: err}
	})
}

func (m Model) handlePodContainers(msg podContainersMsg) (tea.Model, tea.Cmd) {
	m.loading = ""
	if msg.err != nil {
		m.status, m.statusErr = msg.err.Error(), true
		return m, nil
	}
	if len(msg.containers) == 1 {
		return m.runPodAction(msg.action, msg.containers[0].Name)
	}
	m.containers, m.containerCursor, m.containerAction = msg.containers, 0, msg.action
	m.pushView(viewContainers)
	m.status, m.statusErr = fmt.Sprintf("%d containers — enter %s · l logs · s shell", len(msg.containers), msg.action), false
	return m, nil
}

func (m Model) runPodAction(action, container string) (tea.Model, tea.Cmd) {
	m.podContainer = container
	if action == "logs" {
		return m.startPodLogs(false)
	}
	n := m.graph.Nodes[m.graphCursor]
	sh, err := m.session.PodShell(context.Background(), m.selected, n.Namespace, n.Name, container)
	if err != nil {
		m.status, m.statusErr = err.Error(), true
		return m, nil
	}
	m.status, m.statusErr = fmt.Sprintf("shell %s/%s [%s] — exit to come back", n.Namespace, n.Name, container), false
	return m, tea.Exec(sh, func(err error) tea.Msg {
		if err != nil {
			return opDoneMsg{err: fmt.Errorf("shell: %w", err)}
		}
		return opDoneMsg{msg: "shell closed"}
	})
}

func (m Model) handleContainersKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "j", "down":
		m.containerCursor = min(len(m.containers)-1, m.containerCursor+1)
	case "k", "up":
		m.containerCursor = max(0, m.containerCursor-1)
	case "enter", "l", "s":
		if len(m.containers) == 0 {
			break
		}
		action := m.containerAction
		switch msg.String() {
		case "l":
			action = "logs"
		case "s":
			action = "shell"
		}
		mm, cmd := m.runPodAction(action, m.containers[m.containerCursor].Name)
		return mm, cmd, true
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m Model) renderContainers() string {
	w := max(40, m.width-2)
	const stateW, readyW, restartsW = 18, 6, 9
	nameW := 24
	imageW := max(10, w-nameW-stateW-readyW-restartsW-6)
	row := func(name, image, state, ready, restarts string) string {
		return "  " + padTruncate(name, nameW) + " " + padTruncate(image, imageW) + " " +
			padTruncate(state, stateW) + " " + padTruncate(ready, readyW) + " " + padTruncate(restarts, restartsW)
	}
	lines := []string{lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true).Render(row("NAME", "IMAGE", "STATE", "READY", "RESTARTS"))}
	for i, c := range m.containers {
		line := row(c.Name, c.Image, c.State, fmt.Sprint(c.Ready), fmt.Sprint(c.Restarts))
		health := "Healthy"
		if !c.Ready {
			health = "Degraded"
		}
		style := lipgloss.NewStyle().Foreground(healthColor(health, ""))
		if i == m.containerCursor {
			style = lipgloss.NewStyle().Background(lipgloss.Color("117")).Foreground(lipgloss.Color("16")).Bold(true)
			line = padTruncate(line, w)
		}
		lines = append(lines, style.Render(line))
	}
	return strings.Join(lines, "\n")
}
