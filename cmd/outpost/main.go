// Command outpost runs an on-demand Minecraft server on Hetzner.
//
// The short version: `outpost start` rents a VPS, restores the world from R2 and
// points DNS at it. `outpost serve` keeps a loop running that notices when
// everybody has logged off, saves the world, and deletes the server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"outpost/internal/api"
	"outpost/internal/config"
	"outpost/internal/orchestrator"
	"outpost/internal/store"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "outpost:", err)
		os.Exit(1)
	}
}

// requiresCloud marks what a command needs before it can run: "all" for the
// Hetzner, Cloudflare, Tailscale and R2 credentials, "r2" for publishing only.
// `outpost status` carries neither, because reading the database should work on
// a box where nothing has been exported yet.
const requiresCloud = "outpost/requires-cloud"

type app struct {
	cfgPath string
	verbose bool

	cfg   *config.Config
	store *store.Store
	orch  *orchestrator.Orchestrator
	log   *slog.Logger
}

func rootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:   "outpost",
		Short: "On-demand Minecraft servers on Hetzner",
		Long: "Outpost rents a Minecraft server when you want to play, saves the world\n" +
			"to R2, and deletes the server once everyone has logged off.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.setup(cmd)
		},
	}
	root.PersistentFlags().StringVarP(&a.cfgPath, "config", "c", "config.yaml", "path to config.yaml")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "debug logging")

	root.AddCommand(
		a.serveCmd(),
		a.startCmd(),
		a.stopCmd(),
		a.statusCmd(),
		a.tickCmd(),
		a.resetCmd(),
		a.publishCmd(),
		a.updateCmd(),
		a.consoleCmd(),
		a.playerCmd(),
	)
	return root
}

func (a *app) setup(cmd *cobra.Command) error {
	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	a.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	switch cmd.Annotations[requiresCloud] {
	case "all":
		if err := cfg.Validate(); err != nil {
			return err
		}
	case "r2":
		if err := cfg.ValidateR2(); err != nil {
			return err
		}
	}
	a.cfg = cfg

	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	a.store = st
	a.orch = orchestrator.New(cfg, st, a.log)
	return nil
}

func (a *app) serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "serve",
		Short:       "Run the reconcile loop and the HTTP API",
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			rost, err := a.rosterFor()
			if err != nil {
				return err
			}
			opts := api.Options{
				Token:  a.cfg.Secrets.APIToken,
				Update: a.selfUpdater(),
				Roster: rost,
				Log:    a.log,
			}
			// Under systemd the way to restart is to leave: Restart=always brings
			// the new binary up. The unit runs with NoNewPrivileges, which stops
			// the `sudo systemctl restart` the CLI path uses from working here.
			// INVOCATION_ID is set by systemd and by nothing else.
			if os.Getenv("INVOCATION_ID") != "" {
				opts.Restart = func() {
					a.log.Info("update installed; exiting so systemd starts the new binary")
					stop()
				}
			}
			handler := api.New(a.orch, a.store, opts)
			srv := &http.Server{
				Addr:              a.cfg.Listen,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}

			errs := make(chan error, 1)
			go func() {
				a.log.Info("listening", "addr", a.cfg.Listen)
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errs <- err
				}
			}()

			loopDone := make(chan struct{})
			go func() {
				defer close(loopDone)
				a.orch.Run(ctx)
			}()
			// The loop owns the database. Closing it under a tick that is mid-save
			// turns a clean shutdown into a half-recorded one.
			defer func() { <-loopDone }()

			select {
			case err := <-errs:
				stop()
				return err
			case <-ctx.Done():
				a.log.Info("shutting down")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(shutdownCtx)
			}
		},
	}
}

func (a *app) startCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "start [region]",
		Short:       "Rent a server and start provisioning it",
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			region := a.cfg.DefaultRegion()
			if len(args) == 1 {
				region = args[0]
			}

			st, err := a.orch.Start(cmd.Context(), region)
			if err != nil {
				return err
			}
			fmt.Printf("provisioning %s in %s (%s)\n", st.TailnetHost, st.Region, st.ServerType)
			fmt.Println("watch it with: outpost status   (or leave `outpost serve` running)")
			return nil
		},
	}
}

func (a *app) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "stop",
		Short:       "Save the world, then delete the server",
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			st, err := a.store.GetState()
			if err != nil {
				return err
			}
			if st.State == store.Offline {
				fmt.Println("already offline")
				return nil
			}

			// Stop is idempotent (already TERMINATING is fine) and waits for any
			// tick in flight, so the order is: mark it, then do one pass of the
			// work. `outpost serve` keeps retrying a failed save on its own.
			if err := a.orch.Stop(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("saving the world and deleting the server...")
			if err := a.orch.Tick(cmd.Context()); err != nil {
				return err
			}
			after, err := a.store.GetState()
			if err != nil {
				return err
			}
			switch after.State {
			case store.Offline:
				fmt.Println("offline")
			case store.Error:
				return fmt.Errorf("the world could not be saved: %s", after.LastError)
			default:
				fmt.Printf("still %s; `outpost serve` will finish it, or run `outpost stop` again\n", after.State)
			}
			return nil
		},
	}
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what the server is doing",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			st, err := a.orch.Status(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Printf("state       %s\n", st.State)
			if st.Region != "" {
				fmt.Printf("region      %s (%s, €%.4f/hr)\n", st.Region, st.ServerType, st.HourlyRate)
			}
			if st.Hostname != "" {
				fmt.Printf("host        %s\n", st.Hostname)
			}
			if st.ServerIP != "" {
				fmt.Printf("address     %s\n", st.ServerIP)
			}
			fmt.Printf("players     %d\n", st.Players)
			if st.UptimeSec > 0 {
				fmt.Printf("uptime      %s\n", (time.Duration(st.UptimeSec) * time.Second).Round(time.Second))
				fmt.Printf("session     €%.4f\n", st.SessionCost)
			}
			if st.TTLSec > 0 {
				fmt.Printf("shuts down  in %s\n", (time.Duration(st.TTLSec) * time.Second).Round(time.Second))
			}
			if st.LastError != "" {
				fmt.Printf("last error  %s\n", st.LastError)
			}
			fmt.Printf("lifetime    %.2f h, €%.4f\n", st.TotalHours, st.TotalCost)
			return nil
		},
	}
}

func (a *app) tickCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "tick",
		Short:       "Run one reconcile pass (for cron)",
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()
			return a.orch.Tick(cmd.Context())
		},
	}
}

func (a *app) resetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete every Outpost server and go back to OFFLINE",
		Long: "Deletes every Outpost server and returns to OFFLINE. It refuses when that would\n" +
			"throw away a world that was never saved to R2, or servers this database is not\n" +
			"tracking; `outpost stop` retries the save, and --force overrides the refusal.",
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			fmt.Println("deleting any Outpost server that still exists...")
			if err := a.orch.Reset(cmd.Context(), force); err != nil {
				return err
			}
			fmt.Println("offline")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete even if the world was never saved, or the server is untracked")
	return cmd
}
