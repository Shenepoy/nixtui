package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

// Pkg is one nixpkgs package from the channel's packages.json index.
type Pkg struct {
	Attr    string `json:"a"`
	Pname   string `json:"n,omitempty"`
	Version string `json:"v,omitempty"`
	Desc    string `json:"d,omitempty"`
	Home    string `json:"h,omitempty"`

	lattr, lname, ldesc string
}

func (p *Pkg) prepare() {
	p.lattr = strings.ToLower(p.Attr)
	p.lname = strings.ToLower(p.Pname)
	p.ldesc = strings.ToLower(p.Desc)
}

const indexMaxAge = 7 * 24 * time.Hour

func indexURL(channel string) string {
	return "https://channels.nixos.org/" + channel + "/packages.json.br"
}

func cachePath(channel string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nixtui", "index-"+channel+".json"), nil
}

// LoadIndex returns the package index for a channel. It uses a slim on-disk
// cache (refreshed weekly) so the 360 MB upstream file is parsed only once.
func LoadIndex(channel string) ([]Pkg, error) {
	path, err := cachePath(channel)
	if err != nil {
		return nil, err
	}
	st, statErr := os.Stat(path)
	if statErr == nil && time.Since(st.ModTime()) < indexMaxAge {
		if pk, err := readSlim(path); err == nil {
			return pk, nil
		}
	}
	pk, err := downloadIndex(channel)
	if err != nil {
		if statErr == nil { // fall back to a stale cache
			if old, e2 := readSlim(path); e2 == nil {
				return old, nil
			}
		}
		return nil, err
	}
	_ = writeSlim(path, pk)
	return pk, nil
}

func readSlim(path string) ([]Pkg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var pk []Pkg
	if err := json.NewDecoder(bufio.NewReaderSize(f, 1<<20)).Decode(&pk); err != nil {
		return nil, err
	}
	for i := range pk {
		pk[i].prepare()
	}
	return pk, nil
}

func writeSlim(path string, pk []Pkg) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(pk); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// flex decodes a JSON string, or the first string of a JSON array.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flex(s)
		return nil
	}
	var a []string
	if json.Unmarshal(b, &a) == nil && len(a) > 0 {
		*f = flex(a[0])
	}
	return nil
}

type rawPkg struct {
	Pname   string `json:"pname"`
	Version string `json:"version"`
	Meta    struct {
		Description flex `json:"description"`
		Homepage    flex `json:"homepage"`
		Broken      bool `json:"broken"`
	} `json:"meta"`
}

func downloadIndex(channel string) ([]Pkg, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(indexURL(channel))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("index download for %q: %s", channel, resp.Status)
	}
	return parseIndex(brotli.NewReader(resp.Body))
}

// parseIndex streams the {"packages": {...}} document and keeps only the
// fields the UI needs.
func parseIndex(r io.Reader) ([]Pkg, error) {
	dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	if _, err := dec.Token(); err != nil { // {
		return nil, err
	}
	var out []Pkg
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key, _ := keyTok.(string); key != "packages" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := dec.Token(); err != nil { // {
			return nil, err
		}
		for dec.More() {
			attrTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			attr, _ := attrTok.(string)
			var rp rawPkg
			if err := dec.Decode(&rp); err != nil {
				var ute *json.UnmarshalTypeError
				if errors.As(err, &ute) {
					continue
				}
				return nil, err
			}
			if rp.Meta.Broken {
				continue
			}
			p := Pkg{
				Attr:    attr,
				Pname:   rp.Pname,
				Version: rp.Version,
				Desc:    strings.TrimSpace(string(rp.Meta.Description)),
				Home:    string(rp.Meta.Homepage),
			}
			p.prepare()
			out = append(out, p)
		}
		if _, err := dec.Token(); err != nil { // }
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("package index was empty")
	}
	return out, nil
}

// Search ranks packages: exact attr > attr prefix > attr substring > name >
// description. Every whitespace-separated term must match somewhere.
func Search(pk []Pkg, query string, limit int) []Pkg {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		p     *Pkg
		score int
	}
	var hits []scored
	for i := range pk {
		p := &pk[i]
		total := 0
		for _, t := range terms {
			s := 0
			switch {
			case p.lattr == t:
				s = 1000
			case strings.HasPrefix(p.lattr, t):
				s = 500
			case strings.Contains(p.lattr, t):
				s = 300
			case strings.Contains(p.lname, t):
				s = 200
			case strings.Contains(p.ldesc, t):
				s = 50
			}
			if s == 0 {
				total = 0
				break
			}
			total += s
		}
		if total > 0 {
			// Prefer top-level packages over nested sets (emacsPackages.x,
			// vimPlugins.x, ...), which are rarely what you want in systemPackages.
			if strings.Contains(p.Attr, ".") {
				total -= 100
			}
			hits = append(hits, scored{p, total})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if len(hits[i].p.Attr) != len(hits[j].p.Attr) {
			return len(hits[i].p.Attr) < len(hits[j].p.Attr)
		}
		return hits[i].p.Attr < hits[j].p.Attr
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Pkg, len(hits))
	for i, h := range hits {
		out[i] = *h.p
	}
	return out
}
