package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const usage = `nixtui - search nixpkgs, add packages to your NixOS flake, edit it, apply it.

Usage:
  nixtui [flags]                      open the TUI
  nixtui search <terms...>            search packages
  nixtui add <attr> [-c list] [--apply]
                                      add a package to a list in packages.nix
  nixtui remove <attr> [--apply]      remove a package
  nixtui list                         show package lists and their entries
  nixtui apply                        sync to the live flake and nixos-rebuild switch

Flags:
  --repo DIR      flake working copy to edit   (env NIXTUI_REPO, default ~/nixos-config)
  --live DIR      directory NixOS builds from  (default /etc/nixos)
  --host NAME     nixosConfigurations name     (env NIXTUI_HOST, default nixos)
  --file PATH     package module               (default <repo>/modules/packages.nix)
  --channel NAME  index channel                (default: read from flake.nix)
`

func main() {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("nixtui", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.StringVar(&cfg.Repo, "repo", cfg.Repo, "")
	fs.StringVar(&cfg.Live, "live", cfg.Live, "")
	fs.StringVar(&cfg.Host, "host", cfg.Host, "")
	fs.StringVar(&cfg.PkgFile, "file", "", "")
	fs.StringVar(&cfg.Channel, "channel", "", "")
	_ = fs.Parse(os.Args[1:])
	cfg.finish()

	args := fs.Args()
	if len(args) == 0 {
		if err := runTUI(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "nixtui:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch args[0] {
	case "search":
		err = cliSearch(cfg, args[1:])
	case "add":
		err = cliAdd(cfg, args[1:])
	case "remove", "rm":
		err = cliRemove(cfg, args[1:])
	case "list", "ls":
		err = cliList(cfg)
	case "apply":
		err = cliApply(cfg)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nixtui:", err)
		os.Exit(1)
	}
}

func cliSearch(cfg Config, terms []string) error {
	if len(terms) == 0 {
		return fmt.Errorf("usage: nixtui search <terms...>")
	}
	idx, err := LoadIndex(cfg.Channel)
	if err != nil {
		return err
	}
	for _, p := range Search(idx, strings.Join(terms, " "), 20) {
		fmt.Printf("%-32s %-12s %s\n", p.Attr, p.Version, p.Desc)
	}
	return nil
}

// parseEditArgs pulls `-c list` and `--apply` out of args in any order.
func parseEditArgs(args []string) (attr, list string, apply bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--apply":
			apply = true
		case a == "-c" || a == "--category":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("%s needs a value", a)
			}
			i++
			list = args[i]
		case strings.HasPrefix(a, "-"):
			return "", "", false, fmt.Errorf("unknown flag %s", a)
		case attr == "":
			attr = a
		default:
			return "", "", false, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if attr == "" {
		return "", "", false, fmt.Errorf("missing package attribute")
	}
	return attr, list, apply, nil
}

func cliAdd(cfg Config, args []string) error {
	attr, list, apply, err := parseEditArgs(args)
	if err != nil {
		return err
	}
	pf, err := LoadPkgFile(cfg.PkgFile)
	if err != nil {
		return err
	}
	if list == "" {
		if len(pf.Cats) == 0 {
			return fmt.Errorf("%s has no package lists", cfg.PkgFile)
		}
		list = pf.Cats[0].Name
		fmt.Fprintf(os.Stderr, "no -c given; using list %q (available: %s)\n", list, strings.Join(pf.CategoryNames(), ", "))
	}
	if err := pf.Add(list, attr); err != nil {
		return err
	}
	if err := pf.Save(); err != nil {
		return err
	}
	fmt.Printf("added %s to %s in %s\n", attr, list, cfg.PkgFile)
	if apply {
		return cliApply(cfg)
	}
	fmt.Println("run `nixtui apply` to rebuild")
	return nil
}

func cliRemove(cfg Config, args []string) error {
	attr, _, apply, err := parseEditArgs(args)
	if err != nil {
		return err
	}
	pf, err := LoadPkgFile(cfg.PkgFile)
	if err != nil {
		return err
	}
	if err := pf.Remove(attr); err != nil {
		return err
	}
	if err := pf.Save(); err != nil {
		return err
	}
	fmt.Printf("removed %s from %s\n", attr, cfg.PkgFile)
	if apply {
		return cliApply(cfg)
	}
	return nil
}

func cliList(cfg Config) error {
	pf, err := LoadPkgFile(cfg.PkgFile)
	if err != nil {
		return err
	}
	for _, c := range pf.Cats {
		fmt.Printf("%s (%d)\n", c.Name, len(c.Items))
		for _, it := range c.Items {
			fmt.Printf("  %s\n", it.Text)
		}
	}
	return nil
}

func cliApply(cfg Config) error {
	cmd := exec.Command("bash", "-c", cfg.ApplyScript())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
