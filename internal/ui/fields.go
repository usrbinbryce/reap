package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/theme"
)

// field is one row of the setup form.
type field interface {
	Key() string
	Label() string
	Help() string
	View(focused bool) string
	Update(msg tea.KeyMsg) tea.Cmd
	Focus() tea.Cmd
	Blur()
	Value() string
}

// ---- text ----

type textField struct {
	key, label, help string
	numeric          bool
	suffix           string
	in               textinput.Model
}

func newText(key, label, help, value, placeholder string, numeric bool, suffix string) *textField {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.CharLimit = 1024
	in.Width = 48
	in.TextStyle = theme.Text
	in.PlaceholderStyle = theme.Faint
	in.Cursor.Style = theme.Accent
	in.SetValue(value)
	return &textField{key: key, label: label, help: help, numeric: numeric, suffix: suffix, in: in}
}

func (f *textField) Key() string    { return f.key }
func (f *textField) Label() string  { return f.label }
func (f *textField) Help() string   { return f.help }
func (f *textField) Value() string  { return strings.TrimSpace(f.in.Value()) }
func (f *textField) Focus() tea.Cmd { return f.in.Focus() }
func (f *textField) Blur()          { f.in.Blur() }
func (f *textField) SetWidth(w int) {
	if w < 8 {
		w = 8
	}
	f.in.Width = w
}

func (f *textField) Update(msg tea.KeyMsg) tea.Cmd {
	if f.numeric && msg.Type == tea.KeyRunes {
		for _, r := range msg.Runes {
			if r < '0' || r > '9' {
				return nil
			}
		}
	}
	var cmd tea.Cmd
	f.in, cmd = f.in.Update(msg)
	return cmd
}

func (f *textField) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.in, cmd = f.in.Update(msg)
	return cmd
}

func (f *textField) View(focused bool) string {
	v := f.in.View()
	if f.suffix != "" && f.in.Value() != "" {
		v += theme.Muted.Render(" " + f.suffix)
	}
	return v
}

// ---- choice: ←/→ cycles ----

type choiceField struct {
	key, label, help string
	choices          []core.Choice
	idx              int
	onChange         func()
}

func newChoice(key, label, help string, choices []core.Choice, value string) *choiceField {
	f := &choiceField{key: key, label: label, help: help, choices: choices}
	for i, c := range choices {
		if c.Value == value {
			f.idx = i
		}
	}
	return f
}

func (f *choiceField) Key() string    { return f.key }
func (f *choiceField) Label() string  { return f.label }
func (f *choiceField) Help() string   { return f.help }
func (f *choiceField) Focus() tea.Cmd { return nil }
func (f *choiceField) Blur()          {}

func (f *choiceField) Value() string {
	if len(f.choices) == 0 {
		return ""
	}
	return f.choices[f.idx].Value
}

func (f *choiceField) Update(msg tea.KeyMsg) tea.Cmd {
	n := len(f.choices)
	if n < 2 {
		return nil
	}
	switch msg.String() {
	case "left", "h":
		f.idx = (f.idx - 1 + n) % n
	case "right", "l", " ":
		f.idx = (f.idx + 1) % n
	default:
		return nil
	}
	if f.onChange != nil {
		f.onChange()
	}
	return nil
}

func (f *choiceField) View(focused bool) string {
	if len(f.choices) == 0 {
		return ""
	}
	label := f.choices[f.idx].Label
	if !focused {
		return theme.Text.Render(label)
	}
	if len(f.choices) == 1 {
		return theme.Strong.Render(label)
	}
	return theme.Accent.Render("‹ ") + theme.Strong.Render(label) + theme.Accent.Render(" ›")
}

// ---- toggle ----

type toggleField struct {
	key, label, help, caption string
	on                        bool
}

func newToggle(key, label, help, caption string, on bool) *toggleField {
	return &toggleField{key: key, label: label, help: help, caption: caption, on: on}
}

func (f *toggleField) Key() string    { return f.key }
func (f *toggleField) Label() string  { return f.label }
func (f *toggleField) Help() string   { return f.help }
func (f *toggleField) Focus() tea.Cmd { return nil }
func (f *toggleField) Blur()          {}

func (f *toggleField) Value() string {
	if f.on {
		return "true"
	}
	return "false"
}

func (f *toggleField) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case " ", "left", "right", "x":
		f.on = !f.on
	}
	return nil
}

func (f *toggleField) View(focused bool) string {
	box := theme.Faint.Render("□")
	if f.on {
		box = theme.Accent.Render("■")
	}
	text := theme.Muted
	if f.on || focused {
		text = theme.Text
	}
	return box + " " + text.Render(f.caption)
}

// ---- multi-select chips ----

type multiField struct {
	key, label, help string
	opts             []string
	on               map[string]bool
	cur              int
}

func newMulti(key, label, help string, opts, selected []string) *multiField {
	f := &multiField{key: key, label: label, help: help, opts: opts, on: map[string]bool{}}
	for _, s := range selected {
		f.on[core.NormalizeExt(s)] = true
	}
	return f
}

func (f *multiField) Key() string    { return f.key }
func (f *multiField) Label() string  { return f.label }
func (f *multiField) Help() string   { return f.help }
func (f *multiField) Focus() tea.Cmd { return nil }
func (f *multiField) Blur()          {}

func (f *multiField) Value() string {
	var out []string
	for _, o := range f.opts {
		if f.on[o] {
			out = append(out, o)
		}
	}
	return strings.Join(out, ",")
}

func (f *multiField) Update(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h":
		if f.cur > 0 {
			f.cur--
		}
	case "right", "l":
		if f.cur < len(f.opts)-1 {
			f.cur++
		}
	case " ", "x":
		o := f.opts[f.cur]
		f.on[o] = !f.on[o]
	}
	return nil
}

func (f *multiField) View(focused bool) string {
	parts := make([]string, len(f.opts))
	for i, o := range f.opts {
		mark, st := "□", theme.Faint
		if f.on[o] {
			mark, st = "■", theme.Text
		}
		chip := mark + " " + o
		switch {
		case focused && i == f.cur:
			chip = theme.Bold.Underline(true).Render(chip)
		case f.on[o]:
			chip = theme.Accent.Render(mark) + " " + st.Render(o)
		default:
			chip = st.Render(chip)
		}
		parts[i] = chip
	}
	return strings.Join(parts, "   ")
}

// ---- button ----

type buttonField struct {
	label string
	count func() int // targets in the list file; more than one changes the label
}

func (f *buttonField) Key() string   { return "button" }
func (f *buttonField) Label() string { return "" }
func (f *buttonField) Help() string {
	return "Press enter to start. Progress is saved as you go, so you can stop and resume."
}
func (f *buttonField) Focus() tea.Cmd                { return nil }
func (f *buttonField) Blur()                         {}
func (f *buttonField) Value() string                 { return "" }
func (f *buttonField) Update(msg tea.KeyMsg) tea.Cmd { return nil }

func (f *buttonField) View(focused bool) string {
	label := f.label
	if f.count != nil {
		if n := f.count(); n > 1 {
			label = "Start " + theme.Count(int64(n)) + " downloads"
		}
	}
	st := lipgloss.NewStyle().Padding(0, 2).Bold(true)
	if focused {
		return st.Foreground(lipgloss.AdaptiveColor{Light: "#FFF8EE", Dark: "#1C1713"}).Background(theme.Base).Render("▶ "+label) +
			theme.Muted.Render("  enter")
	}
	return st.Foreground(theme.Bone).Background(theme.Soot).Render("▶ " + label)
}
