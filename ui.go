package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tab int

const (
	tabSearch tab = iota
	tabInstalled
	tabFiles
	tabChanges
)

var tabNames = []string{"Search", "Installed", "Files", "Changes"}

var (
	cAccent = lipgloss.Color("#89b4fa")
	cDim    = lipgloss.Color("#6c7086")
	cGood   = lipgloss.Color("#a6e3a1")
	cBad    = lipgloss.Color("#f38ba8")
	cWarn   = lipgloss.Color("#f9e2af")

	sTabOn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1e1e2e")).Background(cAccent).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sGood    = lipgloss.NewStyle().Foreground(cGood)
	sBad     = lipgloss.NewStyle().Foreground(cBad)
	sWarn    = lipgloss.NewStyle().Foreground(cWarn)
	sSel     = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTitle   = lipgloss.NewStyle().Bold(true)
	sKey     = lipgloss.NewStyle().Foreground(cAccent)
	sHunk    = lipgloss.NewStyle().Foreground(lipgloss.Color("#74c7ec"))
	sNameCol = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
)

// ---- messages ---------------------------------------------------------

type indexMsg struct {
	pkgs []Pkg
	err  error
}

type cmdMsg struct {
	title string
	out   string
	err   error
}

type execMsg struct {
	title string
	err   error
}

// ---- model ------------------------------------------------------------

type instRow struct {
	cat  string
	item Item
}

type model struct {
	cfg  Config
	w, h int
	tab  tab

	idx     []Pkg
	loading bool
	spin    spinner.Model
	idxErr  error

	input   textinput.Model
	results []Pkg
	cur     int
	off     int

	pf      *PkgFile
	pfErr   error
	rows    []instRow
	instCur int
	instOff int

	picking bool
	pickPkg string
	pickCur int

	files   []string
	fileCur int
	fileOff int

	editing   bool
	editor    textarea.Model
	editPath  string
	editOrig  string
	discardOK bool

	vp         viewport.Model
	changesTxt string

	committing  bool
	commitInput textinput.Model

	busy  string
	flash string
	bad   bool
}

func runTUI(cfg Config) error {
	m := newModel(cfg)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel(cfg Config) model {
	in := textinput.New()
	in.Placeholder = "search nixpkgs (e.g. ripgrep, \"video editor\")"
	in.Prompt = "🔍 "
	in.Focus()
	in.CharLimit = 120

	ci := textinput.New()
	ci.Prompt = "commit message: "
	ci.CharLimit = 200

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.Prompt = ""

	m := model{
		cfg: cfg, input: in, commitInput: ci, spin: sp, editor: ta,
		loading: true, vp: viewport.New(80, 20),
	}
	m.reloadPkgFile()
	m.reloadFiles()
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spin.Tick, loadIndexCmd(m.cfg.Channel), m.refreshChangesCmd())
}

func loadIndexCmd(channel string) tea.Cmd {
	return func() tea.Msg {
		pk, err := LoadIndex(channel)
		return indexMsg{pk, err}
	}
}

func (m model) refreshChangesCmd() tea.Cmd {
	return func() tea.Msg {
		return cmdMsg{title: "changes", out: m.cfg.Diff()}
	}
}

func (m *model) say(s string)  { m.flash, m.bad = s, false }
func (m *model) fail(s string) { m.flash, m.bad = s, true }

func (m *model) reloadPkgFile() {
	pf, err := LoadPkgFile(m.cfg.PkgFile)
	m.pf, m.pfErr = pf, err
	m.rows = nil
	if pf != nil {
		for _, c := range pf.Cats {
			for _, it := range c.Items {
				m.rows = append(m.rows, instRow{c.Name, it})
			}
		}
	}
	if m.instCur >= len(m.rows) {
		m.instCur = max(0, len(m.rows)-1)
	}
}

func (m *model) reloadFiles() {
	m.files = nil
	_ = filepath.WalkDir(m.cfg.Repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "result" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(m.cfg.Repo, p); err == nil {
			m.files = append(m.files, rel)
		}
		return nil
	})
	sort.Strings(m.files)
	if m.fileCur >= len(m.files) {
		m.fileCur = max(0, len(m.files)-1)
	}
}

func (m *model) layout() {
	bodyH := m.bodyHeight()
	m.vp.Width = m.w
	m.vp.Height = max(3, bodyH-1)
	m.editor.SetWidth(max(20, m.w-2))
	m.editor.SetHeight(max(3, bodyH-1))
	m.input.Width = max(10, m.w-6)
	m.commitInput.Width = max(10, m.w-20)
}

func (m model) bodyHeight() int { return max(5, m.h-3) }

// ---- update -----------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		m.vp.SetContent(m.colorDiff(m.changesTxt))
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case indexMsg:
		m.loading = false
		if msg.err != nil {
			m.idxErr = msg.err
			m.fail("package index failed: " + msg.err.Error())
			return m, nil
		}
		m.idx = msg.pkgs
		m.say(fmt.Sprintf("index ready: %d packages (%s)", len(m.idx), m.cfg.Channel))
		m.results = Search(m.idx, m.input.Value(), 200)
		return m, nil

	case cmdMsg:
		m.busy = ""
		switch msg.title {
		case "changes":
			m.changesTxt = msg.out
		default:
			m.changesTxt = msg.title + "\n\n" + msg.out
			if msg.err != nil {
				m.fail(msg.title + " failed")
			} else {
				m.say(msg.title + " ok")
			}
			m.tab = tabChanges
		}
		m.vp.SetContent(m.colorDiff(m.changesTxt))
		m.vp.GotoTop()
		return m, nil

	case execMsg:
		m.busy = ""
		if msg.err != nil {
			m.fail(msg.title + " failed: " + msg.err.Error())
		} else {
			m.say(msg.title + " finished")
		}
		m.reloadPkgFile()
		return m, m.refreshChangesCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Anything else (cursor blink etc.) goes to the focused widget.
	var cmd tea.Cmd
	switch {
	case m.editing:
		m.editor, cmd = m.editor.Update(msg)
	case m.committing:
		m.commitInput, cmd = m.commitInput.Update(msg)
	case m.tab == tabSearch:
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	switch {
	case m.editing:
		return m.keyEditor(msg)
	case m.committing:
		return m.keyCommit(msg)
	case m.picking:
		return m.keyPicker(msg)
	}

	// Global keys (not while typing a search).
	switch key {
	case "tab":
		m.setTab((m.tab + 1) % 4)
		return m, nil
	case "shift+tab":
		m.setTab((m.tab + 3) % 4)
		return m, nil
	case "ctrl+a":
		return m.startApply()
	}

	switch m.tab {
	case tabSearch:
		return m.keySearch(msg)
	case tabInstalled:
		return m.keyInstalled(msg)
	case tabFiles:
		return m.keyFiles(msg)
	default:
		return m.keyChanges(msg)
	}
}

func (m *model) setTab(t tab) {
	m.tab = t
	if t == tabSearch {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
	if t == tabFiles {
		m.reloadFiles()
	}
}

func (m model) startApply() (tea.Model, tea.Cmd) {
	m.busy = "applying"
	cmd := exec.Command("bash", "-c", m.cfg.ApplyScript()+
		"\nstatus=$?\necho\nread -rp 'Press enter to return to nixtui… ' _\nexit $status")
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return execMsg{"apply", err} })
}

// -- search tab

func (m model) keySearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.SetValue("")
		m.results, m.cur, m.off = nil, 0, 0
		return m, nil
	case "up", "ctrl+p":
		m.moveCursor(&m.cur, &m.off, -1, len(m.results), m.searchListHeight())
		return m, nil
	case "down", "ctrl+n":
		m.moveCursor(&m.cur, &m.off, 1, len(m.results), m.searchListHeight())
		return m, nil
	case "pgup":
		m.moveCursor(&m.cur, &m.off, -10, len(m.results), m.searchListHeight())
		return m, nil
	case "pgdown":
		m.moveCursor(&m.cur, &m.off, 10, len(m.results), m.searchListHeight())
		return m, nil
	case "enter":
		if len(m.results) == 0 {
			return m, nil
		}
		return m.beginInstall(m.results[m.cur].Attr)
	}
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.results = Search(m.idx, m.input.Value(), 200)
		m.cur, m.off = 0, 0
	}
	return m, cmd
}

func (m model) beginInstall(attr string) (tea.Model, tea.Cmd) {
	if m.pf == nil {
		m.fail("can't read " + m.cfg.PkgFile)
		return m, nil
	}
	if c, _ := m.pf.Find(attr); c != nil {
		m.say(attr + " is already in " + c.Name)
		return m, nil
	}
	switch len(m.pf.Cats) {
	case 0:
		m.fail("no package lists found in " + m.cfg.PkgFile)
	case 1:
		return m.doAdd(m.pf.Cats[0].Name, attr), nil
	default:
		m.picking, m.pickPkg, m.pickCur = true, attr, 0
	}
	return m, nil
}

func (m model) doAdd(cat, attr string) model {
	if err := m.pf.Add(cat, attr); err != nil {
		m.fail(err.Error())
		return m
	}
	if err := m.pf.Save(); err != nil {
		m.fail("save failed: " + err.Error())
		return m
	}
	m.reloadPkgFile()
	m.say(fmt.Sprintf("added %s to %s — ctrl+a to apply", attr, cat))
	return m
}

func (m model) keyPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.pf.Cats)
	switch msg.String() {
	case "esc", "q":
		m.picking = false
	case "up", "k":
		m.pickCur = (m.pickCur + n - 1) % n
	case "down", "j":
		m.pickCur = (m.pickCur + 1) % n
	case "enter":
		m.picking = false
		m = m.doAdd(m.pf.Cats[m.pickCur].Name, m.pickPkg)
		return m, m.refreshChangesCmd()
	}
	return m, nil
}

// -- installed tab

func (m model) keyInstalled(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.bodyHeight() - 1
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(&m.instCur, &m.instOff, -1, len(m.rows), h)
	case "down", "j":
		m.moveCursor(&m.instCur, &m.instOff, 1, len(m.rows), h)
	case "pgup":
		m.moveCursor(&m.instCur, &m.instOff, -10, len(m.rows), h)
	case "pgdown":
		m.moveCursor(&m.instCur, &m.instOff, 10, len(m.rows), h)
	case "x", "delete", "d":
		if len(m.rows) == 0 {
			return m, nil
		}
		r := m.rows[m.instCur]
		if r.item.Opaque {
			m.fail("complex entry — press e to edit the file")
			return m, nil
		}
		attr := strings.TrimPrefix(r.item.Text, "pkgs.")
		if err := m.pf.Remove(attr); err != nil {
			m.fail(err.Error())
			return m, nil
		}
		if err := m.pf.Save(); err != nil {
			m.fail("save failed: " + err.Error())
			return m, nil
		}
		m.reloadPkgFile()
		m.say("removed " + attr + " — ctrl+a to apply")
		return m, m.refreshChangesCmd()
	case "e", "enter":
		if len(m.rows) == 0 {
			return m, nil
		}
		return m.openEditor(m.cfg.PkgFile, m.rows[m.instCur].item.Line)
	}
	return m, nil
}

// -- files tab

func (m model) keyFiles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.bodyHeight() - 1
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(&m.fileCur, &m.fileOff, -1, len(m.files), h)
	case "down", "j":
		m.moveCursor(&m.fileCur, &m.fileOff, 1, len(m.files), h)
	case "pgup":
		m.moveCursor(&m.fileCur, &m.fileOff, -10, len(m.files), h)
	case "pgdown":
		m.moveCursor(&m.fileCur, &m.fileOff, 10, len(m.files), h)
	case "enter", "e":
		if len(m.files) == 0 {
			return m, nil
		}
		return m.openEditor(filepath.Join(m.cfg.Repo, m.files[m.fileCur]), 0)
	}
	return m, nil
}

func (m model) openEditor(path string, line int) (tea.Model, tea.Cmd) {
	b, err := os.ReadFile(path)
	if err != nil {
		m.fail(err.Error())
		return m, nil
	}
	if strings.IndexByte(string(b[:min(len(b), 8000)]), 0) >= 0 {
		m.fail("binary file — not opening")
		return m, nil
	}
	m.editing, m.editPath, m.editOrig, m.discardOK = true, path, string(b), false
	m.editor.Reset()
	m.editor.SetValue(string(b))
	total := strings.Count(string(b), "\n") + 2
	for guard := 0; m.editor.Line() > 0 && guard < total; guard++ {
		m.editor.CursorUp()
	}
	for guard := 0; m.editor.Line() < line && guard < total; guard++ {
		m.editor.CursorDown()
	}
	m.editor.SetCursor(0)
	m.layout()
	focus := m.editor.Focus()
	// The textarea only scrolls to the cursor during Update.
	m.editor, _ = m.editor.Update(nil)
	return m, focus
}

func (m model) keyEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		return m.saveEditor()
	case "esc":
		if m.editor.Value() != m.editOrig && !m.discardOK {
			m.discardOK = true
			m.fail("unsaved changes — esc again to discard, ctrl+s to save")
			return m, nil
		}
		m.editing = false
		m.editor.Blur()
		m.say("closed " + m.relPath(m.editPath))
		return m, nil
	}
	m.discardOK = false
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m model) saveEditor() (tea.Model, tea.Cmd) {
	text := m.editor.Value()
	if err := os.WriteFile(m.editPath, []byte(text), 0o644); err != nil {
		m.fail("save failed: " + err.Error())
		return m, nil
	}
	m.editOrig = text
	rel := m.relPath(m.editPath)
	if strings.HasSuffix(m.editPath, ".nix") {
		if msg := ValidateNix(m.editPath); msg != "" {
			m.fail("saved " + rel + " but syntax error: " + strings.ReplaceAll(firstLines(msg, 2), "\n", " "))
		} else {
			m.say("saved " + rel + " — syntax ok")
		}
	} else {
		m.say("saved " + rel)
	}
	if m.editPath == m.cfg.PkgFile {
		m.reloadPkgFile()
	}
	return m, m.refreshChangesCmd()
}

func (m model) relPath(p string) string {
	if r, err := filepath.Rel(m.cfg.Repo, p); err == nil {
		return r
	}
	return p
}

// -- changes tab

func (m model) keyChanges(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "r":
		return m, m.refreshChangesCmd()
	case "b":
		m.busy = "dry build"
		cfg := m.cfg
		return m, func() tea.Msg {
			out, err := cfg.DryBuild()
			return cmdMsg{"dry build", out, err}
		}
	case "u":
		m.busy = "flake update"
		cfg := m.cfg
		return m, func() tea.Msg {
			out, err := run(cfg.Repo, "nix", "flake", "update")
			return cmdMsg{"flake update", out, err}
		}
	case "c":
		m.committing = true
		m.commitInput.SetValue("")
		return m, m.commitInput.Focus()
	case "R":
		cmd := exec.Command("bash", "-c", m.cfg.RollbackScript()+
			"\nstatus=$?\necho\nread -rp 'Press enter to return to nixtui… ' _\nexit $status")
		m.busy = "rollback"
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return execMsg{"rollback", err} })
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m model) keyCommit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.committing = false
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.commitInput.Value())
		m.committing = false
		if text == "" {
			m.fail("empty commit message")
			return m, nil
		}
		cfg := m.cfg
		return m, func() tea.Msg {
			if _, err := run(cfg.Repo, "git", "add", "-A"); err != nil {
				return cmdMsg{"commit", "git add failed", err}
			}
			out, err := run(cfg.Repo, "git", "commit", "-m", text)
			return cmdMsg{"commit", out, err}
		}
	}
	var cmd tea.Cmd
	m.commitInput, cmd = m.commitInput.Update(msg)
	return m, cmd
}

// ---- shared helpers ---------------------------------------------------

func (m model) moveCursor(cur, off *int, delta, n, h int) {
	if n == 0 {
		*cur, *off = 0, 0
		return
	}
	*cur = min(max(*cur+delta, 0), n-1)
	if h < 1 {
		h = 1
	}
	if *cur < *off {
		*off = *cur
	}
	if *cur >= *off+h {
		*off = *cur - h + 1
	}
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func (m model) colorDiff(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff "):
			lines[i] = sTitle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = sHunk.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = sGood.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = sBad.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// ---- view -------------------------------------------------------------

func (m model) View() string {
	if m.w == 0 {
		return "starting…"
	}
	var body string
	switch {
	case m.editing:
		body = m.viewEditor()
	case m.picking:
		body = m.viewPicker()
	default:
		switch m.tab {
		case tabSearch:
			body = m.viewSearch()
		case tabInstalled:
			body = m.viewInstalled()
		case tabFiles:
			body = m.viewFiles()
		default:
			body = m.viewChanges()
		}
	}
	body = lipgloss.NewStyle().Height(m.bodyHeight()).MaxHeight(m.bodyHeight()).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, m.viewTabs(), body, m.viewStatus(), m.viewHints())
}

func (m model) viewTabs() string {
	var parts []string
	for i, n := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		if tab(i) == m.tab && !m.editing {
			parts = append(parts, sTabOn.Render(label))
		} else {
			parts = append(parts, sTabOff.Render(label))
		}
	}
	right := ""
	if m.loading {
		right = m.spin.View() + " loading index…"
	}
	left := strings.Join(parts, "")
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + sDim.Render(right)
}

func (m model) viewStatus() string {
	s := m.flash
	style := sGood
	if m.bad {
		style = sBad
	}
	if m.busy != "" {
		s, style = m.spin.View()+" "+m.busy+"…", sWarn
	}
	if m.committing {
		return m.commitInput.View()
	}
	return style.Render(truncate(s, m.w))
}

func (m model) viewHints() string {
	k := func(key, desc string) string { return sKey.Render(key) + sDim.Render(" "+desc) }
	var hs []string
	switch {
	case m.editing:
		hs = []string{k("ctrl+s", "save"), k("esc", "close")}
	case m.picking:
		hs = []string{k("↑↓", "choose list"), k("enter", "add"), k("esc", "cancel")}
	case m.committing:
		hs = []string{k("enter", "commit"), k("esc", "cancel")}
	default:
		switch m.tab {
		case tabSearch:
			hs = []string{k("↑↓", "select"), k("enter", "add to config"), k("esc", "clear")}
		case tabInstalled:
			hs = []string{k("↑↓", "select"), k("x", "remove"), k("e", "edit file")}
		case tabFiles:
			hs = []string{k("↑↓", "select"), k("enter", "edit")}
		default:
			hs = []string{k("b", "dry build"), k("c", "commit"), k("u", "flake update"), k("R", "rollback"), k("r", "refresh")}
		}
		hs = append(hs, k("tab", "next"), k("ctrl+a", "apply"))
		if m.tab != tabSearch {
			hs = append(hs, k("q", "quit"))
		} else {
			hs = append(hs, k("ctrl+c", "quit"))
		}
	}
	return truncate(strings.Join(hs, sDim.Render("  ·  ")), m.w)
}

func (m model) searchListHeight() int { return max(3, m.bodyHeight()-1-6) }

func (m model) viewSearch() string {
	var b strings.Builder
	b.WriteString(m.input.View() + "\n")
	lh := m.searchListHeight()
	switch {
	case m.idxErr != nil:
		b.WriteString(sBad.Render("Index unavailable: " + m.idxErr.Error()))
		return b.String()
	case m.loading:
		b.WriteString(sDim.Render("First run downloads and indexes ~144k packages (about 9 MB); later runs are instant."))
		return b.String()
	case strings.TrimSpace(m.input.Value()) == "":
		b.WriteString(sDim.Render("Type to search. Enter adds the selected package to your config."))
		return b.String()
	case len(m.results) == 0:
		b.WriteString(sDim.Render("No matches."))
		return b.String()
	}

	end := min(len(m.results), m.off+lh)
	for i := m.off; i < end; i++ {
		p := m.results[i]
		mark := "  "
		if c, _ := m.installedIn(p.Attr); c != "" {
			mark = "✓ "
		}
		plain := truncate(fmt.Sprintf("%s%-34s %-10s %s", mark, truncate(p.Attr, 34), truncate(p.Version, 10), p.Desc), m.w-2)
		if i == m.cur {
			b.WriteString(sSel.Render("▌ "+plain) + "\n")
		} else {
			b.WriteString("  " + plain + "\n")
		}
	}
	for i := end - m.off; i < lh; i++ {
		b.WriteString("\n")
	}

	p := m.results[m.cur]
	b.WriteString(sDim.Render(strings.Repeat("─", m.w)) + "\n")
	title := sTitle.Render(p.Attr) + sDim.Render("  v"+p.Version)
	if c, _ := m.installedIn(p.Attr); c != "" {
		title += sGood.Render("  ✓ in " + c)
	}
	b.WriteString(truncate(title, m.w) + "\n")
	b.WriteString(truncate(p.Desc, m.w) + "\n")
	b.WriteString(sDim.Render(truncate(p.Home, m.w)))
	return b.String()
}

func (m model) installedIn(attr string) (string, bool) {
	if m.pf == nil {
		return "", false
	}
	if c, _ := m.pf.Find(attr); c != nil {
		return c.Name, true
	}
	return "", false
}

func (m model) viewPicker() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("Add "+sSel.Render(m.pickPkg)+" to which list?") + "\n\n")
	for i, c := range m.pf.Cats {
		line := fmt.Sprintf("%-22s %s", c.Name, sDim.Render(fmt.Sprintf("%d packages", len(c.Items))))
		if i == m.pickCur {
			b.WriteString(sSel.Render("▌ ") + lipgloss.NewStyle().Bold(true).Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m model) viewInstalled() string {
	if m.pfErr != nil {
		return sBad.Render("Can't read " + m.cfg.PkgFile + ": " + m.pfErr.Error())
	}
	var b strings.Builder
	b.WriteString(sDim.Render(fmt.Sprintf("%d entries in %s", len(m.rows), m.relPath(m.cfg.PkgFile))) + "\n")
	h := m.bodyHeight() - 1
	end := min(len(m.rows), m.instOff+h)
	for i := m.instOff; i < end; i++ {
		r := m.rows[i]
		text := r.item.Text
		if r.item.Opaque {
			text = sWarn.Render("(complex) ") + truncate(text, m.w-40)
		} else {
			text = sNameCol.Render(text)
		}
		line := fmt.Sprintf("%-18s %s", sDim.Render(truncate(r.cat, 18)), text)
		if i == m.instCur {
			b.WriteString(sSel.Render("▌ ") + line + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m model) viewFiles() string {
	var b strings.Builder
	b.WriteString(sDim.Render(fmt.Sprintf("%d files in %s", len(m.files), m.cfg.Repo)) + "\n")
	h := m.bodyHeight() - 1
	end := min(len(m.files), m.fileOff+h)
	for i := m.fileOff; i < end; i++ {
		line := truncate(m.files[i], m.w-2)
		if i == m.fileCur {
			b.WriteString(sSel.Render("▌ ") + lipgloss.NewStyle().Bold(true).Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m model) viewChanges() string {
	head := sDim.Render("git changes in " + m.cfg.Repo + "  (scroll with ↑↓ / pgup / pgdn)")
	return head + "\n" + m.vp.View()
}

func (m model) viewEditor() string {
	dirty := ""
	if m.editor.Value() != m.editOrig {
		dirty = sWarn.Render(" ● modified")
	}
	return sTitle.Render(m.relPath(m.editPath)) + dirty + "\n" + m.editor.View()
}
