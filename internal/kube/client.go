package kube

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps Kubernetes typed and dynamic clients.
type Client struct {
	Config    *rest.Config
	Typed     kubernetes.Interface
	Dynamic   dynamic.Interface
	Context   string
	Namespace string
}

func New(kubeContext, namespace string, timeout time.Duration) (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	raw, err := clientConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	ctxName := kubeContext
	if ctxName == "" {
		ctxName = raw.CurrentContext
	}

	restCfg, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	if timeout > 0 {
		restCfg.Timeout = timeout
	}

	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	ns := namespace
	if ns == "" {
		if n, _, err := clientConfig.Namespace(); err == nil && n != "" {
			ns = n
		}
	}

	return &Client{
		Config:    restCfg,
		Typed:     typed,
		Dynamic:   dyn,
		Context:   ctxName,
		Namespace: ns,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Typed.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("kubernetes api unreachable: %w", err)
	}
	return nil
}

func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := c.Typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	return out, nil
}

func ContextNames() ([]string, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	raw, err := clientConfig.RawConfig()
	if err != nil {
		return nil, "", err
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	return names, raw.CurrentContext, nil
}
