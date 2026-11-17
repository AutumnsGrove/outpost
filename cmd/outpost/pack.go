// The `pack` command materialises a Modrinth modpack as the directory
// `outpost publish` uploads. It is the only command that knows what Modrinth is:
// publish stays the thing that uploads a directory and verifies key sizes.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"outpost/internal/pack"
	"outpost/internal/publish"
	"outpost/internal/r2"
	"outpost/internal/roster"
	"outpost/internal/store"
	"outpost/internal/trash"
)

func (a *app) packCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pack",
		Short: "Fetch a Modrinth modpack into a pack directory",
		Long: "Resolves a Modrinth modpack and writes the server files `outpost publish`\n" +
			"uploads. The pack's own manifest decides the jar versions, the loader\n" +
			"version, and which files a dedicated server must not load, so none of that\n" +
			"is hand-maintained.\n\n" +
			"Source can be a Modrinth slug, a modrinth.com modpack link (Prism's \"open\n" +
			"in browser\" gives you one), a .mrpack URL, or a .mrpack on disk.",
	}
	cmd.AddCommand(a.packFetchCmd(), a.packVanillaCmd(), a.packListCmd(), a.packUseCmd(), a.packDeleteCmd())
	return cmd
}

// packManager wires the same Manager the page uses: R2, the buckets, the plans,
// the player list and the switch. One implementation, two front doors.
func (a *app) packManager(rost *roster.Roster) (*pack.Manager, error) {
	client, err := r2.New(a.cfg.R2.AccountID, a.cfg.Secrets.R2AccessKey, a.cfg.Secrets.R2SecretKey)
	if err != nil {
		return nil, err
	}
	m := &pack.Manager{
		AssetBucket: a.cfg.Server.AssetBucket,
		WorldBucket: a.cfg.Server.WorldBucket,
		Dir:         a.cfg.Server.PackDir,
		Store:       client,
		Regions:     a.cfg.Regions,
		Log:         a.log,
		Activate:    func(_ context.Context, profile string) error { return a.orch.SetActiveProfile(profile) },
	}
	return m, nil
}

func (a *app) packVanillaCmd() *cobra.Command {
	var dir, mc string

	cmd := &cobra.Command{
		Use:   "vanilla",
		Short: "Write a server with no mods: Fabric, an empty mods/, and the Minecraft version you name",
		Long: "Writes the pack.json that makes a directory a modless server, which is what\n" +
			"\"vanilla\" means at Outpost: Fabric with nothing loaded. `outpost publish`\n" +
			"fetches the launcher for it, so the next command is the only one left.\n\n" +
			"--mc is the Minecraft version, and it has to be a row of the table the image\n" +
			"boots (" + publish.MinecraftList() + "); without it the oldest row is\n" +
			"written, which is what the page's Vanilla button defaults to as well.\n\n" +
			"It is a pack like any other — a profile, a world, a name in the panel — and\n" +
			"the page's Vanilla button does exactly this.",
		Example: "  outpost pack vanilla --mc 1.21.1 && outpost publish packs/vanilla --profile vanilla-1-21-1",
		Args:    cobra.NoArgs,
		// Modrinth is not involved and neither is R2: this writes one file.
		Annotations: map[string]string{noConfig: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			pk := publish.VanillaPack(mc)
			// The same refusal a fetched pack gets, and before anything is written: a
			// pack.json naming a version with no JDK row is a boot that fails on the VPS
			// twenty minutes into a rental.
			if err := publish.CheckPack(pk); err != nil {
				return err
			}
			if dir == "" {
				dir = filepath.Join("packs", pk.Slug)
			}
			// Refuse to write into a directory that holds another pack: `publish`
			// uploads what is in the directory, so old jars in here would go up as a
			// vanilla server's mods.
			entries, err := os.ReadDir(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			for _, e := range entries {
				if e.Name() != publish.PackFile {
					return fmt.Errorf("%s holds another pack (%s); remove it, or pass --dir for a fresh directory", dir, e.Name())
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := publish.WritePack(dir, pk); err != nil {
				return err
			}
			fmt.Printf("wrote %s: %s, no mods\n", filepath.Join(dir, publish.PackFile), pk.Describe())
			fmt.Printf("next: outpost publish %s\n", dir)
			// A version that is not the default is the one case where a plain publish
			// can refuse: the world guard will not carry a world across Minecraft
			// versions. It is a condition, not a destination — this command has no R2
			// access and cannot tell whether the profile holds a world, and the panel's
			// button always targets `vanilla`, so steering a fresh install into a
			// versioned profile is the two front doors disagreeing about where vanilla
			// lives. Wording it as the guard's own answer keeps them one sentence.
			//
			// pk.MC rather than the flag: --mc "1.20.1 " is the default version, and
			// VanillaPack is what canonicalised it.
			if pk.MC != publish.SupportedMC {
				fmt.Printf("      (%s is not the default %s: if profile %s already holds a world, publish refuses and names --profile %s)\n",
					pk.MC, publish.SupportedMC, publish.ProfileFromSlug(pk.Slug),
					publish.ProfileFromSlug(pk.Slug+"-"+strings.ReplaceAll(pk.MC, ".", "-")))
			}
			// `publish` refuses a server nobody could join, and the roster in R2 is
			// what it falls back to. Saying so here beats a refusal that looks like
			// the vanilla directory is wrong.
			fmt.Println("      (that reuses the player list in R2; on an empty bucket pass --whitelist <names>)")
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "where to write it (default packs/vanilla)")
	cmd.Flags().StringVar(&mc, "mc", "", "Minecraft version to write (default "+publish.SupportedMC+"); one of "+publish.MinecraftList())
	return cmd
}

func (a *app) packListCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "List the installed profiles, and which one is active",
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.packManager(nil)
			if err != nil {
				return err
			}
			active, err := a.orch.ActiveProfile()
			if err != nil {
				return err
			}
			profiles, err := m.Profiles(cmd.Context(), active)
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				fmt.Println("no profiles yet: `outpost pack fetch <link>` then `outpost publish --profile <name> <dir>`")
				return nil
			}
			for _, p := range profiles {
				marker := " "
				if p.Active {
					marker = "*"
				}
				name := p.ID
				if p.Pack != nil {
					name = fmt.Sprintf("%-22s %-26s", p.ID, p.Pack.Describe())
				}
				world := "no world"
				if p.HasWorld {
					world = "world"
				}
				fmt.Printf("%s %s %4d mod(s) %8s  %s\n", marker, name, p.Mods, humanSize(p.Bytes), world)
			}
			return nil
		},
	}
}

func (a *app) packDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <profile>",
		Short: "Remove a profile: its world, its backups, and its files",
		Long: "Removes mc-assets/profiles/<id>/ and mc-worlds/profiles/<id>/. The world is\n" +
			"copied to trash/ first and stays recoverable for " + fmt.Sprint(trash.RetentionDays) + " days; the\n" +
			"backups under the profile go with it.\n\n" +
			"Needs --yes, because it is the one command here that removes something you\n" +
			"cannot publish again from a link. Refused while a server is rented, and\n" +
			"refused on the active profile: `outpost pack use <other>` first, since\n" +
			"deleting what Start boots is a confusing way to find out what happened.",
		Example: "  outpost pack delete cobblemon-fabric --yes",
		Args:    cobra.ExactArgs(1),
		// R2 and the database: the active profile decides whether this is allowed.
		Annotations: map[string]string{requiresCloud: "r2"},
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := args[0]
			if err := publish.ValidateProfile(profile); err != nil {
				return err
			}

			active, err := a.orch.ActiveProfile()
			if err != nil {
				return err
			}
			if profile == active {
				return fmt.Errorf("profile %s is the active one; `outpost pack use <other>` first, then delete it", profile)
			}
			st, err := a.store.GetState()
			if err != nil {
				return err
			}
			if st.State != store.Offline {
				return fmt.Errorf("the server is %s; stop it before deleting a profile", st.State)
			}
			if !yes {
				return fmt.Errorf("this removes profile %q, its backups and its files (the world is kept in trash for %d days); pass --yes if that is what you want",
					profile, trash.RetentionDays)
			}

			m, err := a.packManager(nil)
			if err != nil {
				return err
			}
			res, err := m.DeleteProfile(cmd.Context(), profile, active, nil)
			if err != nil {
				return err
			}
			if res.Trashed {
				fmt.Printf("world   %s to %s, recoverable until %s\n",
					humanSize(res.World.Bytes), res.World.Trash, res.World.Expires.Format("2006-01-02 15:04 MST"))
			} else {
				fmt.Printf("world   none saved, nothing to trash\n")
			}
			fmt.Printf("removed %d asset key(s) and %d world key(s) (marker, backups)\n", res.Assets, res.Leftovers)
			if res.LocalBytes > 0 {
				fmt.Printf("freed   %s on the box (its working copy of the pack)\n", humanSize(res.LocalBytes))
			}
			fmt.Printf("profile %s is gone\n", profile)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "actually delete it")
	return cmd
}

func (a *app) packUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile>",
		Short: "Switch which pack and world the next session boots",
		Long: "Switches the active profile. Refused while a server is rented: a running server\n" +
			"already pulled one profile's assets and will upload its world to that profile.",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{requiresCloud: "none"},
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := args[0]
			if err := publish.ValidateProfile(profile); err != nil {
				return err
			}
			if err := a.orch.SetActiveProfile(profile); err != nil {
				return err
			}
			fmt.Printf("profile %s is now active: `outpost start` boots it\n", profile)
			return nil
		},
	}
}

func (a *app) packFetchCmd() *cobra.Command {
	var (
		dir          string
		version      string
		memoryGB     int
		skipOptional bool
		force        bool
	)

	cmd := &cobra.Command{
		Use:   "fetch <modrinth-pack>",
		Short: "Download a modpack's server files into a pack directory",
		Example: "  outpost pack fetch https://modrinth.com/modpack/cobblemon-fabric --memory 4\n" +
			"  outpost publish pack --whitelist AutumnsAdventure --op AutumnsAdventure",
		Args: cobra.ExactArgs(1),
		// Needs no config.yaml and no credentials: this command only talks to
		// Modrinth and writes files, and it has to work on a fresh clone.
		Annotations: map[string]string{noConfig: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				// The same place the page installs into, so a CLI fetch and a page
				// install of the same pack do not end up as two directories.
				dir = filepath.Join("packs", profileOr(args[0]))
			}
			res, err := pack.Fetch(cmd.Context(), pack.Options{
				Source:       args[0],
				Version:      version,
				Dir:          dir,
				MemoryGB:     memoryGB,
				SkipOptional: skipOptional,
				Force:        force,
				Log:          a.log,
			})
			if err != nil {
				return err
			}

			p := res.Pack
			fmt.Printf("%s: minecraft %s, %s %s\n", p.Describe(), p.MC, p.Loader, p.LoaderVersion)
			fmt.Printf("wrote %d file(s) (%d mod(s)), %s into %s/\n", res.Files, res.Mods, humanSize(res.Bytes), dir)
			if len(res.Skipped) > 0 {
				fmt.Printf("skipped %d file(s): client-only, optional-and-skipped, or unsupported on a server\n", len(res.Skipped))
			}
			for _, w := range res.Warnings {
				fmt.Printf("note    %s\n", w)
			}
			fmt.Printf("start.sh: %d GB box, %d GB heap\n", p.MemoryGB, pack.HeapFor(p.MemoryGB))
			fmt.Printf("next: outpost publish %s --whitelist <names> --op <name>\n", dir)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", `where to write the pack directory (default "packs/<profile>")`)
	f.StringVar(&version, "version", "", "pin a pack version (id or version number; default: newest release for Minecraft 1.20.1)")
	f.IntVar(&memoryGB, "memory", 4, "RAM in GB of the box you will rent; sizes start.sh and is checked by publish")
	f.BoolVar(&skipOptional, "skip-optional", false, "leave out files the pack marks optional for the server")
	f.BoolVar(&force, "force", false, "re-download files that already match their checksum")

	return cmd
}

// profileOr is the profile id a link implies, for the default directory. A source
// that names no pack falls back to "pack".
func profileOr(source string) string {
	if profile, err := pack.SourceProfile(source); err == nil {
		return profile
	}
	return "pack"
}
