// The `pack` command materialises a Modrinth modpack as the directory
// `outpost publish` uploads. It is the only command that knows what Modrinth is:
// publish stays the thing that uploads a directory and verifies key sizes.
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"outpost/internal/pack"
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
	cmd.AddCommand(a.packFetchCmd())
	return cmd
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
	f.StringVar(&dir, "dir", "pack", "where to write the pack directory")
	f.StringVar(&version, "version", "", "pin a pack version (id or version number; default: newest release for Minecraft 1.20.1)")
	f.IntVar(&memoryGB, "memory", 4, "RAM in GB of the box you will rent; sizes start.sh and is checked by publish")
	f.BoolVar(&skipOptional, "skip-optional", false, "leave out files the pack marks optional for the server")
	f.BoolVar(&force, "force", false, "re-download files that already match their checksum")

	return cmd
}
