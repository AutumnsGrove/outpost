package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"outpost/internal/r2"
	"outpost/internal/roster"
)

// consoleCmd runs RCON commands against the live server.
//
// It reads the server's address and password from the same database the daemon
// writes, so it works from any shell on the box. It needs no cloud credentials:
// RCON only ever reaches Minecraft.
func (a *app) consoleCmd() *cobra.Command {
	var interactive bool
	cmd := &cobra.Command{
		Use:   "console [command...]",
		Short: "Run a command on the live Minecraft server",
		Long: "Sends a command over RCON and prints the reply, exiting non-zero when the\n" +
			"server is not running. With --interactive it is a prompt for a live session.\n" +
			"`stop` and `save-off` are refused: use `outpost stop`.",
		Example: "  outpost console list\n  outpost console \"op AutumnsAdventure\"\n  outpost console -i",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer a.store.Close()

			if !interactive {
				if len(args) == 0 {
					return errors.New("give a command, or use --interactive")
				}
				res, err := a.orch.Console(cmd.Context(), "cli", strings.Join(args, " "))
				if err != nil {
					return err
				}
				fmt.Println(res.Output)
				return nil
			}

			fmt.Println("outpost console — one command per line, `exit` to leave")
			in := bufio.NewScanner(os.Stdin)
			for {
				fmt.Print("> ")
				if !in.Scan() {
					fmt.Println()
					return in.Err()
				}
				line := strings.TrimSpace(in.Text())
				if line == "exit" || line == "quit" {
					return nil
				}
				if line == "" {
					continue
				}
				res, err := a.orch.Console(cmd.Context(), "cli", line)
				if err != nil {
					// A refused or failed command must not end the session.
					fmt.Fprintln(os.Stderr, "error:", err)
					continue
				}
				fmt.Println(res.Output)
			}
		},
	}
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "a prompt instead of a single command")
	return cmd
}

// rosterFor builds the player list over R2.
func (a *app) rosterFor() (*roster.Roster, error) {
	client, err := r2.New(a.cfg.R2.AccountID, a.cfg.Secrets.R2AccessKey, a.cfg.Secrets.R2SecretKey)
	if err != nil {
		return nil, err
	}
	return roster.New(client, a.cfg.Server.AssetBucket, nil), nil
}

// playerCmd manages who may join. Every change is written to R2, where the next
// boot restores it from, and applied to the running server if there is one.
func (a *app) playerCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "player",
		Short: "Manage the whitelist and operators",
		Long: "Changes are written to the asset bucket (what every boot restores) and applied\n" +
			"to the running server over RCON, so they survive the server being deleted.",
	}

	do := func(action roster.Action, short string) *cobra.Command {
		return &cobra.Command{
			Use:         string(action) + " <name>",
			Short:       short,
			Args:        cobra.ExactArgs(1),
			Annotations: map[string]string{requiresCloud: "r2"},
			RunE: func(cmd *cobra.Command, args []string) error {
				defer a.store.Close()
				r, err := a.rosterFor()
				if err != nil {
					return err
				}
				res, err := r.Apply(cmd.Context(), action, args[0], a.orch.Live(),
					func(ctx context.Context, c string) error {
						_, err := a.orch.Console(ctx, "cli", c)
						return err
					})
				if err != nil {
					return err
				}
				_ = a.store.Log("player", "%s %s (by cli)", action, res.Name)
				fmt.Println(res.Message)
				return nil
			},
		}
	}
	root.AddCommand(
		do(roster.ActionAdd, "Let a player join"),
		do(roster.ActionRemove, "Stop a player joining (and drop their operator status)"),
		do(roster.ActionOp, "Make a whitelisted player an operator"),
		do(roster.ActionDeop, "Remove operator status"),
		&cobra.Command{
			Use:         "list",
			Short:       "Show who may join",
			Annotations: map[string]string{requiresCloud: "r2"},
			RunE: func(cmd *cobra.Command, args []string) error {
				defer a.store.Close()
				r, err := a.rosterFor()
				if err != nil {
					return err
				}
				snap, err := r.List(cmd.Context())
				if err != nil {
					return err
				}
				ops := map[string]bool{}
				for _, n := range snap.Ops {
					ops[strings.ToLower(n)] = true
				}
				if len(snap.Whitelist) == 0 {
					fmt.Println("nobody is whitelisted")
				}
				for _, n := range snap.Whitelist {
					mark := ""
					if ops[strings.ToLower(n)] {
						mark = "  (op)"
					}
					fmt.Println(n + mark)
				}
				return nil
			},
		},
	)
	return root
}
