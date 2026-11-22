package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Update checking.
//
// nixtui asks GitHub once a day for the latest release tag. The request is a
// plain unauthenticated GET that carries nothing but a generic User-Agent. The
// answer is cached; set NIXTUI_NO_UPDATE_CHECK=1 (or --no-update-check) to turn
// it off. Development builds (version "dev") never check.

const repoSlug = "Shenepoy/nixtui"

var (
	updateAPI   = "https://api.github.com/repos/" + repoSlug + "/releases/latest"
	releasesURL = "https://github.com/" + repoSlug + "/releases"
)

const (
	updateTTL     = 24 * time.Hour
	updateFailTTL = 6 * time.Hour
)

type updateCache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"` // empty when the last check failed
}

func updateCheckDisabled() bool {
	return version == "dev" || os.Getenv("NIXTUI_NO_UPDATE_CHECK") != ""
}

func updateCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "nixtui", "update.json")
}

func readUpdateCache() (updateCache, bool) {
	var c updateCache
	p := updateCachePath()
	if p == "" {
		return c, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, false
	}
	return c, true
}

func writeUpdateCache(c updateCache) {
	p := updateCachePath()
	if p == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// compareVersions compares calendar versions like 26.11.0 numerically.
// It returns -1, 0 or 1.
func compareVersions(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// fetchLatest asks GitHub for the newest release (no cache).
func fetchLatest() (string, error) {
	url := updateAPI
	if u := os.Getenv("NIXTUI_UPDATE_URL"); u != "" { // mirrors and testing
		url = u
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "nixtui-update-check")
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup: %s", resp.Status)
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	v := strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v")
	if v == "" {
		return "", errors.New("release has no tag")
	}
	return v, nil
}

// LatestVersion returns the newest released version, using the daily cache.
// With network == false it only reads the cache and never blocks.
func LatestVersion(network bool) (string, error) {
	c, ok := readUpdateCache()
	if ok {
		age := time.Since(c.Checked)
		if (c.Latest != "" && age < updateTTL) || (c.Latest == "" && age < updateFailTTL) {
			if c.Latest == "" {
				return "", errors.New("update check unavailable")
			}
			return c.Latest, nil
		}
	}
	if !network {
		if ok && c.Latest != "" {
			return c.Latest, nil
		}
		return "", errors.New("no cached update information")
	}
	latest, err := fetchLatest()
	writeUpdateCache(updateCache{Checked: time.Now(), Latest: latest})
	return latest, err
}

// UpdateAvailable reports a newer release than the running version, if known.
func UpdateAvailable(network bool) (string, bool) {
	if updateCheckDisabled() {
		return "", false
	}
	latest, err := LatestVersion(network)
	if err != nil || compareVersions(latest, version) <= 0 {
		return "", false
	}
	return latest, true
}

// ---- self update ---------------------------------------------------------

func assetName() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("self-update supports Linux only (this is %s)", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		return "nixtui-linux-x86_64", nil
	case "arm64":
		return "nixtui-linux-aarch64", nil
	}
	return "", fmt.Errorf("no release binary for %s", runtime.GOARCH)
}

// releaseBase is where release assets for a version are downloaded from.
func releaseBase(ver string) string {
	if b := os.Getenv("NIXTUI_BASE_URL"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return releasesURL + "/download/v" + strings.TrimPrefix(ver, "v")
}

func httpGetBytes(url string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// selfUpdate downloads the release binary from base, verifies its SHA-256
// against sha256sums.txt, and atomically replaces target.
func selfUpdate(target, base, asset string) error {
	sums, err := httpGetBytes(base+"/sha256sums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in sha256sums.txt", asset)
	}
	bin, err := httpGetBytes(base+"/"+asset, 256<<20)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", asset, err)
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s); not updating", asset, want, got)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".nixtui-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w (re-run the install script instead)", target, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

func cliUpdate(args []string) error {
	checkOnly := false
	for _, a := range args {
		switch a {
		case "--check":
			checkOnly = true
		default:
			return fmt.Errorf("unknown argument %q (usage: nixtui update [--check])", a)
		}
	}
	if version == "dev" {
		return errors.New("this is a development build; there is nothing to update")
	}
	latest, err := fetchLatest()
	if err != nil {
		return fmt.Errorf("could not check for updates: %w", err)
	}
	writeUpdateCache(updateCache{Checked: time.Now(), Latest: latest})
	if compareVersions(latest, version) <= 0 {
		fmt.Printf("nixtui %s is up to date.\n", version)
		return nil
	}
	fmt.Printf("nixtui %s is available (you have %s).\n", latest, version)

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	if strings.HasPrefix(exe, "/nix/store/") {
		fmt.Println("This copy is managed by Nix. Update it with one of:")
		fmt.Println("  nix profile upgrade nixtui")
		fmt.Println("  nix flake update nixtui    # if it is an input of your NixOS flake")
		return nil
	}
	if checkOnly {
		fmt.Println("Run: nixtui update")
		return nil
	}
	asset, err := assetName()
	if err != nil {
		return err
	}
	fmt.Printf("Downloading %s…\n", asset)
	if err := selfUpdate(exe, releaseBase(latest), asset); err != nil {
		return err
	}
	fmt.Printf("Updated %s to %s.\n", exe, latest)
	return nil
}
