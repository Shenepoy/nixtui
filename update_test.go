package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"26.11.0", "26.11.0", 0},
		{"26.11.1", "26.11.0", 1},
		{"26.12.0", "26.11.9", 1},
		{"27.01.0", "26.12.9", 1},
		{"26.11.0", "26.11.1", -1},
		{"v26.11.0", "26.11.0", 0},
		{"26.11", "26.11.0", 0},
		{"26.11.10", "26.11.9", 1}, // numeric, not lexical
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// withUpdateEnv isolates the cache dir, the running version and the API URL.
func withUpdateEnv(t *testing.T, current, apiURL string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("NIXTUI_NO_UPDATE_CHECK", "")
	oldV, oldAPI := version, updateAPI
	version, updateAPI = current, apiURL
	t.Cleanup(func() { version, updateAPI = oldV, oldAPI })
}

func releaseServer(t *testing.T, tag string, status int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if status != http.StatusOK {
			http.Error(w, "nope", status)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestUpdateAvailableAndCache(t *testing.T) {
	srv, hits := releaseServer(t, "v26.12.0", http.StatusOK)
	withUpdateEnv(t, "26.11.0", srv.URL)

	latest, ok := UpdateAvailable(true)
	if !ok || latest != "26.12.0" {
		t.Fatalf("UpdateAvailable = %q, %v; want 26.12.0, true", latest, ok)
	}
	// Second call (and a cache-only call) must not hit the network again.
	if _, ok := UpdateAvailable(true); !ok {
		t.Fatal("cached result lost")
	}
	if latest, ok := UpdateAvailable(false); !ok || latest != "26.12.0" {
		t.Fatalf("cache-only lookup failed: %q %v", latest, ok)
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Fatalf("server hit %d times, want 1 (daily cache)", n)
	}
}

func TestNoUpdateWhenCurrent(t *testing.T) {
	srv, _ := releaseServer(t, "v26.11.0", http.StatusOK)
	withUpdateEnv(t, "26.11.0", srv.URL)
	if latest, ok := UpdateAvailable(true); ok {
		t.Fatalf("same version reported as update: %q", latest)
	}
	srv2, _ := releaseServer(t, "v26.10.0", http.StatusOK)
	updateAPI = srv2.URL
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if latest, ok := UpdateAvailable(true); ok {
		t.Fatalf("older release reported as update: %q", latest)
	}
}

func TestFailureIsQuietAndCached(t *testing.T) {
	srv, hits := releaseServer(t, "", http.StatusNotFound) // e.g. private repo
	withUpdateEnv(t, "26.11.0", srv.URL)
	if _, ok := UpdateAvailable(true); ok {
		t.Fatal("a failed lookup must not report an update")
	}
	if _, ok := UpdateAvailable(true); ok {
		t.Fatal("second lookup must not report an update either")
	}
	if n := atomic.LoadInt32(hits); n != 1 {
		t.Fatalf("failure not cached: %d requests", n)
	}
}

func TestUpdateCheckDisabled(t *testing.T) {
	srv, hits := releaseServer(t, "v99.01.0", http.StatusOK)
	withUpdateEnv(t, "26.11.0", srv.URL)

	t.Setenv("NIXTUI_NO_UPDATE_CHECK", "1")
	if _, ok := UpdateAvailable(true); ok {
		t.Fatal("opt-out ignored")
	}
	t.Setenv("NIXTUI_NO_UPDATE_CHECK", "")
	version = "dev"
	if _, ok := UpdateAvailable(true); ok {
		t.Fatal("dev builds must not check")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Fatalf("network used while disabled: %d requests", n)
	}
}

func assetServer(t *testing.T, asset string, body []byte, sums string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + asset:
			_, _ = w.Write(body)
		case "/sha256sums.txt":
			_, _ = w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSelfUpdate(t *testing.T) {
	const asset = "nixtui-linux-x86_64"
	newBin := []byte("#!/bin/sh\necho new\n")
	sum := sha256.Sum256(newBin)
	good := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	dir := t.TempDir()
	target := filepath.Join(dir, "nixtui")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// wrong checksum: target must stay untouched
	bad := assetServer(t, asset, newBin, fmt.Sprintf("%064d  %s\n", 0, asset))
	if err := selfUpdate(target, bad.URL, asset); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("target modified after a failed update: %q", b)
	}

	// asset missing from the checksum list
	missing := assetServer(t, asset, newBin, "abc  other-file\n")
	if err := selfUpdate(target, missing.URL, asset); err == nil {
		t.Fatal("unlisted asset accepted")
	}

	// good update replaces the file, keeps it executable, leaves no temp files
	ok := assetServer(t, asset, newBin, good)
	if err := selfUpdate(target, ok.URL, asset); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != string(newBin) {
		t.Fatalf("target not replaced: %q", b)
	}
	if st, _ := os.Stat(target); st.Mode()&0o111 == 0 {
		t.Fatal("updated binary is not executable")
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".nixtui-update-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}
