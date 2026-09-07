package argocd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// PodLogOptions for Argo CD application pod logs API.
type PodLogOptions struct {
	AppName      string
	AppNamespace string
	PodNamespace string
	PodName      string
	Container    string
	Follow       bool
	TailLines    int64
	Previous     bool
}

// StreamPodLogs streams log lines via
// GET /api/v1/applications/{name}/pods/{podName}/logs
func (c *Client) StreamPodLogs(ctx context.Context, opts PodLogOptions, emit func(line string) error) error {
	if c == nil {
		return fmt.Errorf("nil argocd client")
	}
	if opts.AppName == "" || opts.PodName == "" {
		return fmt.Errorf("app name and pod name required")
	}
	q := url.Values{}
	if opts.AppNamespace != "" {
		q.Set("appNamespace", opts.AppNamespace)
	}
	if opts.PodNamespace != "" {
		q.Set("namespace", opts.PodNamespace)
	}
	if opts.Container != "" {
		q.Set("container", opts.Container)
	}
	if opts.Follow {
		q.Set("follow", "true")
	}
	if opts.TailLines > 0 {
		q.Set("tailLines", strconv.FormatInt(opts.TailLines, 10))
	}
	if opts.Previous {
		q.Set("previous", "true")
	}

	path := "/api/v1/applications/" + url.PathEscape(opts.AppName) + "/pods/" + url.PathEscape(opts.PodName) + "/logs"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("pod logs: %s: %s", res.Status, truncate(string(b), 300))
	}

	sc := bufio.NewScanner(res.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		content, last, err := parseLogLine(line)
		if err != nil {
			// plain text fallback
			if err := emit(line); err != nil {
				return err
			}
			continue
		}
		if content != "" {
			if err := emit(content); err != nil {
				return err
			}
		}
		if last {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return sc.Err()
}

func parseLogLine(raw string) (content string, last bool, err error) {
	var wrap struct {
		Result struct {
			Content      string `json:"content"`
			Last         bool   `json:"last"`
			TimeStampStr string `json:"timeStampStr"`
			PodName      string `json:"podName"`
		} `json:"result"`
		Content string `json:"content"`
		Last    bool   `json:"last"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		return "", false, err
	}
	if wrap.Result.Content != "" || wrap.Result.Last {
		return wrap.Result.Content, wrap.Result.Last, nil
	}
	return wrap.Content, wrap.Last, nil
}
