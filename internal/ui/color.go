package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vvrnv/argo9s/internal/session"
)

// ApplyColorMode configures lipgloss for the process.
// noColor wins over demo/truecolor forcing.
func ApplyColorMode(noColor bool, sessType string) {
	if noColor || envNoColor() {
		lipgloss.SetColorProfile(termenv.Ascii)
		return
	}
	// VHS/ttyd often reports no color capability; force truecolor for demos.
	if sessType == session.TypeDemo || os.Getenv("CLICOLOR_FORCE") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
}

func envNoColor() bool {
	// https://no-color.org/ — any value disables color.
	v, ok := os.LookupEnv("NO_COLOR")
	return ok && strings.TrimSpace(v) != ""
}
