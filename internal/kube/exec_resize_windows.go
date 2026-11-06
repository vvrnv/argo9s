//go:build windows

package kube

// watchResize: Windows consoles have no SIGWINCH; the initial size is kept.
// shortcut: no live resize on Windows; poll term.GetSize if users ask for it.
func watchResize(<-chan struct{}, func()) {}
