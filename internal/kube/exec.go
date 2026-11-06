package kube

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// PodShell is an interactive shell in a pod container. It satisfies Bubble Tea's
// ExecCommand interface (Run + SetStdin/SetStdout/SetStderr), so the TUI can hand
// the terminal over with tea.Exec and get it back when the shell exits.
type PodShell struct {
	client                    *Client
	Namespace, Pod, Container string
	stdin                     io.Reader
	stdout, stderr            io.Writer
}

func (c *Client) PodShell(namespace, pod, container string) *PodShell {
	return &PodShell{client: c, Namespace: namespace, Pod: pod, Container: container,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
}

func (p *PodShell) SetStdin(r io.Reader)  { p.stdin = r }
func (p *PodShell) SetStdout(w io.Writer) { p.stdout = w }
func (p *PodShell) SetStderr(w io.Writer) { p.stderr = w }

// shellCmd prefers bash, falls back to sh (distroless images have neither: the
// API then returns an error that we surface as-is).
var shellCmd = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}

func (p *PodShell) Run() error {
	if p.client == nil || p.client.Config == nil || p.client.Typed == nil {
		return fmt.Errorf("nil kube client")
	}
	req := p.client.Typed.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(p.Namespace).Name(p.Pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: p.Container,
			Command:   shellCmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    false, // with a TTY stderr is merged into stdout
			TTY:       true,
		}, scheme.ParameterCodec)

	// Same transport choice as kubectl exec: websocket first, SPDY fallback.
	spdy, err := remotecommand.NewSPDYExecutor(p.client.Config, "POST", req.URL())
	if err != nil {
		return err
	}
	ws, err := remotecommand.NewWebSocketExecutor(p.client.Config, "GET", req.URL().String())
	if err != nil {
		return err
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}

	var sizes remotecommand.TerminalSizeQueue
	if f, ok := p.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		state, err := term.MakeRaw(int(f.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(f.Fd()), state)
		q := newSizeQueue(int(f.Fd()))
		defer q.stop()
		sizes = q
	}

	return exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
		Stdin:             p.stdin,
		Stdout:            p.stdout,
		Tty:               true,
		TerminalSizeQueue: sizes,
	})
}

// sizeQueue reports the local terminal size to the pod: once at start, then on
// every resize (SIGWINCH on unix; see exec_resize_*.go).
type sizeQueue struct {
	ch   chan remotecommand.TerminalSize
	done chan struct{}
}

func newSizeQueue(fd int) *sizeQueue {
	q := &sizeQueue{ch: make(chan remotecommand.TerminalSize, 1), done: make(chan struct{})}
	push := func() {
		if w, h, err := term.GetSize(fd); err == nil {
			select {
			case q.ch <- remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
			default: // a resize is already pending; the next one will carry the latest size
			}
		}
	}
	push()
	watchResize(q.done, push)
	return q
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.ch:
		return &s
	case <-q.done:
		return nil
	}
}

func (q *sizeQueue) stop() { close(q.done) }
