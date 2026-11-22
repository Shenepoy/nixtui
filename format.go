package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// nixfmt is bundled with the Nix package (see flake.nix); outside Nix it
// falls back to whatever is on PATH.
const formatter = "nixfmt"

func FormatterAvailable() bool {
	_, err := exec.LookPath(formatter)
	return err == nil
}

// FormatNix formats Nix source with nixfmt (RFC 166 style).
func FormatNix(src string) (string, error) {
	if !FormatterAvailable() {
		return "", errors.New("nixfmt not found (the Nix package bundles it; otherwise install nixfmt)")
	}
	cmd := exec.Command(formatter, "-")
	cmd.Stdin = strings.NewReader(src)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(firstLines(msg, 6))
	}
	return string(out), nil
}

// FormatFile rewrites a .nix file in place; reports whether it changed.
func FormatFile(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, err := FormatNix(string(b))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if out == string(b) {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// NixFiles lists every .nix file under repo (skipping .git and results).
func NixFiles(repo string) []string {
	var files []string
	_ = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "result" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".nix") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// FormatRepo formats all .nix files (or only checks them when check is true).
// It returns the files that were (or would be) changed.
func FormatRepo(repo string, check bool) (changed []string, err error) {
	for _, f := range NixFiles(repo) {
		if check {
			b, rerr := os.ReadFile(f)
			if rerr != nil {
				return changed, rerr
			}
			out, ferr := FormatNix(string(b))
			if ferr != nil {
				return changed, fmt.Errorf("%s: %w", f, ferr)
			}
			if out != string(b) {
				changed = append(changed, f)
			}
			continue
		}
		did, ferr := FormatFile(f)
		if ferr != nil {
			return changed, ferr
		}
		if did {
			changed = append(changed, f)
		}
	}
	return changed, nil
}
