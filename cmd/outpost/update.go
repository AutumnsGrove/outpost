package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"outpost/internal/store"
	"outpost/internal/updatesignal"
)

// updateCmd asks the host update watcher for the newest image.
//
// This used to be `git pull && go build && systemctl restart` from inside the
// binary. It is a message now, because the daemon is a container: it cannot pull
// an image, it has no shell to build with, and replacing the process that is
// running the request is exactly the kind of thing that is worth doing from
// outside (PLAN 38). The watcher does the pulling, pins the digest, recreates the
// container, checks it came up, and puts the old image back if it did not.
//
// Three callers end up here and all three mean the same thing: this command,
// Meridian's executor, and the page's Update button (through the API). The answer
// arrives in the mailbox, not on stdout — the request file is read by the watcher
// within a second or two, and the container is replaced shortly after.
func (a *app) updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Ask the host update watcher for the newest image",
		Long: "Writes the update request the host watcher watches, then returns. The watcher\\n" +
			"pulls the newest published image by digest, recreates the container, checks it\\n" +
			"answers /health, and rolls back if it does not. Watch it with `outpost status`\\n" +
			"or the page; the outcome is in the update mailbox's result file.",
		Annotations: map[string]string{requiresCloud: "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			mailbox := updatesignal.Mailbox{Dir: a.cfg.UpdateSignalDir}
			if !mailbox.Configured() {
				return errors.New("no update watcher is configured (update_signal_dir is empty), so there is nothing to ask; deploy with deploy/install.sh and the update comes with it")
			}

			// Same rule the old in-process updater had, for the same reason: recreating
			// the daemon mid-session kills the loop that saves the world.
			st, err := a.orch.Status(cmd.Context())
			if err != nil {
				return err
			}
			if st.State != string(store.Offline) && st.State != string(store.Error) {
				return fmt.Errorf("server is %s; save and stop it before updating (`outpost stop`)", st.State)
			}

			if err := mailbox.Request(); err != nil {
				return err
			}
			fmt.Println("asked the update watcher for the newest image:", mailbox.Dir)
			fmt.Println("it pulls, recreates the container and checks it answers /health; the outcome lands in", mailbox.Dir+"/result")
			return nil
		},
	}
}
