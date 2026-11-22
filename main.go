package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// version is set at build time: -ldflags "-X main.version=26.11.0".
var version = "dev"

const usage = `nixtui - search nixpkgs, add packages to your NixOS flake, edit it, apply it.

Usage:
  nixtui [flags]                      open the TUI
  nixtui search <terms...>            search packages
  nixtui add <attr> [-c list] [--apply]
                                      add a package to a list in packages.nix
  nixtui remove <attr> [--apply]      remove a package
  nixtui list                         show package lists and their entries
  nixtui store                        size of every package, versions kept in the store
  nixtui info <attr>                  size of a package before installing it (binary cache)
  nixtui fmt [--check] [files...]     format .nix files with the bundled nixfmt
  nixtui apply                        sync to the live flake and nixos-rebuild switch
  nixtui --version

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
	showVersion := fs.Bool("version", false, "")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("nixtui", version)
		return
	}
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
	case "store", "sizes":
		err = cliStore(cfg)
	case "info":
		err = cliInfo(cfg, args[1:])
	case "fmt", "format":
		err = cliFmt(cfg, args[1:])
	case "apply":
		err = cliApply(cfg)
	case "version":
		fmt.Println("nixtui", version)
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

func installedAttrs(cfg Config) ([]string, error) {
	pf, err := LoadPkgFile(cfg.PkgFile)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var attrs []string
	for _, c := range pf.Cats {
		for _, it := range c.Items {
			a := strings.TrimPrefix(it.Text, "pkgs.")
			if !it.Opaque && !seen[a] {
				seen[a] = true
				attrs = append(attrs, a)
			}
		}
	}
	return attrs, nil
}

func cliStore(cfg Config) error {
	attrs, err := installedAttrs(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "scanning the store…")
	rep, err := ScanStore(cfg, attrs)
	if err != nil {
		return err
	}
	fmt.Printf("store: %d paths, %s | this system: %d paths, %s | %d generations\n\n",
		rep.StorePaths, humanBytes(rep.StoreBytes), rep.SysPaths, humanBytes(rep.SysBytes), rep.Generations)
	pk := append([]PkgStore(nil), rep.Pkgs...)
	sortStore(pk, sortClosure)
	fmt.Printf("%-32s %10s %10s %6s %5s %10s\n", "PACKAGE", "SIZE", "CLOSURE", "COPIES", "VERS", "STALE")
	for _, p := range pk {
		if p.Err != "" || !p.Present {
			note := p.Err
			if note == "" {
				note = "not in store yet (apply to install)"
			}
			fmt.Printf("%-32s %s\n", p.Attr, note)
			continue
		}
		fmt.Printf("%-32s %10s %10s %6d %5d %10s\n", p.Attr, humanBytes(p.Nar), humanBytes(p.Closure),
			len(p.Copies), p.Versions, humanBytes(p.Stale))
	}
	return nil
}

func cliInfo(cfg Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: nixtui info <attr>")
	}
	info, err := QueryRemote(cfg, args[0])
	if info != nil && err == nil {
		fmt.Printf("%s: %s\n", args[0], info)
	}
	return err
}

func cliFmt(cfg Config, args []string) error {
	check := false
	var files []string
	for _, a := range args {
		if a == "--check" {
			check = true
		} else {
			files = append(files, a)
		}
	}
	var changed []string
	var err error
	if len(files) == 0 {
		changed, err = FormatRepo(cfg.Repo, check)
	} else {
		for _, f := range files {
			if check {
				b, rerr := os.ReadFile(f)
				if rerr != nil {
					return rerr
				}
				out, ferr := FormatNix(string(b))
				if ferr != nil {
					return ferr
				}
				if out != string(b) {
					changed = append(changed, f)
				}
				continue
			}
			did, ferr := FormatFile(f)
			if ferr != nil {
				return ferr
			}
			if did {
				changed = append(changed, f)
			}
		}
	}
	for _, f := range changed {
		if check {
			fmt.Println("needs formatting:", f)
		} else {
			fmt.Println("formatted:", f)
		}
	}
	if err != nil {
		return err
	}
	if check && len(changed) > 0 {
		os.Exit(1)
	}
	if len(changed) == 0 {
		fmt.Println("all files already formatted")
	}
	return nil
}
