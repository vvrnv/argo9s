//go:build !windows

package kube

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls push on every terminal resize until done is closed.
func watchResize(done <-chan struct{}, push func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(sig)
		for {
			select {
			case <-sig:
				push()
			case <-done:
				return
			}
		}
	}()
}
