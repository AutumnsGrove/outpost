// The `pack` command materialises a Modrinth modpack as the directory
// `outpost publish` uploads. It is the only command that knows what Modrinth is:
// publish stays the thing that uploads a directory and verifies key sizes.
package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"outpost/internal/pack"
	"outpost/internal/publish"
	"outpost/internal/r2"
	"outpost/internal/roster"
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
	cmd.AddCommand(a.packFetchCmd(), a.packListCmd(), a.packUseCmd())
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
				fmt.Printf("skipped %d file(s) the pack marks client-only or optional\n", len(res.Skipped))
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
