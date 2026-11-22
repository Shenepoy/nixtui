package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// PkgFile is a parsed Nix module whose package lists look like
//
//	name = with pkgs; [
//	  a b c
//	  d
//	];
//
// It edits the text line by line, so everything it doesn't touch (comments,
// formatting, other options) is preserved byte for byte.
type PkgFile struct {
	Path       string
	Lines      []string
	Cats       []Category
	trailingNL bool
}

type Category struct {
	Name     string
	WithPkgs bool // list starts with `with pkgs;`, so bare attr names work
	Start    int  // line of `name = ... [`
	End      int  // line of `];`
	Indent   string
	Items    []Item
}

type Item struct {
	Text    string
	Line    int
	EndLine int
	Opaque  bool // parenthesised expression; shown but not edited
}

var (
	reCatStart = regexp.MustCompile(`^(\s*)(\w+)\s*=\s*(with\s+pkgs;\s*)?\[\s*(#.*)?$`)
	reCatEnd   = regexp.MustCompile(`^\s*\];?\s*$`)
)

func parenDelta(s string) int {
	return strings.Count(s, "(") - strings.Count(s, ")")
}

func splitComment(line string) (code, comment string) {
	if i := strings.Index(line, "#"); i >= 0 {
		return strings.TrimRight(line[:i], " \t"), line[i:]
	}
	return line, ""
}

func LoadPkgFile(path string) (*PkgFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &PkgFile{Path: path}
	f.trailingNL = strings.HasSuffix(string(b), "\n")
	f.Lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	f.parse()
	return f, nil
}

func (f *PkgFile) parse() {
	f.Cats = nil
	for i := 0; i < len(f.Lines); i++ {
		m := reCatStart.FindStringSubmatch(f.Lines[i])
		if m == nil {
			continue
		}
		cat := Category{Name: m[2], WithPkgs: m[3] != "", Start: i, End: -1}
		depth := 0
		var cur *Item
		for j := i + 1; j < len(f.Lines); j++ {
			line := f.Lines[j]
			if depth > 0 {
				cur.EndLine = j
				depth += parenDelta(line)
				if depth <= 0 {
					depth = 0
					cat.Items = append(cat.Items, *cur)
					cur = nil
				}
				continue
			}
			if reCatEnd.MatchString(line) {
				cat.End = j
				break
			}
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if cat.Indent == "" {
				cat.Indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			}
			if strings.HasPrefix(t, "(") {
				it := Item{Text: t, Line: j, EndLine: j, Opaque: true}
				if d := parenDelta(line); d > 0 {
					depth = d
					cur = &it
				} else {
					cat.Items = append(cat.Items, it)
				}
				continue
			}
			code, _ := splitComment(t)
			for _, tok := range strings.Fields(code) {
				cat.Items = append(cat.Items, Item{Text: tok, Line: j, EndLine: j})
			}
		}
		if cat.End < 0 {
			continue
		}
		if cat.Indent == "" {
			cat.Indent = reCatStart.FindStringSubmatch(f.Lines[i])[1] + "  "
		}
		f.Cats = append(f.Cats, cat)
		i = cat.End
	}
}

func (f *PkgFile) cat(name string) *Category {
	for i := range f.Cats {
		if f.Cats[i].Name == name {
			return &f.Cats[i]
		}
	}
	return nil
}

func (c *Category) key(attr string) string {
	if c.WithPkgs {
		return attr
	}
	return "pkgs." + strings.TrimPrefix(attr, "pkgs.")
}

// Find reports which list already contains attr.
func (f *PkgFile) Find(attr string) (*Category, *Item) {
	for ci := range f.Cats {
		c := &f.Cats[ci]
		want := c.key(attr)
		for ii := range c.Items {
			if !c.Items[ii].Opaque && c.Items[ii].Text == want {
				return c, &c.Items[ii]
			}
		}
	}
	return nil, nil
}

func (f *PkgFile) CategoryNames() []string {
	names := make([]string, len(f.Cats))
	for i, c := range f.Cats {
		names[i] = c.Name
	}
	return names
}

// Add appends attr on its own line at the end of the named list.
func (f *PkgFile) Add(catName, attr string) error {
	attr = strings.TrimSpace(attr)
	if attr == "" || strings.ContainsAny(attr, " \t()#;[]\"'") {
		return fmt.Errorf("%q is not a plain package attribute", attr)
	}
	if c, _ := f.Find(attr); c != nil {
		return fmt.Errorf("%s is already in %s", attr, c.Name)
	}
	c := f.cat(catName)
	if c == nil {
		return fmt.Errorf("no package list named %q (have: %s)", catName, strings.Join(f.CategoryNames(), ", "))
	}
	line := c.Indent + c.key(attr)
	f.Lines = append(f.Lines[:c.End], append([]string{line}, f.Lines[c.End:]...)...)
	f.parse()
	return nil
}

// Remove deletes attr from whichever list holds it. Parenthesised entries
// can't be removed here; edit the file for those.
func (f *PkgFile) Remove(attr string) error {
	c, it := f.Find(attr)
	if c == nil {
		return fmt.Errorf("%s is not in any package list", attr)
	}
	line := f.Lines[it.Line]
	code, comment := splitComment(line)
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	var keep []string
	removed := false
	for _, tok := range strings.Fields(code) {
		if !removed && tok == it.Text {
			removed = true
			continue
		}
		keep = append(keep, tok)
	}
	switch {
	case len(keep) == 0 && comment == "":
		f.Lines = append(f.Lines[:it.Line], f.Lines[it.Line+1:]...)
	case len(keep) == 0:
		f.Lines[it.Line] = indent + comment
	case comment == "":
		f.Lines[it.Line] = indent + strings.Join(keep, " ")
	default:
		f.Lines[it.Line] = indent + strings.Join(keep, " ") + "  " + comment
	}
	f.parse()
	return nil
}

func (f *PkgFile) Save() error {
	out := strings.Join(f.Lines, "\n")
	if f.trailingNL {
		out += "\n"
	}
	return os.WriteFile(f.Path, []byte(out), 0o644)
}
