package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Config says where the NixOS flake lives and how to apply it.
type Config struct {
	Repo    string // working copy that nixtui edits
	Live    string // directory NixOS actually builds from (usually /etc/nixos)
	Host    string // nixosConfigurations.<Host>
	PkgFile string // module holding the package lists
	Channel string // nixpkgs channel for the package index
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	repo := os.Getenv("NIXTUI_REPO")
	if repo == "" {
		repo = filepath.Join(home, "nixos-config")
	}
	host := os.Getenv("NIXTUI_HOST")
	if host == "" {
		host = "nixos"
	}
	return Config{Repo: repo, Live: "/etc/nixos", Host: host}
}

var reChannel = regexp.MustCompile(`nixpkgs/(nixos-[0-9]+\.[0-9]+|nixos-unstable)`)

// finish fills defaults that depend on other fields.
func (c *Config) finish() {
	if c.PkgFile == "" {
		c.PkgFile = filepath.Join(c.Repo, "modules", "packages.nix")
	}
	if c.Channel == "" {
		c.Channel = "nixos-unstable"
		if b, err := os.ReadFile(filepath.Join(c.Repo, "flake.nix")); err == nil {
			if m := reChannel.FindSubmatch(b); m != nil {
				c.Channel = string(m[1])
			}
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ApplyScript syncs the working copy to the live flake and switches to it.
// It runs in the user's terminal so sudo can prompt for a password.
func (c Config) ApplyScript() string {
	return fmt.Sprintf(`set -e
cd %[1]s
git add -A
sudo rsync -a --exclude secrets --exclude archive --exclude result ./ %[2]s/
sudo nixos-rebuild switch --flake %[2]s#%[3]s`,
		shellQuote(c.Repo), shellQuote(c.Live), c.Host)
}

func (c Config) RollbackScript() string {
	return "sudo nixos-rebuild switch --rollback"
}

func (c Config) ApplyCmd() *exec.Cmd {
	return exec.Command("bash", "-c", c.ApplyScript())
}

// run executes a command in dir and returns its combined output.
func run(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// ValidateNix returns a syntax error message for a .nix file, or "".
func ValidateNix(path string) string {
	out, err := run(filepath.Dir(path), "nix-instantiate", "--parse", path)
	if err != nil {
		return firstLines(out, 6)
	}
	return ""
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (c Config) DryBuild() (string, error) {
	if _, err := run(c.Repo, "git", "add", "-A"); err != nil {
		return "git add failed", err
	}
	return run(c.Repo, "nix", "build", "--dry-run", "--no-link",
		fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", c.Host))
}

func (c Config) Diff() string {
	if _, err := run(c.Repo, "git", "rev-parse", "--git-dir"); err != nil {
		return "Not a git repository: " + c.Repo + "\nRun `git init` there to track changes, see diffs and commit from nixtui."
	}
	status, err := run(c.Repo, "git", "status", "--short")
	if err != nil {
		return "git status failed:\n" + status
	}
	// A repo with no commits yet has no HEAD to diff against.
	diff, err := run(c.Repo, "git", "diff", "HEAD", "--no-color")
	if err != nil {
		diff, _ = run(c.Repo, "git", "diff", "--no-color")
	}
	if status == "" && diff == "" {
		return "No uncommitted changes."
	}
	return status + "\n\n" + diff
}
