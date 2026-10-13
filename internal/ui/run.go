package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/usrbinbryce/reap/internal/engine"
	"github.com/usrbinbryce/reap/internal/httpx"
	"github.com/usrbinbryce/reap/internal/theme"
)

type lane struct {
	busy bool
	host string
	id   string
}

type feedLine struct {
	kind   engine.EventKind
	text   string
	detail string
	bytes  int64
}

type sample struct {
	t time.Time
	b int64
}

type runModel struct {
	run      *engine.Run
	job      engine.Job
	title    string
	stats    engine.Stats
	lanes    []lane
	feed     []feedLine
	status   string
	done     bool
	stopping bool
	tick     int
	samples  []sample
	rate     float64
	width    int
	height   int

	batch     *batch // set when running a list file
	recorded  bool   // this target's result is in the batch
	skipping  bool   // stopped to move on to the next target
	batchFrom time.Time
}

const feedKeep = 200

// kindNote is a feed line the UI adds itself, between a list's targets.
const kindNote engine.EventKind = -1

// carry keeps the feed and the batch clock going from the previous target.
func (m *runModel) carry(prev *runModel) {
	m.feed = prev.feed
	m.batchFrom = prev.batchFrom
	if m.batchFrom.IsZero() {
		m.batchFrom = prev.stats.Started
	}
	word := "done"
	switch {
	case prev.skipping:
		word = "skipped"
	case prev.stats.Err != nil:
		word = "stopped early"
	}
	m.push(feedLine{kind: kindNote, text: prev.job.Target + " " + word,
		detail: theme.Count(prev.stats.Saved) + " saved, " + theme.Count(prev.stats.Dupes) + " duplicates"})
	m.push(feedLine{kind: kindNote, text: "now " + m.job.Target})
}

func newRunModel(r *engine.Run, w, h int) *runModel {
	return &runModel{
		run: r, job: r.Job, title: r.Info().Title,
		lanes: make([]lane, r.Job.Workers), width: w, height: h,
		stats: r.Stats(),
	}
}

func (m *runModel) apply(ev engine.Event) {
	switch ev.Kind {
	case engine.EvStatus:
		m.status = ev.Reason
	case engine.EvStart:
		if l := m.lane(ev.Worker); l != nil {
			*l = lane{busy: true, host: httpx.Host(ev.URL), id: ev.Item.ID}
		}
	case engine.EvIdle:
		if l := m.lane(ev.Worker); l != nil {
			*l = lane{}
		}
	case engine.EvSaved:
		d := ""
		if ev.Other != "" {
			d = "looks like " + ev.Other
		}
		m.push(feedLine{kind: ev.Kind, text: ev.Path, detail: d, bytes: ev.Bytes})
	case engine.EvDuplicate:
		m.push(feedLine{kind: ev.Kind, text: ev.Item.ID, detail: "same file as " + ev.Other})
	case engine.EvNear:
		if ev.Path == "" { // skipped look-alike; flagged ones show as saved
			m.push(feedLine{kind: ev.Kind, text: ev.Item.ID, detail: ev.Reason})
		}
	case engine.EvSkipped:
		m.push(feedLine{kind: ev.Kind, text: ev.Item.ID, detail: ev.Reason})
	case engine.EvFailed:
		m.push(feedLine{kind: ev.Kind, text: ev.Item.ID, detail: ev.Reason})
	case engine.EvDone:
		m.done = true
		m.stats = ev.Stats
		for i := range m.lanes {
			m.lanes[i] = lane{}
		}
		return
	}
	if !m.done {
		m.stats = m.run.Stats()
	}
}

func (m *runModel) lane(w int) *lane {
	if w < 1 || w > len(m.lanes) {
		return nil
	}
	return &m.lanes[w-1]
}

func (m *runModel) push(l feedLine) {
	m.feed = append(m.feed, l)
	if len(m.feed) > feedKeep {
		m.feed = m.feed[len(m.feed)-feedKeep:]
	}
}

func (m *runModel) onTick(now time.Time) {
	m.tick++
	if m.done {
		return
	}
	m.stats = m.run.Stats()
	m.samples = append(m.samples, sample{now, m.stats.Bytes})
	for len(m.samples) > 2 && now.Sub(m.samples[0].t) > 5*time.Second {
		m.samples = m.samples[1:]
	}
	if n := len(m.samples); n > 1 {
		first, last := m.samples[0], m.samples[n-1]
		if dt := last.t.Sub(first.t).Seconds(); dt > 0 {
			m.rate = float64(last.b-first.b) / dt
		}
	}
}

func (m *runModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	s := m.stats
	tot := s
	if m.batch != nil {
		tot = m.batch.total(s, !m.recorded)
	}
	var top []string

	// Header: what, where, and the clock.
	left := theme.Bold.Render("reap") + theme.Faint.Render("  ·  ") + theme.Text.Render(m.title) + "  " + theme.Strong.Render(m.job.Target)
	if b := m.batch; b != nil {
		left += "  " + theme.Muted.Render(theme.Count(int64(b.pos+1))+" of "+theme.Count(int64(len(b.jobs))))
	}
	var clock string
	switch {
	case m.done:
		from := s.Started
		if m.batch != nil && !m.batchFrom.IsZero() {
			from = m.batchFrom
		}
		clock = theme.Muted.Render("finished in " + theme.Dur(s.Ended.Sub(from)))
	case m.skipping:
		clock = theme.Pill("skipping", theme.Edge)
	case m.stopping:
		clock = theme.Pill("stopping", theme.Grease)
	case m.run.Paused():
		clock = theme.Pill("paused", theme.Edge) + theme.Muted.Render("  "+theme.Dur(time.Since(s.Started)))
	default:
		clock = theme.Accent.Render(spin[m.tick%len(spin)]) + theme.Muted.Render(" running "+theme.Dur(time.Since(s.Started)))
	}
	top = append(top, "", "  "+spread(left, clock, w-4))
	top = append(top, "  "+theme.Faint.Render(strings.Repeat("─", max(w-4, 4))))
	where := "  " + theme.Muted.Render("saving to ") + theme.Link.Render(clipLeft(home(m.job.OutDir), w-14))
	if m.job.Rescan {
		where += theme.Faint.Render("  ·  ") + theme.Print.Render("checking every post again")
	}
	top = append(top, where)
	if m.batch != nil {
		top = append(top, "  "+theme.Muted.Render("queue     ")+m.batch.queue(w-14, m.done))
	}
	top = append(top, "")

	strip := s.Saved
	limit := int64(m.job.Limit)
	if m.done && m.batch != nil {
		strip, limit = tot.Saved, 0
	}
	top = append(top, filmstrip(w-4, strip, limit, !m.done && !m.run.Paused(), m.tick))
	top = append(top, "")

	// Counters, as tiles: the number big, the word under it.
	tile := func(n int64, label string, st lipgloss.Style) string {
		if n == 0 {
			st = theme.Faint
		}
		num := theme.Count(n)
		cw := max(lipgloss.Width(num), lipgloss.Width(label)) + 5
		return lipgloss.NewStyle().Width(cw).Render(st.Bold(true).Render(num) + "\n" + theme.Muted.Render(label))
	}
	top = append(top, indent(lipgloss.JoinHorizontal(lipgloss.Top,
		tile(tot.Saved, "saved", theme.Accent),
		tile(tot.Dupes, "duplicates", theme.Text),
		tile(tot.Near, "look-alikes", theme.Link),
		tile(tot.Skipped, "skipped", theme.Text),
		tile(tot.Failed, "failed", theme.Error),
	), "  "))
	sub := theme.Text.Render(theme.Bytes(tot.Bytes))
	if !m.done && m.rate > 0 {
		sub += theme.Muted.Render(" at " + theme.Bytes(int64(m.rate)) + "/s")
	}
	if tot.Known > 0 {
		sub += theme.Faint.Render("  ·  ") + theme.Muted.Render(theme.Count(tot.Known)+" posts already handled by earlier runs")
	}
	top = append(top, "", "  "+sub, "")

	if m.done {
		top = append(top, m.summary(w)...)
	} else {
		top = append(top, rule("Downloading", w))
		top = append(top, m.laneLines(w)...)
	}
	top = append(top, "")

	// The feed takes whatever height is left.
	bottom := []string{}
	if !m.done && m.status != "" {
		bottom = append(bottom, "  "+theme.Muted.Render(clip(m.status, w-4)))
	}
	switch {
	case m.done:
		bottom = append(bottom, keys(w, "enter", "new download", "q", "quit"))
	case m.stopping:
		bottom = append(bottom, keys(w, "ctrl+c", "quit now"))
	case m.batch != nil && m.batch.more():
		bottom = append(bottom, keys(w, "p", "pause or resume", "n", "skip to next", "q", "stop and keep what's saved"))
	default:
		bottom = append(bottom, keys(w, "p", "pause or resume", "q", "stop and keep what's saved"))
	}

	head := rule("Recent", w)
	if m.done && m.batch != nil {
		head = rule("Targets", w)
	}
	room := m.height - len(strings.Split(strings.Join(top, "\n"), "\n")) - len(bottom) - 2
	if m.height <= 0 {
		room = 10
	}
	var feed []string
	if m.done && m.batch != nil {
		feed = m.batch.lines(w, room)
	} else {
		feed = m.feedLines(w, room)
	}
	return strings.Join(top, "\n") + "\n" + head + "\n" + strings.Join(feed, "\n") + "\n\n" + strings.Join(bottom, "\n")
}

var spin = []string{"◐", "◓", "◑", "◒"}

func (m *runModel) laneLines(w int) []string {
	show := len(m.lanes)
	if show > 8 {
		show = 8
	}
	var out []string
	for i := 0; i < show; i++ {
		l := m.lanes[i]
		if !l.busy {
			out = append(out, "  "+theme.Faint.Render("·  waiting"))
			continue
		}
		g := theme.Accent.Render(spin[(m.tick+i)%len(spin)])
		host := theme.Link.Render(pad(clip(l.host, 22), 22))
		out = append(out, "  "+g+"  "+host+" "+theme.Text.Render(clip(l.id, w-32)))
	}
	if len(m.lanes) > show {
		out = append(out, "     "+theme.Muted.Render(fmt.Sprintf("and %d more downloading", len(m.lanes)-show)))
	}
	return out
}

var kindWord = map[engine.EventKind]struct {
	word string
	st   lipgloss.Style
}{
	engine.EvSaved:     {"saved", theme.Accent},
	engine.EvDuplicate: {"duplicate", theme.Muted},
	engine.EvNear:      {"look-alike", theme.Link},
	engine.EvSkipped:   {"skipped", theme.Muted},
	engine.EvFailed:    {"failed", theme.Error},
}

func (m *runModel) feedLines(w, room int) []string {
	if room < 1 {
		return nil
	}
	if len(m.feed) == 0 {
		return []string{"  " + theme.Faint.Render("Files appear here as they're saved.")}
	}
	start := len(m.feed) - room
	if start < 0 {
		start = 0
	}
	var out []string
	for _, l := range m.feed[start:] {
		if l.kind == kindNote {
			line := "  " + theme.Faint.Render("── ") + theme.Print.Render(clip(l.text, w-20))
			if l.detail != "" {
				line += theme.Muted.Render("  " + l.detail)
			}
			out = append(out, clipStyled(line, w))
			continue
		}
		k := kindWord[l.kind]
		size := ""
		if l.bytes > 0 && l.kind == engine.EvSaved {
			size = theme.Bytes(l.bytes)
		}
		textW := w - 16 - len(size)
		line := "  " + k.st.Render(pad(k.word, 11)) + " "
		text := theme.Text.Render(clip(l.text, textW))
		if l.detail != "" {
			remain := textW - runewidth.StringWidth(clip(l.text, textW)) - 2
			if remain > 8 {
				text += "  " + theme.Muted.Render(clip(l.detail, remain))
			}
		}
		line += text
		if size != "" {
			line = spread(line, theme.Muted.Render(size), w-2)
		}
		out = append(out, line)
	}
	return out
}

func (m *runModel) summary(w int) []string {
	s := m.stats
	var head string
	switch b := m.batch; {
	case b != nil && s.Stopped && !m.skipping:
		head = theme.Text.Render("Stopped at " + m.job.Target + ", " + theme.Count(int64(b.pos+1)) + " of " +
			theme.Count(int64(len(b.jobs))) + ". Run the same list again to pick up where it left off.")
	case b != nil:
		failed := 0
		for _, r := range b.results {
			if r.err != nil || r.stats.Err != nil {
				failed++
			}
		}
		head = theme.Text.Render("Done. Went through all " + theme.Count(int64(len(b.jobs))) + " targets in the list.")
		if failed > 0 {
			head += theme.Error.Render(" " + theme.Count(int64(failed)) + " stopped early.")
		}
	case s.Err != nil:
		head = theme.Error.Render("Stopped early: " + s.Err.Error())
	case s.LimitHit:
		head = theme.Text.Render("Reached your limit of " + theme.Count(int64(m.job.Limit)) + " new files.")
	case s.Stopped:
		head = theme.Text.Render("Stopped. Run the same download again to pick up where it left off.")
	default:
		head = theme.Text.Render("Done. Everything the source listed has been checked.")
	}
	meta := filepath.Join(m.job.OutDir, ".reap", "manifest.jsonl")
	return []string{
		"  " + lipgloss.NewStyle().Width(w-4).Render(head),
		"  " + theme.Muted.Render("Metadata for every file is in ") + theme.Link.Render(clipLeft(home(meta), w-36)),
	}
}

// spread puts left and right on one line, padded to width.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + right
}

func clip(s string, w int) string {
	if w <= 1 {
		return ""
	}
	return runewidth.Truncate(s, w, "…")
}

// clipLeft is clip from the other end, for paths, where the end matters most.
func clipLeft(s string, w int) string {
	if runewidth.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return ""
	}
	r := []rune(s)
	for len(r) > 0 && runewidth.StringWidth(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}

func pad(s string, w int) string {
	if n := runewidth.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func home(p string) string {
	h, err := os.UserHomeDir()
	if err == nil && h != "" && (p == h || strings.HasPrefix(p, h+string(filepath.Separator))) {
		return "~" + p[len(h):]
	}
	return p
}
