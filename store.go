package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const storeDir = "/nix/store"

// StoreCopy is one realised store path of a package (one version/build).
type StoreCopy struct {
	Path     string
	Version  string
	Nar      int64
	InSystem bool // reachable from the running system
}

// PkgStore describes how a package from your config sits in the Nix store.
type PkgStore struct {
	Attr     string
	Name     string // ripgrep-15.1.0
	Stem     string // ripgrep
	Version  string
	Path     string // output path your current config would use
	Present  bool   // that path exists in the store
	Nar      int64  // size of the package alone
	Closure  int64  // size including all dependencies
	Copies   []StoreCopy
	Versions int   // distinct versions kept in the store
	Total    int64 // bytes across every copy
	Stale    int64 // bytes in copies the running system doesn't use
	Err      string
}

type StoreReport struct {
	Pkgs        []PkgStore
	StorePaths  int
	StoreBytes  int64
	SysPaths    int
	SysBytes    int64
	Generations int
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- resolving attributes to store paths --------------------------------

type resolved struct {
	Attr, Name, Stem, Version, Path string
}

var reAttr = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)

// ResolveAttrs asks the flake's own nixpkgs (the one your system is built
// with) what each attribute evaluates to.
func ResolveAttrs(cfg Config, attrs []string) (map[string]resolved, error) {
	var quoted []string
	for _, a := range attrs {
		if reAttr.MatchString(a) {
			quoted = append(quoted, strconv.Quote(a))
		}
	}
	out := map[string]resolved{}
	if len(quoted) == 0 {
		return out, nil
	}
	expr := `pkgs: map (a: let ` +
		`v = pkgs.lib.attrByPath (pkgs.lib.splitString "." a) null pkgs; ` +
		`r = builtins.tryEval (v.outPath or null); in ` +
		`{ attr = a; path = if r.success then r.value else null; ` +
		`name = v.name or null; pname = v.pname or null; version = v.version or null; }) [ ` +
		strings.Join(quoted, " ") + ` ]`
	text, err := run(cfg.Repo, "nix", "eval", "--json",
		fmt.Sprintf(".#nixosConfigurations.%s.pkgs", cfg.Host), "--apply", expr)
	if err != nil {
		return nil, fmt.Errorf("resolving packages: %s", firstLines(text, 4))
	}
	// nix may print warnings before the JSON array
	if i := strings.Index(text, "[{"); i > 0 {
		text = text[i:]
	} else if i := strings.Index(text, "[]"); i >= 0 {
		text = text[i:]
	}
	var rows []struct {
		Attr, Path, Name, Pname, Version *string
	}
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, fmt.Errorf("unexpected eval output: %v", err)
	}
	for _, r := range rows {
		if r.Attr == nil {
			continue
		}
		res := resolved{Attr: *r.Attr}
		if r.Path != nil {
			res.Path = *r.Path
		}
		if r.Name != nil {
			res.Name = *r.Name
		}
		if r.Version != nil {
			res.Version = *r.Version
		}
		res.Stem = stemOf(res.Name, res.Version)
		out[res.Attr] = res
	}
	return out, nil
}

// stemOf strips "-<version>" from a derivation name.
func stemOf(name, version string) string {
	if version != "" && strings.HasSuffix(name, "-"+version) {
		return strings.TrimSuffix(name, "-"+version)
	}
	// fall back to cutting at the first "-<digit>"
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '-' && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[:i]
		}
	}
	return name
}

// ---- sizes -----------------------------------------------------------

type SizeInfo struct {
	Nar, Closure, Download int64
}

// PathSizes queries `nix path-info`. With store != "" it asks that binary
// cache instead of the local store.
func PathSizes(paths []string, closure bool, store string) (map[string]SizeInfo, error) {
	out := map[string]SizeInfo{}
	for start := 0; start < len(paths); start += 200 {
		end := min(start+200, len(paths))
		args := []string{"path-info", "--json", "--json-format", "1"}
		if closure {
			args = append(args, "-S")
		}
		if store != "" {
			args = append(args, "--store", store)
		}
		args = append(args, paths[start:end]...)
		cmd := exec.Command("nix", args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("nix path-info: %s", firstLines(strings.TrimSpace(stderr.String()), 3))
		}
		var m map[string]struct {
			NarSize      int64 `json:"narSize"`
			ClosureSize  int64 `json:"closureSize"`
			DownloadSize int64 `json:"downloadSize"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return out, err
		}
		for p, v := range m {
			out[p] = SizeInfo{v.NarSize, v.ClosureSize, v.DownloadSize}
		}
	}
	return out, nil
}

// listSizes runs `nix path-info ... -s` and returns path -> NAR size.
func listSizes(args ...string) (map[string]int64, error) {
	cmd := exec.Command("nix", append([]string{"path-info", "-s"}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				m[f[0]] = n
			}
		}
	}
	return m, nil
}

// ---- finding copies in the store --------------------------------------

var outputSuffixes = map[string]bool{
	"bin": true, "dev": true, "man": true, "doc": true, "lib": true, "out": true,
	"info": true, "static": true, "debug": true, "terminfo": true, "dist": true,
	"devdoc": true, "lib32": true, "dev32": true, "nsswitch": true,
}

var reVersionTail = regexp.MustCompile(`^(rc|beta|alpha|pre|git|unstable|svn|hg)[0-9]*$`)

// versionish reports whether a "-"-separated token can be part of a version
// string (digits, dates, "unstable", "rc1", ...).
func versionish(tok string) bool {
	if tok == "" {
		return false
	}
	if tok[0] >= '0' && tok[0] <= '9' {
		return true
	}
	return reVersionTail.MatchString(tok)
}

// copyVersion reports whether store entry `name` (hash already removed) is a
// real build of the package with the given stem, and returns its version.
// Auxiliary derivations that share the prefix (bottles-63.2-init,
// ripgrep-15.2.0-vendor-staging, ...) are not copies and are rejected.
func copyVersion(stem, name string) (string, bool) {
	if stem == "" || !strings.HasPrefix(name, stem+"-") {
		return "", false
	}
	rest := name[len(stem)+1:]
	if rest == "" || rest[0] < '0' || rest[0] > '9' {
		return "", false
	}
	toks := strings.Split(rest, "-")
	n := 1
	for n < len(toks) && versionish(toks[n]) {
		n++
	}
	for _, t := range toks[n:] {
		if !outputSuffixes[t] {
			return "", false
		}
	}
	return strings.Join(toks[:n], "-"), true
}

// storeEntries lists realised store paths grouped by their hash-less name.
func storeEntries() (map[string][]string, int, error) {
	ents, err := os.ReadDir(storeDir)
	if err != nil {
		return nil, 0, err
	}
	byName := map[string][]string{}
	n := 0
	for _, e := range ents {
		s := e.Name()
		if len(s) < 34 || s[32] != '-' || strings.HasSuffix(s, ".drv") || strings.HasSuffix(s, ".lock") || strings.HasSuffix(s, ".chroot") {
			continue
		}
		n++
		byName[s[33:]] = append(byName[s[33:]], filepath.Join(storeDir, s))
	}
	return byName, n, nil
}

// Roots returns what keeps a store path alive (generations, profiles, results).
func Roots(path string) []string {
	cmd := exec.Command("nix-store", "-q", "--roots", path)
	b, _ := cmd.Output() // stderr carries unrelated temproot noise
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		root := strings.TrimSpace(strings.SplitN(line, " -> ", 2)[0])
		if root != "" && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	sort.Strings(out)
	return out
}

// ---- the scan ----------------------------------------------------------

func ScanStore(cfg Config, attrs []string) (*StoreReport, error) {
	var (
		wg       sync.WaitGroup
		res      map[string]resolved
		resErr   error
		allSizes map[string]int64
		sysSizes map[string]int64
		byName   map[string][]string
	)
	wg.Add(4)
	go func() { defer wg.Done(); res, resErr = ResolveAttrs(cfg, attrs) }()
	go func() { defer wg.Done(); allSizes, _ = listSizes("--all") }()
	go func() { defer wg.Done(); sysSizes, _ = listSizes("-r", "/run/current-system") }()
	go func() { defer wg.Done(); byName, _, _ = storeEntries() }()
	wg.Wait()
	if resErr != nil {
		return nil, resErr
	}

	rep := &StoreReport{StorePaths: len(allSizes), SysPaths: len(sysSizes)}
	for _, n := range allSizes {
		rep.StoreBytes += n
	}
	for _, n := range sysSizes {
		rep.SysBytes += n
	}
	if g, err := filepath.Glob("/nix/var/nix/profiles/system-*-link"); err == nil {
		rep.Generations = len(g)
	}

	// Gather every path whose size we need in one go.
	var wanted []string
	pkgs := make([]PkgStore, 0, len(attrs))
	for _, a := range attrs {
		r, ok := res[a]
		p := PkgStore{Attr: a, Name: r.Name, Stem: r.Stem, Version: r.Version, Path: r.Path}
		switch {
		case !ok || r.Name == "":
			p.Err = "not found in nixpkgs"
		case r.Path == "":
			p.Err = "unavailable (broken or unsupported)"
		default:
			if _, err := os.Stat(r.Path); err == nil {
				p.Present = true
			}
		}
		// all store copies of this package
		seen := map[string]bool{}
		for name, paths := range byName {
			ver, ok := copyVersion(p.Stem, name)
			if !ok {
				continue
			}
			for _, sp := range paths {
				p.Copies = append(p.Copies, StoreCopy{Path: sp, Version: ver, InSystem: hasKey(sysSizes, sp)})
				seen[ver] = true
				wanted = append(wanted, sp)
			}
		}
		p.Versions = len(seen)
		pkgs = append(pkgs, p)
	}

	// Own and closure size for the path your config uses.
	var mainPaths []string
	for _, p := range pkgs {
		if p.Present {
			mainPaths = append(mainPaths, p.Path)
		}
	}
	closure, err := PathSizes(mainPaths, true, "")
	if err != nil {
		return nil, err
	}
	for i := range pkgs {
		p := &pkgs[i]
		if p.Present {
			p.Nar, p.Closure = closure[p.Path].Nar, closure[p.Path].Closure
		}
		for j := range p.Copies {
			c := &p.Copies[j]
			c.Nar = allSizes[c.Path]
			p.Total += c.Nar
			if !c.InSystem {
				p.Stale += c.Nar
			}
		}
		sort.Slice(p.Copies, func(a, b int) bool {
			if p.Copies[a].InSystem != p.Copies[b].InSystem {
				return p.Copies[a].InSystem
			}
			return p.Copies[a].Version > p.Copies[b].Version
		})
	}
	rep.Pkgs = pkgs
	return rep, nil
}

func hasKey(m map[string]int64, k string) bool { _, ok := m[k]; return ok }

// ---- size of a package before installing it ----------------------------

type RemoteInfo struct {
	Path     string
	InStore  bool
	Nar      int64
	Closure  int64
	Download int64
}

// QueryRemote reports the size of a package as served by cache.nixos.org.
func QueryRemote(cfg Config, attr string) (*RemoteInfo, error) {
	res, err := ResolveAttrs(cfg, []string{attr})
	if err != nil {
		return nil, err
	}
	r, ok := res[attr]
	if !ok || r.Name == "" {
		return nil, fmt.Errorf("%s: not found in your nixpkgs", attr)
	}
	if r.Path == "" {
		return nil, fmt.Errorf("%s: unavailable (broken or unsupported platform)", attr)
	}
	info := &RemoteInfo{Path: r.Path}
	if _, err := os.Stat(r.Path); err == nil {
		info.InStore = true
	}
	sizes, err := PathSizes([]string{r.Path}, true, "https://cache.nixos.org")
	if err != nil {
		return info, fmt.Errorf("not in the binary cache (would be built from source)")
	}
	s := sizes[r.Path]
	info.Nar, info.Closure, info.Download = s.Nar, s.Closure, s.Download
	return info, nil
}

func (r *RemoteInfo) String() string {
	s := fmt.Sprintf("size %s · with dependencies %s", humanBytes(r.Nar), humanBytes(r.Closure))
	if r.Download > 0 {
		s += " · download " + humanBytes(r.Download)
	}
	if r.InStore {
		s += " · already in store"
	}
	return s
}

// ---- sorting -------------------------------------------------------------

const (
	sortClosure = iota
	sortSize
	sortCopies
	sortStale
	sortName
	sortModes
)

var sortLabels = []string{"closure", "size", "copies", "stale", "name"}

// sortStore orders packages for display; missing/unresolved ones sink.
func sortStore(p []PkgStore, mode int) {
	sort.SliceStable(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.Present != b.Present {
			return a.Present
		}
		switch mode {
		case sortSize:
			return a.Nar > b.Nar
		case sortCopies:
			return len(a.Copies) > len(b.Copies)
		case sortStale:
			return a.Stale > b.Stale
		case sortName:
			return a.Attr < b.Attr
		}
		return a.Closure > b.Closure
	})
}
