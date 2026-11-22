package main

import "testing"

func TestCopyVersion(t *testing.T) {
	cases := []struct {
		stem, name string
		ver        string
		ok         bool
	}{
		{"ripgrep", "ripgrep-15.1.0", "15.1.0", true},
		{"ripgrep", "ripgrep-15.2.0-man", "15.2.0", true},
		{"ripgrep", "ripgrep-15.2.0-vendor-staging", "", false},
		{"ripgrep", "ripgrep-all-0.10.10", "", false},
		{"git", "git-lfs-3.5.1", "", false},
		{"git", "git-2.51.0-doc", "2.51.0", true},
		{"python3.13-pip", "python3.13-pip-25.3", "25.3", true},
		{"python3", "python3.13-pip-25.3", "", false},
		{"vim", "vim-full-9.1", "", false},
		{"nvd", "nvd-0.2.4", "0.2.4", true},
		{"foo", "foo-0-unstable-2024-08-01", "0-unstable-2024-08-01", true},
		{"bottles", "bottles-63.2-init", "", false},
		{"bottles", "bottles-63.2-fhsenv-rootfs", "", false},
		{"bottles", "bottles-63.2", "63.2", true},
		{"foo", "foo-2024-08-01", "2024-08-01", true},
		{"foo", "foo-1.0-rc1-bin", "1.0-rc1", true},
		{"", "anything-1", "", false},
	}
	for _, c := range cases {
		ver, ok := copyVersion(c.stem, c.name)
		if ok != c.ok || ver != c.ver {
			t.Errorf("copyVersion(%q, %q) = (%q, %v), want (%q, %v)", c.stem, c.name, ver, ok, c.ver, c.ok)
		}
	}
}

func TestStemOf(t *testing.T) {
	if got := stemOf("python3.13-pip-25.3", "25.3"); got != "python3.13-pip" {
		t.Errorf("got %q", got)
	}
	if got := stemOf("ripgrep-15.1.0", ""); got != "ripgrep" {
		t.Errorf("got %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 6535648: "6.2 MiB", 56865152: "54.2 MiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSortStore(t *testing.T) {
	p := []PkgStore{
		{Attr: "a", Present: true, Closure: 10, Nar: 1},
		{Attr: "b", Present: true, Closure: 30, Nar: 2},
		{Attr: "missing", Present: false, Closure: 99},
		{Attr: "c", Present: true, Closure: 20, Nar: 3},
	}
	sortStore(p, sortClosure)
	if p[0].Attr != "b" || p[1].Attr != "c" || p[3].Attr != "missing" {
		t.Fatalf("closure order wrong: %+v", p)
	}
	sortStore(p, sortSize)
	if p[0].Attr != "c" || p[3].Attr != "missing" {
		t.Fatalf("size order wrong: %+v", p)
	}
}
