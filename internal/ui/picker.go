package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/config"
)

type ConnectionPicker struct {
	connections []config.Connection
	cursor      int
	width       int
	height      int
	err         string
	chosen      *config.Connection
	quitting    bool
}

func NewConnectionPicker(conns []config.Connection, defaultName string) ConnectionPicker {
	cursor := 0
	for i, c := range conns {
		if strings.EqualFold(c.Name, defaultName) {
			cursor = i
			break
		}
	}
	return ConnectionPicker{connections: conns, cursor: cursor}
}

func (m ConnectionPicker) Init() tea.Cmd { return nil }

func (m ConnectionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.connections)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter", " ":
			if len(m.connections) > 0 {
				c := m.connections[m.cursor]
				m.chosen = &c
				return m, tea.Quit
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(msg.String()[0] - '1')
			if idx >= 0 && idx < len(m.connections) {
				c := m.connections[idx]
				m.chosen = &c
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m ConnectionPicker) View() string {
	if m.quitting && m.chosen == nil {
		return ""
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("argo9s — select connection")
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("j/k move · enter select · 1-9 shortcut · q quit")
	var b strings.Builder
	b.WriteString(title + "\n\n")
	for i, c := range m.connections {
		label := fmt.Sprintf("%d  %s", i+1, c.Name)
		detail := c.Type
		if c.Type == config.ConnArgoCD {
			detail = "argocd  " + c.Server
			if c.Insecure {
				detail += "  (insecure)"
			}
		} else {
			detail = "kubernetes"
			if c.Context != "" {
				detail += "  ctx:" + c.Context
			} else {
				detail += "  (kubeconfig / in-cluster)"
			}
		}
		line := fmt.Sprintf("  %-18s  %s", label, detail)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.cursor {
			style = style.Background(lipgloss.Color("238")).Foreground(lipgloss.Color("255")).Bold(true)
			line = "> " + strings.TrimPrefix(line, "  ")
		} else {
			line = "  " + strings.TrimPrefix(line, "  ")
		}
		b.WriteString(style.Render(line) + "\n")
	}
	if m.err != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.err))
	}
	b.WriteString("\n" + hint)
	return b.String()
}

// PickConnection runs a small TUI to choose a connection. Returns nil if cancelled.
func PickConnection(conns []config.Connection, defaultName string) (*config.Connection, error) {
	if len(conns) == 0 {
		return nil, fmt.Errorf("no connections configured")
	}
	if len(conns) == 1 {
		c := conns[0]
		return &c, nil
	}
	m := NewConnectionPicker(conns, defaultName)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	pm, ok := final.(ConnectionPicker)
	if !ok || pm.chosen == nil {
		return nil, fmt.Errorf("connection selection cancelled")
	}
	return pm.chosen, nil
}
