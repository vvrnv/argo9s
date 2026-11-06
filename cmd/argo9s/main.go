package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vvrnv/argo9s/internal/argocd"
	"github.com/vvrnv/argo9s/internal/config"
	"github.com/vvrnv/argo9s/internal/demo"
	"github.com/vvrnv/argo9s/internal/session"
	"github.com/vvrnv/argo9s/internal/ui"
)

var (
	version   = "0.2.0"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	root := &cobra.Command{
		Use:   "argo9s",
		Short: "K9s-inspired terminal UI for Argo CD",
		Long: `argo9s monitors Argo CD Applications in real time.

Backends:
  • kubernetes  — kubeconfig / in-cluster (watch CRDs)
  • argocd      — Argo CD HTTP API (server + bearer token)

Examples:
  argo9s
  argo9s --connection argocd-prod
  argo9s --server https://argocd.example.com --auth-token "$TOKEN"
  argo9s --server https://argocd.example.com --auth-token "$TOKEN" --insecure
`,
		RunE: runTUI,
	}

	root.PersistentFlags().String("context", "", "kubeconfig context (kubernetes mode)")
	root.PersistentFlags().StringP("namespace", "n", "", "namespace to watch (kubernetes mode)")
	root.PersistentFlags().Bool("all-namespaces", false, "watch all namespaces (kubernetes mode)")
	root.PersistentFlags().Bool("readonly", true, "disable mutating actions")
	root.PersistentFlags().String("connection", "", "named connection from ~/.argo9s/config.yaml")
	root.PersistentFlags().String("server", "", "Argo CD API URL (enables argocd mode)")
	root.PersistentFlags().String("auth-token", "", "Argo CD bearer token")
	root.PersistentFlags().Bool("insecure", false, "skip TLS verify for Argo CD API")
	root.PersistentFlags().Bool("pick", false, "force connection picker at startup")
	root.PersistentFlags().Bool("demo", false, "offline demo with sample Applications (for screenshots)")
	root.PersistentFlags().Bool("no-color", false, "disable TUI colors (also: NO_COLOR=1 or config noColor)")
	root.Flags().Bool("version", false, "print version and exit")

	root.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print version",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Printf("argo9s %s (commit=%s date=%s)\n", version, commit, buildDate)
			},
		},
		configCmd(),
		checkCmd(),
		appCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	showVersion, _ := cmd.Flags().GetBool("version")
	if showVersion {
		fmt.Printf("argo9s %s\n", version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.EnsureBuiltinKubernetes()

	if cmd.Flags().Changed("namespace") {
		ns, _ := cmd.Flags().GetString("namespace")
		cfg.Namespace = ns
		cfg.AllNamespaces = false
	}
	if all, _ := cmd.Flags().GetBool("all-namespaces"); all {
		cfg.AllNamespaces = true
		cfg.Namespace = "all"
	}
	if cmd.Flags().Changed("readonly") {
		ro, _ := cmd.Flags().GetBool("readonly")
		cfg.ReadOnly = ro
	}
	if noColor, _ := cmd.Flags().GetBool("no-color"); noColor {
		cfg.NoColor = true
	}

	// Apply before picker / TUI so monochrome covers all screens.
	ui.ApplyColorMode(cfg.NoColor, "")

	if useDemo, _ := cmd.Flags().GetBool("demo"); useDemo {
		_, err := ui.Run(demo.Open(cfg)) // single fake connection: nothing to switch to
		return err
	}

	conn, err := resolveConnection(cmd, cfg)
	if err != nil {
		return err
	}
	// CLI --readonly overrides per-connection when explicitly set.
	if cmd.Flags().Changed("readonly") {
		ro := cfg.ReadOnly
		conn.ReadOnly = &ro
	}

	ctxName, _ := cmd.Flags().GetString("context")
	for {
		timeout := time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
		if conn.Type == config.ConnArgoCD && timeout < 60*time.Second {
			// Large Argo instances need more than the kube connect timeout.
			timeout = 60 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		sess, err := session.Open(ctx, cfg, *conn, ctxName, timeout)
		cancel()
		if err != nil {
			return err
		}
		next, err := ui.Run(sess)
		if err != nil || next == "" {
			return err
		}
		// :ctx picked another connection — reopen with it.
		found := false
		for i := range cfg.Connections {
			if cfg.Connections[i].Name == next {
				c := cfg.Connections[i]
				conn, ctxName, found = &c, "", true
			}
		}
		if !found {
			return fmt.Errorf("connection %q not found", next)
		}
	}
}

func resolveConnection(cmd *cobra.Command, cfg config.Config) (*config.Connection, error) {
	server, _ := cmd.Flags().GetString("server")
	token, _ := cmd.Flags().GetString("auth-token")
	insecure, _ := cmd.Flags().GetBool("insecure")
	connName, _ := cmd.Flags().GetString("connection")
	forcePick, _ := cmd.Flags().GetBool("pick")

	// Explicit CLI Argo CD API overrides named connections.
	if server != "" {
		if token == "" {
			token = os.Getenv("ARGOCD_AUTH_TOKEN")
		}
		c := config.Connection{
			Name:     "cli",
			Type:     config.ConnArgoCD,
			Server:   server,
			Token:    token,
			Insecure: insecure,
		}
		return &c, nil
	}

	if connName != "" {
		c, ok := cfg.FindConnection(connName)
		if !ok {
			return nil, fmt.Errorf("unknown connection %q (see argo9s config view)", connName)
		}
		return applyCLIOverrides(&c, token, insecure), nil
	}

	if !forcePick && cfg.DefaultConnection != "" {
		if c, ok := cfg.FindConnection(cfg.DefaultConnection); ok {
			// Auto-use default only when it's the sole connection or user set default explicitly
			// and there is only kubernetes — still pick when multiple argocd profiles exist.
			if len(cfg.Connections) == 1 {
				return &c, nil
			}
		}
	}

	if len(cfg.Connections) == 1 {
		c := cfg.Connections[0]
		return applyCLIOverrides(&c, token, insecure), nil
	}

	picked, err := ui.PickConnection(cfg.Connections, cfg.DefaultConnection)
	if err != nil {
		return nil, err
	}
	return applyCLIOverrides(picked, token, insecure), nil
}

func applyCLIOverrides(c *config.Connection, token string, insecure bool) *config.Connection {
	if c == nil {
		return nil
	}
	if c.Type == config.ConnArgoCD {
		if insecure {
			c.Insecure = true
		}
		if token != "" {
			c.Token = token
		}
	}
	return c
}

func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check [connection]",
		Short: "Test an Argo CD / kubernetes connection",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.EnsureBuiltinKubernetes()

			token, _ := cmd.Flags().GetString("auth-token")
			insecure, _ := cmd.Flags().GetBool("insecure")
			server, _ := cmd.Flags().GetString("server")

			var conn *config.Connection
			switch {
			case server != "":
				if token == "" {
					token = os.Getenv("ARGOCD_AUTH_TOKEN")
				}
				c := config.Connection{
					Name:     "cli",
					Type:     config.ConnArgoCD,
					Server:   server,
					Token:    token,
					Insecure: insecure,
				}
				conn = &c
			case len(args) == 1:
				c, ok := cfg.FindConnection(args[0])
				if !ok {
					return fmt.Errorf("unknown connection %q", args[0])
				}
				conn = applyCLIOverrides(&c, token, insecure)
			default:
				if flag, _ := cmd.Flags().GetString("connection"); flag != "" {
					c, ok := cfg.FindConnection(flag)
					if !ok {
						return fmt.Errorf("unknown connection %q", flag)
					}
					conn = applyCLIOverrides(&c, token, insecure)
				} else {
					c, ok := cfg.FindConnection(cfg.DefaultConnection)
					if !ok {
						c = cfg.Connections[0]
					}
					conn = applyCLIOverrides(&c, token, insecure)
				}
			}

			fmt.Printf("connection: %s\ntype: %s\n", conn.Name, conn.Type)
			if conn.Type == config.ConnArgoCD {
				fmt.Printf("server: %s\ntoken source: %s\ninsecure: %v\n", conn.Server, conn.TokenSourceHint(), conn.Insecure)
				tok := conn.ResolvedToken()
				if tok == "" {
					return fmt.Errorf("token is empty — export %s or pass --auth-token", conn.TokenEnv)
				}
				fmt.Printf("token length: %d\n", len(tok))
			}

			timeout := 60 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			ctxName, _ := cmd.Flags().GetString("context")
			sess, err := session.Open(ctx, cfg, *conn, ctxName, timeout)
			if err != nil {
				return err
			}
			fmt.Printf("ok: connected as %s\n", sess.Label)

			if sess.Type == config.ConnArgoCD && sess.Argo != nil {
				apps, err := sess.Argo.ListApplications(ctx)
				if err != nil {
					return fmt.Errorf("list applications: %w", err)
				}
				named := argocd.ParseCount(apps)
				fmt.Printf("applications: %d\n", len(apps))
				fmt.Printf("parsed names: %d\n", named)
				fmt.Printf("sample: %s\n", argocd.SampleAppDiag(apps))
				if len(apps) > 0 && named == 0 {
					return fmt.Errorf("API returned %d apps but none had metadata.name — cannot populate TUI", len(apps))
				}
			}
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or set configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "view",
		Short: "Print current config path and connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			path, _ := config.Path()
			fmt.Printf("# %s\n", path)
			fmt.Printf("namespace: %s\nreadOnly: %v\nallNamespaces: %v\nconnectTimeoutSeconds: %d\n",
				cfg.Namespace, cfg.ReadOnly, cfg.AllNamespaces, cfg.ConnectTimeoutSeconds)
			fmt.Printf("defaultConnection: %s\nconnections:\n", cfg.DefaultConnection)
			for _, c := range cfg.Connections {
				fmt.Printf("  - name: %s\n    type: %s\n    readOnly: %v\n", c.Name, c.Type, c.EffectiveReadOnly(cfg.ReadOnly))
				if c.Type == config.ConnArgoCD {
					fmt.Printf("    server: %s\n    tokenEnv: %s\n    insecure: %v\n", c.Server, c.TokenEnv, c.Insecure)
				} else if c.Context != "" {
					fmt.Printf("    context: %s\n", c.Context)
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write example ~/.argo9s/config.yaml (if missing)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			dir, err := config.Dir()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(config.ExampleYAML()), 0o600); err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", path)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set [key] [value]",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.Set(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("set %s=%s\n", args[0], args[1])
			return nil
		},
	})
	return cmd
}
