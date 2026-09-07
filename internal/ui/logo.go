package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// FIGlet "standard" logo — same header-art style as k9s-like TUIs.
// Generated with: figlet -f standard argo9s
var logoLines = []string{
	"                       ___      ",
	"  __ _ _ __ __ _  ___ / _ \\ ___ ",
	" / _` | '__/ _` |/ _ \\ (_) / __|",
	"| (_| | | | (_| | (_) \\__, \\__ \\",
	" \\__,_|_|  \\__, |\\___/  /_/|___/",
	"           |___/                ",
}

func renderLogo(subtitle string) string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	sub := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	lines := make([]string, 0, len(logoLines)+1)
	for _, l := range logoLines {
		lines = append(lines, style.Render(l))
	}
	if subtitle != "" {
		// Version line centered under the glyph block.
		pad := max(0, (len(logoLines[0])-len(subtitle))/2)
		lines = append(lines, sub.Render(strings.Repeat(" ", pad)+subtitle))
	}
	return strings.Join(lines, "\n")
}

func renderLogoCompact(subtitle string) string {
	brand := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render("argo9s")
	if subtitle == "" {
		return brand
	}
	return brand + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(subtitle)
}

func logoSubtitle(appVersion, argoHint string) string {
	if argoHint != "" {
		return fmt.Sprintf("%s  %s", argoHint, appVersion)
	}
	return appVersion
}
