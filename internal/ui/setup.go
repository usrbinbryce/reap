package ui

import (
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/usrbinbryce/reap/internal/config"
	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
	"github.com/usrbinbryce/reap/internal/theme"
)

const labelW = 12

type setupModel struct {
	cfg *config.Config
	env *core.Env

	fields []field
	common int // fields before the source's own options
	focus  int
	err    string
	width  int
	height int

	source  *choiceField
	target  *textField
	list    *textField
	rescan  *choiceField
	lists   map[string]listInfo          // list files read so far, by path
	current string                       // source whose options are showing
	srcVals map[string]map[string]string // option values per source, kept while switching
}

func newSetup(cfg *config.Config, env *core.Env) *setupModel {
	m := &setupModel{cfg: cfg, env: env, srcVals: map[string]map[string]string{}, lists: map[string]listInfo{}}
	last := cfg.Last

	var choices []core.Choice
	for _, s := range core.Sources() {
		choices = append(choices, core.C(s.Info().Name, s.Info().Title))
	}
	m.source = newChoice("source", "Source", "Where to download from. New sites plug in as sources.", choices, last.Source)
	m.source.onChange = m.switchSource
	m.target = newText("target", "Target", "", last.Target, "", false, "")
	m.list = newText("list", "Or a list", "", last.List, "optional .txt file, one per line", false, "")

	dedupe := newChoice("dedupe", "Duplicates",
		"Exact copies are always skipped, across everything in this folder. Look-alikes are resized or recompressed reposts, found by comparing what the images look like.",
		[]core.Choice{
			core.C("flag", "Skip copies, note look-alikes"),
			core.C("skip", "Skip copies and look-alikes"),
			core.C("exact", "Skip exact copies only"),
		}, last.Dedupe)

	// Not remembered: checking everything again is for one run, not a habit.
	m.rescan = newChoice("rescan", "Earlier runs",
		"Posts that an earlier run into this folder already checked are skipped, so reruns are quick. "+
			"Choose check again to look at every post anew, say after turning on more file types or another listing. "+
			"Nothing is deleted, and files you already have still count as duplicates, so nothing is saved twice. "+
			"To start over completely, point Save to at a new folder.",
		[]core.Choice{
			core.C("skip", "Skip posts they already checked"),
			core.C("again", "Check every post again"),
		}, "skip")

	m.fields = []field{
		m.source,
		m.target,
		m.list,
		newText("out", "Save to", "Every run into this folder shares one duplicate index. Point all your runs here to dedupe the whole dataset.", last.OutDir, "~/reap", false, ""),
		newText("folder", "Folders", "Folders inside Save to. Tokens: {source} {collection} {author} {date}.", last.Folder, engine.DefaultFolder, false, ""),
		newText("name", "File names", "Tokens: {prefix} {id} {title} {author} {date} {hash} {variant}. The extension is added for you.", last.Name, engine.DefaultName, false, ""),
		newText("prefix", "Prefix", "Text that {prefix} becomes in file names.", last.Prefix, "none", false, ""),
		newMulti("types", "File types", "←→ picks a type, space turns it on or off. RedGIFs and Imgur gifv posts arrive as mp4.", core.FileTypes, last.Types),
		newText("limit", "Stop after", "Counts new files only. 0 keeps going until the source runs out.", strconv.Itoa(last.Limit), "0", true, "new files"),
		newText("workers", "At once", "How many files download in parallel. Each site still has its own rate limit.", strconv.Itoa(last.Workers), "6", true, "downloads"),
		m.rescan,
		dedupe,
	}
	m.common = len(m.fields)
	m.current = m.source.Value()
	m.srcVals[m.current] = cfg.SourceOptions(m.current)
	m.rebuild()
	m.focus = 1 // the target is what changes most often
	return m
}

func (m *setupModel) info() core.Info {
	s, _ := core.GetSource(m.source.Value())
	if s == nil {
		return core.Info{}
	}
	return s.Info()
}

// switchSource swaps the source-specific rows when the source changes.
func (m *setupModel) switchSource() {
	m.srcVals[m.current] = m.sourceValues()
	m.current = m.source.Value()
	if _, ok := m.srcVals[m.current]; !ok {
		m.srcVals[m.current] = m.cfg.SourceOptions(m.current)
	}
	m.rebuild()
}

func (m *setupModel) sourceValues() map[string]string {
	out := map[string]string{}
	for _, f := range m.fields[m.common:] {
		if k, ok := strings.CutPrefix(f.Key(), "opt:"); ok {
			out[k] = f.Value()
		}
	}
	return out
}

func (m *setupModel) rebuild() {
	info := m.info()
	m.target.label = info.TargetLabel
	m.target.help = info.TargetHelp
	m.target.in.Placeholder = info.TargetHint
	what := strings.ToLower(info.TargetLabel)
	if what == "" {
		what = "target"
	}
	m.list.help = "Optional. A .txt file with one " + what + " or link per line; lines starting with # are ignored. " +
		"Each line downloads in turn, one after another, and the " + what + " above is left alone."

	vals := m.srcVals[m.current]
	fields := append([]field(nil), m.fields[:m.common]...)
	for _, o := range info.Options {
		fields = append(fields, optionField(o, vals[o.Key]))
	}
	fields = append(fields, &buttonField{label: "Start download", count: m.listCount})
	m.fields = fields
	if m.focus >= len(m.fields) {
		m.focus = len(m.fields) - 1
	}
	m.resize(m.width, m.height)
}

func optionField(o core.Option, val string) field {
	key := "opt:" + o.Key
	switch o.Kind {
	case core.OptBool:
		return newToggle(key, o.Label, o.Help, o.Placeholder, val == "true")
	case core.OptChoice:
		return newChoice(key, o.Label, o.Help, o.Choices, val)
	case core.OptInt:
		return newText(key, o.Label, o.Help, val, o.Placeholder, true, "")
	default:
		return newText(key, o.Label, o.Help, val, o.Placeholder, false, "")
	}
}

func (m *setupModel) resize(w, h int) {
	m.width, m.height = w, h
	inputW := w - labelW - 8
	if inputW > 72 {
		inputW = 72
	}
	for _, f := range m.fields {
		if t, ok := f.(*textField); ok {
			t.SetWidth(inputW)
		}
	}
}

func (m *setupModel) focusCmd() tea.Cmd { return m.fields[m.focus].Focus() }

func (m *setupModel) move(d int) tea.Cmd {
	clear(m.lists) // the list file may have changed; read it again when it shows
	m.fields[m.focus].Blur()
	m.focus = (m.focus + d + len(m.fields)) % len(m.fields)
	return m.fields[m.focus].Focus()
}

func (m *setupModel) get(key string) string {
	for _, f := range m.fields {
		if f.Key() == key {
			return f.Value()
		}
	}
	return ""
}

// Update returns the jobs to run when the user starts: one, or one per line
// of the list file.
func (m *setupModel) Update(msg tea.Msg) (tea.Cmd, []engine.Job) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if t, ok := m.fields[m.focus].(*textField); ok {
			return t.forward(msg), nil
		}
		return nil, nil
	}
	switch key.String() {
	case "up", "shift+tab":
		return m.move(-1), nil
	case "down", "tab":
		return m.move(1), nil
	case "enter":
		if m.focus == len(m.fields)-1 {
			return m.submit()
		}
		return m.move(1), nil
	case "ctrl+r":
		return m.submit()
	}
	m.err = ""
	return m.fields[m.focus].Update(key), nil
}

func (m *setupModel) submit() (tea.Cmd, []engine.Job) {
	j, err := m.job()
	if err != nil {
		m.err = err.Error()
		return nil, nil
	}
	path := m.list.Value()
	if path == "" {
		return nil, []engine.Job{j}
	}
	targets, err := engine.ReadTargets(path)
	if err != nil {
		m.err = err.Error()
		return nil, nil
	}
	jobs := make([]engine.Job, len(targets))
	for i, t := range targets {
		jobs[i] = j
		jobs[i].Target = t
		jobs[i].Options = maps.Clone(j.Options)
	}
	return nil, jobs
}

type listInfo struct {
	n   int
	err error
}

// listStatus reads the list file named in the form, remembering the answer
// per path so the preview doesn't hit the disk on every frame.
func (m *setupModel) listStatus() (listInfo, bool) {
	path := m.list.Value()
	if path == "" {
		return listInfo{}, false
	}
	if li, ok := m.lists[path]; ok {
		return li, true
	}
	targets, err := engine.ReadTargets(path)
	li := listInfo{n: len(targets), err: err}
	m.lists[path] = li
	return li, true
}

// listCount is how many targets the start button will run, 0 for no list.
func (m *setupModel) listCount() int {
	li, ok := m.listStatus()
	if !ok || li.err != nil {
		return 0
	}
	return li.n
}

func (m *setupModel) job() (engine.Job, error) {
	info := m.info()
	j := engine.Job{
		Source:  m.source.Value(),
		Target:  m.get("target"),
		OutDir:  m.get("out"),
		Folder:  m.get("folder"),
		Name:    m.get("name"),
		Prefix:  m.get("prefix"),
		Dedupe:  engine.Dedupe(m.get("dedupe")),
		Rescan:  m.rescan.Value() == "again",
		Options: m.sourceValues(),
	}
	if j.Target == "" && m.list.Value() == "" {
		return j, fmt.Errorf("Enter a %s to download from, or a list file.", strings.ToLower(info.TargetLabel))
	}
	if t := m.get("types"); t != "" {
		j.Types = strings.Split(t, ",")
	} else {
		return j, errors.New("Turn on at least one file type.")
	}
	j.Limit, _ = strconv.Atoi(m.get("limit"))
	j.Workers, _ = strconv.Atoi(m.get("workers"))
	if j.Workers < 1 || j.Workers > 32 {
		return j, errors.New("At once takes a number from 1 to 32.")
	}
	for _, t := range []string{j.Folder, j.Name} {
		if err := engine.CheckTemplate(t); err != nil {
			return j, err
		}
	}
	return j, nil
}

// remember saves the job as the defaults for next time.
func (m *setupModel) remember(j engine.Job) error {
	m.cfg.Last.List = m.list.Value()
	if m.cfg.Last.List != "" {
		j.Target = m.target.Value() // the list's first line isn't what was typed
	}
	m.cfg.Remember(j)
	return m.cfg.Save()
}

func (m *setupModel) View() string {
	v := m.render(false)
	if m.height > 0 && lipgloss.Height(v) > m.height {
		v = m.render(true)
	}
	return v
}

// groups are the headings that split the common fields, by the field they sit above.
var groups = map[string]string{"source": "What", "out": "Where", "types": "How"}

// render draws the form; compact drops the wordmark and group headings so it
// fits short terminals.
func (m *setupModel) render(compact bool) string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	info := m.info()
	var b strings.Builder

	intro := theme.Text.Render("Download the images from "+info.Title+" into a folder,") + "\n" +
		theme.Muted.Render("without keeping the same picture twice.")
	switch {
	case compact:
		b.WriteString("\n " + theme.Bold.Render("reap") + "   " + theme.Muted.Render("images from "+info.Title+", deduped") + "\n\n")
	case w >= 70:
		b.WriteString("\n" + indent(lipgloss.JoinHorizontal(lipgloss.Top, theme.Wordmark(), "     ", "\n"+intro), " ") + "\n\n")
	default:
		b.WriteString("\n" + indent(theme.Wordmark()+"\n\n"+intro, " ") + "\n\n")
	}

	listOn := m.list.Value() != ""
	for i, f := range m.fields {
		focused := i == m.focus
		if g, ok := groups[f.Key()]; ok && !compact {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(rule(g, w) + "\n")
		}
		if i == m.common && strings.HasPrefix(f.Key(), "opt:") {
			b.WriteString("\n" + rule(info.Title+" settings", w) + "\n")
		}
		if _, ok := f.(*buttonField); ok {
			b.WriteString("\n" + strings.Repeat(" ", labelW+4) + f.View(focused) + "\n")
			continue
		}
		val := f.View(focused)
		switch {
		case f == field(m.target) && listOn && !focused:
			val = theme.Faint.Render(m.target.Value()) + theme.Faint.Render("  using the list below")
			if m.target.Value() == "" {
				val = theme.Faint.Render("using the list below")
			}
		case f == field(m.list) && listOn:
			val += "  " + m.listBadge()
		}
		b.WriteString(row(f.Label(), val, focused) + "\n")
	}

	pad := strings.Repeat(" ", labelW+4)
	msgW := w - labelW - 8
	if msgW < 20 {
		msgW = 20
	}
	b.WriteString("\n")
	if m.err != "" {
		b.WriteString(indent(theme.Note.BorderForeground(theme.Grease).Width(msgW).Render(theme.Error.Render(m.err)), pad))
	} else if h := m.fields[m.focus].Help(); h != "" {
		b.WriteString(indent(theme.Note.Width(msgW).Render(theme.Help.Render(h)), pad))
	}
	b.WriteString("\n\n")

	if s, ok := core.GetSource(m.source.Value()); ok {
		if n, ok := s.(core.SetupNoter); ok {
			b.WriteString(indent(theme.Muted.Width(w-4).Render(n.SetupNote(m.env)), "  ") + "\n")
		}
	}
	b.WriteString(keys(w, "↑↓", "move", "←→", "change", "space", "toggle", "ctrl+r", "start", "esc", "quit"))
	return b.String()
}

// listBadge says what's in the list file, or why it can't be used.
func (m *setupModel) listBadge() string {
	li, _ := m.listStatus()
	switch {
	case li.err != nil:
		return theme.Error.Render("✗ not found or empty")
	case li.n == 1:
		return theme.Print.Render("✓ 1 target")
	}
	return theme.Print.Render("✓ " + theme.Count(int64(li.n)) + " targets")
}

// row renders one form row: a marker, the label column and the control.
func row(label, value string, focused bool) string {
	mark, lab := "  ", theme.Muted
	if focused {
		mark, lab = theme.Accent.Render("▌")+" ", theme.Bold
	}
	return "  " + mark + lab.Width(labelW).Render(label) + " " + value
}

func rule(title string, w int) string {
	line := w - lipgloss.Width(title) - 9
	if line < 4 {
		line = 4
	}
	return "  " + theme.Faint.Render("──") + " " + theme.Print.Render(title) + " " + theme.Faint.Render(strings.Repeat("─", line))
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// keys renders "key action" pairs for the footer, keys as keycaps.
func keys(w int, pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, theme.Key.Render(pairs[i])+" "+theme.Muted.Render(pairs[i+1]))
	}
	out := "  " + strings.Join(parts, "   ")
	if lipgloss.Width(out) > w && w > 0 {
		out = "  " + strings.Join(parts, "  ")
	}
	return out
}
