package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"outpost/internal/publish"
	"outpost/internal/r2"
)

// publishCmd fills the asset bucket with the files a VPS boots from.
//
// It is the missing half of the system: internal/cloudinit has always known how
// to pull a server out of R2, and until now nothing could put one there.
func (a *app) publishCmd() *cobra.Command {
	var (
		fabricMC  string
		loader    string
		whitelist []string
		ops       []string
		open      bool
		prune     bool
		profile   string
		newWorld  bool
		keepWorld bool
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "publish [dir]",
		Short: "Upload a server pack to R2",
		Long: "Uploads a pack directory to the asset bucket in the layout the VPS expects:\n" +
			"server/, mods/, config/, scripts/ and root/. A pack.json left by\n" +
			"`outpost pack fetch` supplies the Minecraft version, the loader version and\n" +
			"the RAM the pack needs, so those flags are only for hand-rolled packs.\n" +
			"A launcher jar can be fetched from Fabric Meta, and Minecraft names are\n" +
			"resolved to UUIDs through Mojang, so the whitelist never goes stale.\n" +
			"\n" +
			"The player list comes from the first of these that exists: --whitelist\n" +
			"(resolved through Mojang, and --op with it), a server/whitelist.json in\n" +
			"the pack, or the list already in R2. The third is what re-publishing an\n" +
			"installed pack does: no lookups, the bytes go back up unchanged, and any\n" +
			"operators already recorded stay operators. --open skips all three and\n" +
			"turns white-list off instead.",
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "pack"
			if len(args) == 1 {
				dir = args[0]
			}
			if newWorld && keepWorld {
				return fmt.Errorf("--new-world and --keep-world ask for opposite things")
			}
			if profile == "" {
				// A fetched pack knows its own name; a directory of hand-picked jars
				// does not, and those go to "default".
				if pk, ok, err := publish.LoadPack(dir); err == nil && ok {
					profile = publish.ProfileFromSlug(pk.Slug)
				}
			}

			client, err := r2.New(a.cfg.R2.AccountID, a.cfg.Secrets.R2AccessKey, a.cfg.Secrets.R2SecretKey)
			if err != nil {
				return err
			}

			opts := publish.Options{
				Dir:          dir,
				AssetBucket:  a.cfg.Server.AssetBucket,
				WorldBucket:  a.cfg.Server.WorldBucket,
				Profile:      profile,
				FabricMC:     fabricMC,
				FabricLoader: loader,
				Whitelist:    whitelist,
				Ops:          ops,
				// Without this, re-publishing an installed pack demands the names
				// again and resolves each one through Mojang, when R2 has held the
				// answer since the last session.
				ReusePlayers: true,
				Open:         open,
				DryRun:       dryRun,
				Prune:        prune,
				Regions:      a.cfg.Regions,
				NewWorld:     newWorld,
				KeepWorld:    keepWorld,
				Log:          a.log,
			}

			res, err := publish.Run(cmd.Context(), client, opts)
			if err != nil {
				return err
			}

			for _, w := range res.Warnings {
				fmt.Printf("note    %s\n", w)
			}
			for _, k := range res.Removed {
				fmt.Printf("pruned  %s\n", k)
			}

			verb := "uploaded"
			if dryRun {
				verb = "would upload"
			}
			fmt.Printf("%s %d file(s), %s\n", verb, len(res.Uploaded), humanSize(res.TotalSize))
			fmt.Printf("unchanged %d file(s) in %s\n", len(res.Unchanged), a.cfg.Server.AssetBucket)
			if len(res.Players) > 0 {
				fmt.Printf("whitelist %d player(s): %s\n", len(res.Players), strings.Join(res.Players, ", "))
			}

			if dryRun {
				fmt.Println("dry run: nothing written")
				return nil
			}
			if len(res.Uploaded) > 0 || len(res.Removed) > 0 {
				fmt.Printf("ready: `outpost start` will boot from this\n")
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&fabricMC, "fabric", "", "fetch a Fabric server launcher for this Minecraft version (e.g. 1.20.1)")
	f.StringVar(&loader, "loader", "", "pin the Fabric loader (default: newest stable)")
	f.StringSliceVar(&whitelist, "whitelist", nil, "Minecraft names to allow, resolved to UUIDs (default: the list already in R2)")
	f.StringSliceVar(&ops, "op", nil, "Minecraft names to make operators (must be on the whitelist)")
	f.BoolVar(&open, "open", false, "publish with white-list=false (anyone who finds the address can join)")
	f.StringVar(&profile, "profile", "", "profile to publish into: a pack and its world (default: the pack's slug, or \"default\" for a hand-rolled pack)")
	f.BoolVar(&prune, "prune", false, "delete mods/ and config/ keys that are not in the pack (always on when the pack has a pack.json)")
	f.BoolVar(&newWorld, "new-world", false, "discard the saved world when it was made by another pack (it goes to trash/ first)")
	f.BoolVar(&keepWorld, "keep-world", false, "load a world saved by another pack anyway")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without touching R2")

	return cmd
}

// humanSize keeps the publish summary readable without a dependency.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(n)
	for _, u := range units {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f %s", size, u)
		}
	}
	return fmt.Sprintf("%.1f PB", size)
}
