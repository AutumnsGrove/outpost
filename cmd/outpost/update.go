package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"outpost/internal/store"
	"outpost/internal/updater"
)

// unitName is the systemd unit `outpost serve` runs under. `outpost update`
// restarts it by name; the name is a constant because the page and the CLI had
// better agree about which service they are replacing.
const unitName = "outpost.service"

// selfUpdater builds an updater for this executable's checkout. It returns nil
// (and no error) for a binary that is not sitting in a git checkout — a build
// in /tmp, or a copy someone scp'd — because that is a normal way to run this,
// not a failure.
func (a *app) selfUpdater() *updater.Update {
	u, err := updater.Update{Log: a.log}.ForExecutable(unitName)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(u.Dir, ".git")); err != nil {
		return nil
	}
	return &u
}

// updateCmd pulls, rebuilds and restarts the daemon.
//
// This exists so a phone can do it: Meridian's control plane runs
// `outpost update` as `autumn`, and the status page has a button that does the
// same thing. On its own it is also just the deploy command.
func (a *app) updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Pull, rebuild and restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			u := a.selfUpdater()
			if u == nil {
				return errors.New("this binary is not inside a git checkout, so there is nothing to pull")
			}

			st, err := a.orch.Status(cmd.Context())
			if err != nil {
				return err
			}
			if st.State != string(store.Offline) && st.State != string(store.Error) {
				return fmt.Errorf("server is %s; save and stop it before updating (`outpost stop`)", st.State)
			}

			res, err := u.PullAndBuild(cmd.Context())
			if err != nil {
				return err
			}
			if !res.Changed {
				fmt.Printf("already up to date at %s\n", res.After)
				return nil
			}

			fmt.Printf("updated %s → %s\n", res.Before, res.After)
			if err := u.Restart(cmd.Context()); err != nil {
				return fmt.Errorf("built, but the restart failed: %w", err)
			}
			fmt.Println("restarted", unitName)
			return nil
		},
	}
}
