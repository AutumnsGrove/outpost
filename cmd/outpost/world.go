// The `world` command is about the other bucket: mc-worlds holds one tarball per
// profile, and nothing else in the system can tell you what is in there. It is
// also the only door to the world from before profiles existed, which is still
// sitting at current/world.tar.gz where nothing reads it.
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"outpost/internal/publish"
	"outpost/internal/r2"
	"outpost/internal/store"
	"outpost/internal/world"
)

func (a *app) worldCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "world",
		Short: "See the saved worlds, and adopt one into a profile",
		Long: "A world is one tarball in the world bucket, beside a marker that says which\n" +
			"pack made it. Profiles made the world something a pack keeps, so the world\n" +
			"from before profiles is still at current/world.tar.gz, where nothing reads\n" +
			"it and no amount of playing will bring it back.\n\n" +
			"Deleting copies the world to trash/ first and removes it second, and the\n" +
			"trash keeps it for " + world.TrashRetention.String() + ". Nothing here deletes the only\n" +
			"copy of a world in one step.",
	}
	cmd.AddCommand(a.worldListCmd(), a.worldImportCmd(), a.worldDeleteCmd())
	return cmd
}

// r2Client is the one place the CLI builds a bucket client.
func (a *app) r2Client() (*r2.Client, error) {
	return r2.New(a.cfg.R2.AccountID, a.cfg.Secrets.R2AccessKey, a.cfg.Secrets.R2SecretKey)
}

func (a *app) worldListCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "Every profile's world, and any world no profile claims",
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.r2Client()
			if err != nil {
				return err
			}
			worlds, err := world.List(cmd.Context(), client, a.cfg.Server.WorldBucket)
			if err != nil {
				return err
			}
			if len(worlds) == 0 {
				fmt.Println("no worlds saved yet: the first session writes one to",
					a.cfg.Server.WorldBucket)
				return nil
			}

			active, err := a.orch.ActiveProfile()
			if err != nil {
				return err
			}

			// Profiles' worlds first, with the one the next start boots marked, then
			// whatever nobody claims: the orphan is the interesting one, because it is
			// the only copy of a world nothing will ever load again.
			orphans := 0
			for _, w := range worlds {
				if w.Orphaned() {
					orphans++
					continue
				}
				mark := " "
				if w.Profile == active {
					mark = "*"
				}
				fmt.Printf("%s %-24s %8s  %s\n", mark, w.Profile, humanSize(w.Size), markerLine(w.Pack))
			}
			if orphans > 0 {
				fmt.Println("no profile claims these — `outpost world import <profile> --from <key>`:")
				for _, w := range worlds {
					if w.Orphaned() {
						fmt.Printf("  %-40s %8s  %s\n", w.Key, humanSize(w.Size), markerLine(w.Pack))
					}
				}
			}
			return nil
		},
	}
}

func (a *app) worldImportCmd() *cobra.Command {
	var from string

	cmd := &cobra.Command{
		Use:   "import <profile>",
		Short: "Copy an existing world into a profile, leaving the original alone",
		Long: "Adopts a world that no profile owns — by default the one from before\n" +
			"profiles, at " + world.LegacyKey + " — by copying it into the profile. The\n" +
			"copy is server-side and its size is verified, and the source is never\n" +
			"deleted: it is the only copy until the profile has one.\n\n" +
			"Idempotent. A profile that already holds a world is left alone rather than\n" +
			"overwritten.",
		Example: "  outpost world import vanilla\n" +
			"  outpost world import fabulously-optimized --from current/world-old.tar.gz",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := args[0]
			if err := publish.ValidateProfile(profile); err != nil {
				return err
			}

			// A rented server already has one profile's world open, and it will
			// upload over whatever is in the profile when it shuts down. An import
			// underneath that is a copy that disappears the moment they log off.
			st, err := a.store.GetState()
			if err != nil {
				return err
			}
			if st.State != store.Offline {
				return fmt.Errorf("the server is %s; stop it before importing a world", st.State)
			}

			client, err := a.r2Client()
			if err != nil {
				return err
			}
			res, err := world.Import(cmd.Context(), client, world.ImportOptions{
				WorldBucket: a.cfg.Server.WorldBucket,
				AssetBucket: a.cfg.Server.AssetBucket,
				Profile:     profile,
				From:        from,
			})
			if err != nil {
				return err
			}
			if res.AlreadyThere {
				fmt.Printf("profile %s already has a world (%s) — left alone, nothing was copied\n",
					res.Profile, humanSize(res.Bytes))
				return nil
			}
			fmt.Printf("imported %s into %s (%s)\n", res.From, res.To, humanSize(res.Bytes))
			fmt.Printf("the source is untouched; `outpost pack use %s` boots it next\n", res.Profile)
			fmt.Printf("marker: %s\n", markerLine(&res.Marker))
			return nil
		},
	}

	cmd.Flags().StringVar(&from, "from", "", "the world key to adopt (default: the pre-profile "+world.LegacyKey+")")
	return cmd
}

func (a *app) worldDeleteCmd() *cobra.Command {
	var key string

	cmd := &cobra.Command{
		Use:   "delete <profile>",
		Short: "Move a world to the trash, where it waits 14 days",
		Long: "Copies the world to trash/ and only then removes it, so a copy that fails\n" +
			"leaves the world exactly where it was. The trash expires on its own.\n\n" +
			"Pass --key to delete a world no profile claims, using the key `outpost\n" +
			"world list` printed. That includes the world from before profiles, which\n" +
			"is the only copy of it: the trash is the only way back.\n\n" +
			"Deleting the active profile's world is allowed — Start makes a fresh one —\n" +
			"and refused while a server is rented, because the session uploads over its\n" +
			"own world when it shuts down.",
		Example: "  outpost world delete cobblemon-fabric\n" +
			"  outpost world delete --key current/world.tar.gz",
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 1 {
				profile = args[0]
			}
			if (profile == "") == (key == "") {
				return fmt.Errorf("name a profile or pass --key: one of them says what to delete")
			}

			st, err := a.store.GetState()
			if err != nil {
				return err
			}
			if st.State != store.Offline {
				return fmt.Errorf("the server is %s; stop it before deleting a world — it would upload over the delete", st.State)
			}

			if profile != "" {
				active, err := a.orch.ActiveProfile()
				if err != nil {
					return err
				}
				if profile == active {
					fmt.Printf("note: %s is the active profile, so the next start boots it with a fresh world\n", profile)
				}
			}

			client, err := a.r2Client()
			if err != nil {
				return err
			}
			res, err := world.Delete(cmd.Context(), client, world.DeleteOptions{
				WorldBucket: a.cfg.Server.WorldBucket,
				Profile:     profile,
				Key:         key,
			})
			if err != nil {
				return err
			}
			for _, s := range res.Swept {
				fmt.Printf("swept   %s (past its retention)\n", s)
			}
			if res.Missing {
				fmt.Printf("%s is not there — nothing to delete\n", res.Key)
				return nil
			}
			fmt.Printf("trashed %s (%s) to %s\n", res.Key, humanSize(res.Bytes), res.Trash)
			fmt.Printf("recoverable until %s, then it goes\n", res.Expires.Format("2006-01-02 15:04 MST"))
			return nil
		},
	}

	cmd.Flags().StringVar(&key, "key", "", "delete this exact world key instead of a profile's world")
	return cmd
}

// markerLine is what a world's marker says, in one line.
func markerLine(p *publish.Pack) string {
	if p == nil {
		return "no marker — nothing knows what made it"
	}
	if p.Describe() == "" {
		return "hand-rolled world"
	}
	return p.Describe()
}
