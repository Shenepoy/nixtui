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
	tabStore
)

var tabNames = []string{"Search", "Installed", "Files", "Changes", "Store"}

var (
	// Brand palette: teal for structure, green for selection and success,
	// orange for keys, warnings and the cursor.
	cTeal   = lipgloss.Color("#2DD4BF")
	cGreen  = lipgloss.Color("#22C55E")
	cOrange = lipgloss.Color("#F97316")
	cDim    = lipgloss.Color("#6F8F8A")
	cBad    = lipgloss.Color("#F87171")

	sTabOn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#08171A")).Background(cTeal).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sGood    = lipgloss.NewStyle().Foreground(cGreen)
	sBad     = lipgloss.NewStyle().Foreground(cBad)
	sWarn    = lipgloss.NewStyle().Foreground(cOrange)
	sSel     = lipgloss.NewStyle().Bold(true).Foreground(cGreen)
	sTitle   = lipgloss.NewStyle().Bold(true)
	sKey     = lipgloss.NewStyle().Foreground(cOrange)
	sHunk    = lipgloss.NewStyle().Foreground(cTeal)
	sNameCol = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2F3F0"))
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

type storeMsg struct {
	rep *StoreReport
	err error
}

type rootsMsg struct {
	path  string
	roots []string
}

type infoMsg struct {
	attr string
	text string
	err  error
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

	storeRep     *StoreReport
	storeBusy    bool
	storeDirty   bool
	storeCur     int
	storeOff     int
	storeSort    int
	storeDetail  bool
	storeRoots   map[string][]string
	storeScanned []string

	info map[string]string // attr -> size line from the binary cache

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
	in.Prompt = "› "
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
		storeRoots: map[string][]string{}, info: map[string]string{}, storeDirty: true,
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
	m.storeDirty = true
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
			if msg.title == "format" {
				m.reloadPkgFile()
				m.vp.SetContent(m.colorDiff(m.changesTxt))
				return m, m.refreshChangesCmd()
			}
		}
		m.vp.SetContent(m.colorDiff(m.changesTxt))
		m.vp.GotoTop()
		return m, nil

	case storeMsg:
		m.storeBusy = false
		if msg.err != nil {
			m.fail("store scan failed: " + msg.err.Error())
			return m, nil
		}
		m.storeRep = msg.rep
		m.storeCur, m.storeOff = 0, 0
		m.say(fmt.Sprintf("store scanned: %d packages", len(msg.rep.Pkgs)))
		return m, nil

	case rootsMsg:
		m.storeRoots[msg.path] = msg.roots
		return m, nil

	case infoMsg:
		m.busy = ""
		if msg.err != nil {
			m.info[msg.attr] = "size: " + msg.err.Error()
			m.fail(msg.attr + ": " + msg.err.Error())
		} else {
			m.info[msg.attr] = msg.text
			m.say(msg.attr + ": " + msg.text)
		}
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
		cmd := m.setTab((m.tab + 1) % tab(len(tabNames)))
		return m, cmd
	case "shift+tab":
		cmd := m.setTab((m.tab + tab(len(tabNames)) - 1) % tab(len(tabNames)))
		return m, cmd
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
	case tabStore:
		return m.keyStore(msg)
	default:
		return m.keyChanges(msg)
	}
}

func (m *model) setTab(t tab) tea.Cmd {
	m.tab = t
	if t == tabSearch {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
	if t == tabFiles {
		m.reloadFiles()
	}
	if t == tabStore && (m.storeRep == nil || m.storeDirty) && !m.storeBusy {
		return m.startStoreScan()
	}
	return nil
}

func (m *model) startStoreScan() tea.Cmd {
	attrs := m.installedAttrs()
	m.storeBusy, m.storeDirty = true, false
	m.storeScanned = attrs
	cfg := m.cfg
	return func() tea.Msg {
		rep, err := ScanStore(cfg, attrs)
		return storeMsg{rep, err}
	}
}

func (m model) installedAttrs() []string {
	seen := map[string]bool{}
	var attrs []string
	for _, r := range m.rows {
		a := strings.TrimPrefix(r.item.Text, "pkgs.")
		if !r.item.Opaque && !seen[a] {
			seen[a] = true
			attrs = append(attrs, a)
		}
	}
	return attrs
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
	case "ctrl+g":
		if len(m.results) == 0 {
			return m, nil
		}
		attr := m.results[m.cur].Attr
		m.busy = "checking size of " + attr
		cfg := m.cfg
		return m, func() tea.Msg {
			info, err := QueryRemote(cfg, attr)
			if err != nil {
				return infoMsg{attr: attr, err: err}
			}
			return infoMsg{attr: attr, text: info.String()}
		}
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
	case "f":
		if len(m.files) == 0 {
			return m, nil
		}
		path := filepath.Join(m.cfg.Repo, m.files[m.fileCur])
		if !strings.HasSuffix(path, ".nix") {
			m.fail("the formatter only handles .nix files")
			return m, nil
		}
		changed, err := FormatFile(path)
		switch {
		case err != nil:
			m.fail("format failed: " + strings.ReplaceAll(firstLines(err.Error(), 2), "\n", " "))
		case changed:
			m.say("formatted " + m.files[m.fileCur])
			m.reloadPkgFile()
			return m, m.refreshChangesCmd()
		default:
			m.say(m.files[m.fileCur] + " already formatted")
		}
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
	case "ctrl+f":
		return m.formatEditor()
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

func (m model) formatEditor() (tea.Model, tea.Cmd) {
	if !strings.HasSuffix(m.editPath, ".nix") {
		m.fail("the formatter only handles .nix files")
		return m, nil
	}
	out, err := FormatNix(m.editor.Value())
	if err != nil {
		m.fail("format failed: " + strings.ReplaceAll(firstLines(err.Error(), 2), "\n", " "))
		return m, nil
	}
	if out == m.editor.Value() {
		m.say("already formatted")
		return m, nil
	}
	line := m.editor.Line()
	m.editor.SetValue(out)
	total := strings.Count(out, "\n") + 2
	for guard := 0; m.editor.Line() > 0 && guard < total; guard++ {
		m.editor.CursorUp()
	}
	for guard := 0; m.editor.Line() < line && guard < total; guard++ {
		m.editor.CursorDown()
	}
	m.editor.SetCursor(0)
	m.editor, _ = m.editor.Update(nil)
	m.say("formatted with nixfmt — ctrl+s to save")
	return m, nil
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
	case "F":
		cfg := m.cfg
		m.busy = "formatting"
		return m, func() tea.Msg {
			changed, err := FormatRepo(cfg.Repo, false)
			out := "formatted " + fmt.Sprint(len(changed)) + " file(s):\n" + strings.Join(changed, "\n")
			if err != nil {
				out = err.Error()
			}
			return cmdMsg{"format", out, err}
		}
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
		focus := m.commitInput.Focus()
		return m, focus
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

// tildePath shortens a path under $HOME to ~/...
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + p[len(home):]
		}
	}
	return p
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
		case tabStore:
			body = m.viewStore()
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
	} else if m.storeBusy {
		right = m.spin.View() + " scanning store…"
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
		hs = []string{k("ctrl+s", "save"), k("ctrl+f", "format"), k("esc", "close")}
	case m.picking:
		hs = []string{k("↑↓", "choose list"), k("enter", "add"), k("esc", "cancel")}
	case m.committing:
		hs = []string{k("enter", "commit"), k("esc", "cancel")}
	default:
		switch m.tab {
		case tabSearch:
			hs = []string{k("↑↓", "select"), k("enter", "add to config"), k("ctrl+g", "size"), k("esc", "clear")}
		case tabInstalled:
			hs = []string{k("↑↓", "select"), k("x", "remove"), k("e", "edit file")}
		case tabFiles:
			hs = []string{k("↑↓", "select"), k("enter", "edit"), k("f", "format")}
		case tabStore:
			if m.storeDetail {
				hs = []string{k("esc", "back")}
			} else {
				hs = []string{k("↑↓", "select"), k("s", "sort: "+sortLabels[m.storeSort]), k("enter", "details"), k("r", "rescan")}
			}
		default:
			hs = []string{k("b", "dry build"), k("c", "commit"), k("F", "format"), k("u", "update"), k("R", "rollback"), k("r", "refresh")}
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
	b.WriteString(sDim.Render(truncate(p.Home, m.w)) + "\n")
	if t, ok := m.info[p.Attr]; ok {
		b.WriteString(sWarn.Render(truncate(t, m.w)))
	} else {
		b.WriteString(sDim.Render("ctrl+g: look up size (binary cache)"))
	}
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
	b.WriteString(sDim.Render(fmt.Sprintf("%d files in %s", len(m.files), tildePath(m.cfg.Repo))) + "\n")
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
	head := sDim.Render(truncate("changes in "+tildePath(m.cfg.Repo)+" · ↑↓ pgup pgdn scroll", m.w))
	return head + "\n" + m.vp.View()
}

func (m model) viewEditor() string {
	dirty := ""
	if m.editor.Value() != m.editOrig {
		dirty = sWarn.Render(" ● modified")
	}
	return sTitle.Render(m.relPath(m.editPath)) + dirty + "\n" + m.editor.View()
}

// ---- store tab ---------------------------------------------------------

func (m model) storeListHeight() int { return max(3, m.bodyHeight()-7) }

func (m model) sortedStore() []PkgStore {
	if m.storeRep == nil {
		return nil
	}
	p := append([]PkgStore(nil), m.storeRep.Pkgs...)
	sortStore(p, m.storeSort)
	return p
}

func (m model) keyStore(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.storeDetail {
		switch msg.String() {
		case "esc", "enter", "q", "backspace":
			m.storeDetail = false
		}
		return m, nil
	}
	pkgs := m.sortedStore()
	n, lh := len(pkgs), m.storeListHeight()
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(&m.storeCur, &m.storeOff, -1, n, lh)
	case "down", "j":
		m.moveCursor(&m.storeCur, &m.storeOff, 1, n, lh)
	case "pgup":
		m.moveCursor(&m.storeCur, &m.storeOff, -10, n, lh)
	case "pgdown":
		m.moveCursor(&m.storeCur, &m.storeOff, 10, n, lh)
	case "s":
		m.storeSort = (m.storeSort + 1) % sortModes
		m.storeCur, m.storeOff = 0, 0
	case "r":
		if !m.storeBusy {
			cmd := m.startStoreScan()
			return m, cmd
		}
	case "enter", "d":
		if n == 0 {
			return m, nil
		}
		m.storeDetail = true
		var cmds []tea.Cmd
		for _, c := range pkgs[m.storeCur].Copies {
			if _, done := m.storeRoots[c.Path]; !c.InSystem && !done {
				path := c.Path
				cmds = append(cmds, func() tea.Msg { return rootsMsg{path, Roots(path)} })
			}
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m model) viewStore() string {
	if m.storeDetail {
		return m.viewStoreDetail()
	}
	if m.storeRep == nil {
		if m.storeBusy {
			return sDim.Render("Scanning the Nix store: resolving each package and measuring closures (about 10 s)…")
		}
		return sDim.Render("Press r to scan the store.")
	}
	rep := m.storeRep
	pkgs := m.sortedStore()
	var b strings.Builder
	b.WriteString(truncate(fmt.Sprintf("%s  %s   %s  %s   %s",
		sTitle.Render("store"), fmt.Sprintf("%d paths · %s", rep.StorePaths, humanBytes(rep.StoreBytes)),
		sTitle.Render("this system"), fmt.Sprintf("%d paths · %s", rep.SysPaths, humanBytes(rep.SysBytes)),
		fmt.Sprintf("%d generations", rep.Generations)), m.w) + "\n")
	b.WriteString(sDim.Render(truncate(fmt.Sprintf("%d packages · by %s · SIZE alone, CLOSURE with deps, STALE = old copies the system no longer uses",
		len(pkgs), sortLabels[m.storeSort]), m.w)) + "\n")
	namew := min(36, max(12, m.w-2-10-10-6-5-10-5))
	b.WriteString(sDim.Render(fmt.Sprintf("  %-*s %10s %10s %6s %5s %10s", namew, "PACKAGE", "SIZE", "CLOSURE", "COPIES", "VERS", "STALE")) + "\n")

	lh := m.storeListHeight()
	end := min(len(pkgs), m.storeOff+lh)
	for i := m.storeOff; i < end; i++ {
		p := pkgs[i]
		var line string
		if p.Err != "" || !p.Present {
			note := p.Err
			if note == "" {
				note = "not in the store yet — apply to install"
			}
			line = fmt.Sprintf("  %-*s %s", namew, truncate(p.Attr, namew), sDim.Render(note))
		} else {
			stale := humanBytes(p.Stale)
			if p.Stale > 0 {
				stale = sBad.Render(fmt.Sprintf("%10s", stale))
			} else {
				stale = sDim.Render(fmt.Sprintf("%10s", stale))
			}
			line = fmt.Sprintf("  %-*s %10s %10s %6d %5d %s", namew, truncate(p.Attr, namew),
				humanBytes(p.Nar), humanBytes(p.Closure), len(p.Copies), p.Versions, stale)
		}
		if i == m.storeCur {
			line = sSel.Render("▌") + line[1:]
		}
		b.WriteString(truncate(line, m.w+40) + "\n")
	}
	for i := end - m.storeOff; i < lh; i++ {
		b.WriteString("\n")
	}

	b.WriteString(sDim.Render(strings.Repeat("─", m.w)) + "\n")
	if len(pkgs) == 0 {
		return b.String()
	}
	p := pkgs[min(m.storeCur, len(pkgs)-1)]
	b.WriteString(truncate(sTitle.Render(p.Attr)+sDim.Render("  "+p.Name+"  "+p.Path), m.w) + "\n")
	var cs []string
	for i, c := range p.Copies {
		if i == 4 {
			cs = append(cs, sDim.Render(fmt.Sprintf("+%d more", len(p.Copies)-4)))
			break
		}
		tag := sGood.Render("current")
		if !c.InSystem {
			tag = sBad.Render("old")
		}
		cs = append(cs, fmt.Sprintf("%s %s %s", c.Version, tag, humanBytes(c.Nar)))
	}
	if len(cs) == 0 {
		cs = []string{sDim.Render("no copies in the store")}
	}
	b.WriteString(truncate(strings.Join(cs, sDim.Render("  ·  ")), m.w) + "\n")
	b.WriteString(sDim.Render("enter: all copies and what keeps old ones alive"))
	return b.String()
}

func (m model) viewStoreDetail() string {
	pkgs := m.sortedStore()
	if len(pkgs) == 0 {
		return ""
	}
	p := pkgs[min(m.storeCur, len(pkgs)-1)]
	var b strings.Builder
	b.WriteString(sTitle.Render(p.Attr) + sDim.Render("  "+p.Name) + "\n")
	b.WriteString(sDim.Render(truncate(p.Path, m.w)) + "\n")
	if p.Present {
		b.WriteString(fmt.Sprintf("size %s · with dependencies %s\n", humanBytes(p.Nar), humanBytes(p.Closure)))
	}
	b.WriteString(fmt.Sprintf("%d copies across %d version(s) · %s total · %s stale\n\n",
		len(p.Copies), p.Versions, humanBytes(p.Total), humanBytes(p.Stale)))
	for _, c := range p.Copies {
		tag := sGood.Render("✓ current")
		if !c.InSystem {
			tag = sBad.Render("✗ old    ")
		}
		b.WriteString(truncate(fmt.Sprintf("%s  %-14s %10s  %s", tag, c.Version, humanBytes(c.Nar), sDim.Render(c.Path)), m.w) + "\n")
		if c.InSystem {
			continue
		}
		roots, done := m.storeRoots[c.Path]
		switch {
		case !done:
			b.WriteString(sDim.Render("      checking what keeps it…") + "\n")
		case len(roots) == 0:
			b.WriteString(sDim.Render("      no GC root — a garbage collection will free it") + "\n")
		default:
			for _, r := range roots[:min(len(roots), 4)] {
				b.WriteString(sDim.Render(truncate("      kept by "+r, m.w)) + "\n")
			}
			if len(roots) > 4 {
				b.WriteString(sDim.Render(fmt.Sprintf("      …and %d more", len(roots)-4)) + "\n")
			}
		}
	}
	return b.String()
}
