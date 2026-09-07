package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/vvrnv/argo9s/internal/config"
	"github.com/vvrnv/argo9s/internal/session"
)

func appCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Application actions (autosync, sync, refresh)",
	}
	cmd.PersistentFlags().StringP("app-namespace", "N", "", "Application namespace (default: config namespace / argocd)")

	cmd.AddCommand(appAutosyncCmd())
	cmd.AddCommand(appSuspendCmd())
	cmd.AddCommand(appResumeCmd())
	cmd.AddCommand(appSyncCmd())
	cmd.AddCommand(appRefreshCmd())
	return cmd
}

func appAutosyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autosync",
		Short: "Enable or disable Application auto-sync",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "on APP",
		Aliases: []string{"enable", "resume"},
		Short:   "Enable automated sync (prune + selfHeal)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Resume(ctx, r); err != nil {
					return err
				}
				fmt.Printf("autosync ON  %s/%s (automated prune+selfHeal)\n", r.Namespace, r.Name)
				return nil
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "off APP",
		Aliases: []string{"disable", "suspend"},
		Short:   "Disable automated sync (manual mode)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Suspend(ctx, r); err != nil {
					return err
				}
				fmt.Printf("autosync OFF %s/%s (manual)\n", r.Namespace, r.Name)
				return nil
			})
		},
	})
	return cmd
}

func appSuspendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "suspend APP",
		Short: "Disable auto-sync (alias for autosync off)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Suspend(ctx, r); err != nil {
					return err
				}
				fmt.Printf("suspended %s/%s\n", r.Namespace, r.Name)
				return nil
			})
		},
	}
}

func appResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume APP",
		Short: "Enable auto-sync (alias for autosync on)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Resume(ctx, r); err != nil {
					return err
				}
				fmt.Printf("resumed %s/%s\n", r.Namespace, r.Name)
				return nil
			})
		},
	}
}

func appSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync APP",
		Short: "Trigger a sync",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Sync(ctx, r); err != nil {
					return err
				}
				fmt.Printf("sync started %s/%s\n", r.Namespace, r.Name)
				return nil
			})
		},
	}
}

func appRefreshCmd() *cobra.Command {
	var hard bool
	cmd := &cobra.Command{
		Use:   "refresh APP",
		Short: "Refresh application from git",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAppSession(cmd, args[0], func(ctx context.Context, sess *session.Session, appName, appNS string) error {
				r, err := sess.GetApplication(ctx, appName, appNS)
				if err != nil {
					return err
				}
				if err := sess.Ops.Refresh(ctx, r, hard); err != nil {
					return err
				}
				kind := "refresh"
				if hard {
					kind = "hard refresh"
				}
				fmt.Printf("%s %s/%s\n", kind, r.Namespace, r.Name)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&hard, "hard", false, "hard refresh")
	return cmd
}

func withAppSession(cmd *cobra.Command, appName string, fn func(context.Context, *session.Session, string, string) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.EnsureBuiltinKubernetes()

	// Mutating app commands: if --readonly not set, prefer connection setting;
	// force RW when user passes --readonly=false.
	if cmd.Flags().Changed("readonly") {
		ro, _ := cmd.Flags().GetBool("readonly")
		cfg.ReadOnly = ro
	}

	conn, err := resolveConnection(cmd, cfg)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("readonly") {
		ro := cfg.ReadOnly
		conn.ReadOnly = &ro
	} else if conn.ReadOnly == nil {
		// Default mutating CLI to writable unless connection/global says otherwise.
		// Keep EffectiveReadOnly behavior.
	}

	appNS, _ := cmd.Flags().GetString("app-namespace")
	if appNS == "" {
		if cmd.Flags().Changed("namespace") {
			appNS, _ = cmd.Flags().GetString("namespace")
		}
	}
	if appNS == "" {
		appNS = cfg.Namespace
	}

	ctxName, _ := cmd.Flags().GetString("context")
	timeout := time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
	if timeout < 60*time.Second {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sess, err := session.Open(ctx, cfg, *conn, ctxName, timeout)
	if err != nil {
		return err
	}
	return fn(ctx, sess, appName, appNS)
}
