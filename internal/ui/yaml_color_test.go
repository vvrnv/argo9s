package ui

import (
	"testing"
)

func TestHighlightYAMLKeepsText(t *testing.T) {
	lines := []string{
		"apiVersion: rbac.authorization.k8s.io/v1",
		"  - name: web",
		"    image: nginx:1.27",
		"  repoURL: https://github.com/x/y",
		"  replicas: 3",
		"  - argocd",
		"# comment",
		"metadata:",
		"plain block text",
	}
	for _, l := range lines {
		// Coloring must never change what is displayed, only its style.
		if got := ansiStrip(highlightYAML(l)); got != l {
			t.Fatalf("text changed:\n got %q\nwant %q", got, l)
		}
	}
	// "https://…" must stay one value, not become key "https".
	if m := yamlKeyRe.FindStringSubmatch("  repoURL: https://github.com/x/y"); m == nil || m[3] != "repoURL" {
		t.Fatalf("key parse: %v", m)
	}
}

func ansiStrip(s string) string {
	out := []rune{}
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && r == 'm':
			esc = false
		case !esc:
			out = append(out, r)
		}
	}
	return string(out)
}

func TestCapsLockSuspect(t *testing.T) {
	for _, k := range []string{"J", "K", "D", "Y", "L"} {
		if !capsLockSuspect(k) {
			t.Errorf("%s should warn", k)
		}
	}
	for _, k := range []string{"Q", "R", "G", "S", "j", "/", "ctrl+c", "enter"} {
		if capsLockSuspect(k) {
			t.Errorf("%s must not warn", k)
		}
	}
}
