package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vvrnv/argo9s/internal/argo"
)

// Typing "/pts" must find a name with "pts" in the middle, like k9s.
func TestFilterMatchesMidString(t *testing.T) {
	s := argo.NewStore()
	s.Upsert(argo.Resource{Kind: argo.KindApplication, Namespace: "argocd", Name: "dsfsdf-pts-api"})
	s.Upsert(argo.Resource{Kind: argo.KindApplication, Namespace: "argocd", Name: "other"})
	m := Model{store: s, filterKind: argo.KindAll, allNS: true, namespace: "all", filterIn: textinput.New()}

	var mm tea.Model = m
	for _, k := range []string{"/", "p", "t", "s"} {
		mm, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
	got := mm.(Model).resources
	if len(got) != 1 || got[0].Name != "dsfsdf-pts-api" {
		t.Fatalf("filter %q: got %+v", mm.(Model).filter, got)
	}
}

func TestFilterMatcherK9sSemantics(t *testing.T) {
	cases := []struct {
		f, s string
		want bool
	}{
		{"pts", "dsfsdf-pts-api", true},
		{"PTS", "dsfsdf-pts-api", true},
		{"pts|web", "my-web", true},
		{"^dsf.*api$", "dsfsdf-pts-api", true},
		{"!pts", "dsfsdf-pts-api", false},
		{"!pts", "other", true},
		{"a[", "x-a[-y", true}, // invalid regex falls back to substring
	}
	for _, c := range cases {
		if got := filterMatcher(c.f)(c.s); got != c.want {
			t.Errorf("filter %q on %q = %v, want %v", c.f, c.s, got, c.want)
		}
	}
	if filterMatcher("") != nil || filterMatcher("!") != nil {
		t.Error("empty filter must be nil")
	}
}

func TestProgressBarKeepsWidth(t *testing.T) {
	for f := 0; f < 100; f++ {
		if w := lipgloss.Width(progressBar(f, 24)); w != 24 {
			t.Fatalf("frame %d: width %d", f, w)
		}
	}
}
