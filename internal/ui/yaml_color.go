package ui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// k9s-like YAML colors: keys blue, strings light, numbers/bools orange, comments gray.
var (
	yamlKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("74"))
	yamlString  = lipgloss.NewStyle().Foreground(lipgloss.Color("230"))
	yamlScalar  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	yamlComment = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	yamlPunct   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// indent, optional "- ", key, then ":" followed by space or end of line
	// (so "https://x" values are never split as keys).
	yamlKeyRe = regexp.MustCompile(`^(\s*)(- )?([^\s#:][^:]*?):(\s.*|)$`)
	yamlNumRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

func highlightYAML(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return yamlComment.Render(line)
	}
	if m := yamlKeyRe.FindStringSubmatch(line); m != nil {
		out := m[1]
		if m[2] != "" {
			out += yamlPunct.Render("- ")
		}
		out += yamlKey.Render(m[3]) + yamlPunct.Render(":")
		if v := strings.TrimLeft(m[4], " "); v != "" {
			out += strings.Repeat(" ", len(m[4])-len(v)) + yamlValue(v)
		}
		return out
	}
	if rest, ok := strings.CutPrefix(trimmed, "- "); ok { // list item scalar
		return line[:len(line)-len(trimmed)] + yamlPunct.Render("- ") + yamlValue(rest)
	}
	return yamlString.Render(line)
}

func yamlValue(v string) string {
	switch {
	case v == "true" || v == "false" || v == "null" || v == "~" || yamlNumRe.MatchString(v):
		return yamlScalar.Render(v)
	case v == "|" || v == ">" || v == "|-" || v == ">-" || v == "{}" || v == "[]":
		return yamlPunct.Render(v)
	}
	return yamlString.Render(v)
}
