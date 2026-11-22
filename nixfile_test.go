package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadCopy(t *testing.T) *PkgFile {
	t.Helper()
	b, err := os.ReadFile("testdata/packages.nix")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "packages.nix")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadPkgFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseCategories(t *testing.T) {
	f := loadCopy(t)
	want := []string{"cliAndCore", "development", "desktopApps", "systemAndVm", "compatibility", "mediaAndGraphics"}
	got := f.CategoryNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	if c, it := f.Find("ripgrep"); c == nil || c.Name != "cliAndCore" || it.Opaque {
		t.Fatalf("ripgrep not found in cliAndCore: %v", c)
	}
	if c, _ := f.Find("zellij"); c == nil {
		t.Fatal("zellij should be found")
	}
	if c, _ := f.Find("nonexistent-pkg"); c != nil {
		t.Fatal("unexpected hit")
	}
}

func TestOpaqueEntries(t *testing.T) {
	f := loadCopy(t)
	opaque := 0
	for _, c := range f.Cats {
		for _, it := range c.Items {
			if it.Opaque {
				opaque++
			}
		}
	}
	// python3.withPackages, firefox.override, blender.overrideAttrs
	if opaque != 3 {
		t.Fatalf("opaque entries = %d, want 3", opaque)
	}
	if err := f.Remove("firefox"); err == nil {
		t.Fatal("opaque entries must not be removable")
	}
}

func TestAddAndRemoveRoundTrip(t *testing.T) {
	f := loadCopy(t)
	orig := strings.Join(f.Lines, "\n")

	if err := f.Add("desktopApps", "obsidian"); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.Find("obsidian"); c == nil || c.Name != "desktopApps" {
		t.Fatal("obsidian not added to desktopApps")
	}
	if err := f.Add("cliAndCore", "obsidian"); err == nil {
		t.Fatal("duplicate add should fail")
	}
	if err := f.Remove("obsidian"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.Lines, "\n"); got != orig {
		t.Fatalf("add+remove changed the file:\n%s", got)
	}
}

func TestRemoveFromSharedLine(t *testing.T) {
	f := loadCopy(t)
	if err := f.Remove("wget"); err != nil { // "vim wget curl git gh"
		t.Fatal(err)
	}
	if c, _ := f.Find("wget"); c != nil {
		t.Fatal("wget still present")
	}
	for _, other := range []string{"vim", "curl", "git", "gh"} {
		if c, _ := f.Find(other); c == nil {
			t.Fatalf("%s was lost", other)
		}
	}
}

func TestAddToListWithoutWithPkgs(t *testing.T) {
	f := loadCopy(t)
	if err := f.Add("mediaAndGraphics", "gimp"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.Lines, "\n")
	if !strings.Contains(joined, "    pkgs.gimp\n") {
		t.Fatalf("expected pkgs.gimp line:\n%s", joined)
	}
	if c, _ := f.Find("gimp"); c == nil {
		t.Fatal("gimp not found after add")
	}
}

func TestRejectsBadAttr(t *testing.T) {
	f := loadCopy(t)
	for _, bad := range []string{"", "two words", "a;b", "(x)", "evil\"x"} {
		if err := f.Add("cliAndCore", bad); err == nil {
			t.Fatalf("Add(%q) should fail", bad)
		}
	}
	if err := f.Add("nope", "gimp"); err == nil {
		t.Fatal("unknown category should fail")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	f := loadCopy(t)
	before, _ := os.ReadFile(f.Path)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(f.Path)
	if string(before) != string(after) {
		t.Fatal("Save without edits must not change the file")
	}
}

func TestSearchRanking(t *testing.T) {
	pk := []Pkg{
		{Attr: "ripgrep-all", Pname: "ripgrep-all", Desc: "rga"},
		{Attr: "ripgrep", Pname: "ripgrep", Desc: "fast grep"},
		{Attr: "ugrep", Pname: "ugrep", Desc: "a grep that mentions ripgrep"},
		{Attr: "firefox", Pname: "firefox", Desc: "browser"},
	}
	for i := range pk {
		pk[i].prepare()
	}
	got := Search(pk, "ripgrep", 10)
	if len(got) != 3 || got[0].Attr != "ripgrep" || got[1].Attr != "ripgrep-all" || got[2].Attr != "ugrep" {
		t.Fatalf("ranking wrong: %+v", got)
	}
	if got := Search(pk, "grep browser", 10); len(got) != 0 {
		t.Fatalf("all terms must match: %+v", got)
	}
	if got := Search(pk, "  ", 10); got != nil {
		t.Fatal("blank query returns nil")
	}
}

func TestParseIndexStreaming(t *testing.T) {
	doc := `{"version":2,"packages":{
	  "hello":{"pname":"hello","version":"2.12","meta":{"description":"Greeter","homepage":["https://a","https://b"]}},
	  "broken1":{"pname":"b","version":"1","meta":{"broken":true}},
	  "weird":{"pname":"w","version":"1","meta":{"description":null,"homepage":123}}
	}}`
	pk, err := parseIndex(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(pk) != 2 {
		t.Fatalf("got %d packages, want 2 (broken skipped): %+v", len(pk), pk)
	}
	if pk[0].Attr != "hello" || pk[0].Home != "https://a" || pk[0].Desc != "Greeter" {
		t.Fatalf("unexpected: %+v", pk[0])
	}
}
