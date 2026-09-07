package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// PodLogOptions controls a log stream.
type PodLogOptions struct {
	Namespace  string
	Pod        string
	Container  string
	Follow     bool
	TailLines  *int64
	Timestamps bool
	Previous   bool
}

// StreamPodLogs streams pod logs to emit (one line at a time). Uses a client
// copy without request timeout so --follow can run until ctx cancel.
func (c *Client) StreamPodLogs(ctx context.Context, opts PodLogOptions, emit func(line string) error) error {
	if c == nil || c.Config == nil {
		return fmt.Errorf("nil kube client")
	}
	if opts.Pod == "" {
		return fmt.Errorf("pod name required")
	}
	ns := opts.Namespace
	if ns == "" {
		ns = "default"
	}

	cfg := rest.CopyConfig(c.Config)
	cfg.Timeout = 0
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	logOpts := &corev1.PodLogOptions{
		Container:  opts.Container,
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
		Previous:   opts.Previous,
	}
	if opts.TailLines != nil {
		logOpts.TailLines = opts.TailLines
	}

	req := typed.CoreV1().Pods(ns).GetLogs(opts.Pod, logOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		if err := emit(sc.Text()); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
