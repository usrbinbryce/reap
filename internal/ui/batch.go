package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/usrbinbryce/reap/internal/engine"
	"github.com/usrbinbryce/reap/internal/theme"
)

// batch is a list file's targets, downloaded one after another.
type batch struct {
	jobs    []engine.Job
	pos     int // the job running now
	results []result
}

// result is how one target went.
type result struct {
	target string
	stats  engine.Stats
	err    error // the run couldn't start
	skip   bool  // the user moved on to the next target
}

func (b *batch) more() bool { return b.pos+1 < len(b.jobs) }

// total adds up the finished targets and, while it runs, the current one.
func (b *batch) total(cur engine.Stats, running bool) engine.Stats {
	var t engine.Stats
	add := func(s engine.Stats) {
		t.Found += s.Found
		t.Known += s.Known
		t.Saved += s.Saved
		t.Dupes += s.Dupes
		t.Near += s.Near
		t.Skipped += s.Skipped
		t.Failed += s.Failed
		t.Bytes += s.Bytes
	}
	for _, r := range b.results {
		add(r.stats)
	}
	if running {
		add(cur)
	}
	return t
}

// queue renders the targets as one line: done ones ticked, the current one
// lit, the rest waiting. It scrolls so the current target stays in view.
func (b *batch) queue(w int, done bool) string {
	parts := make([]string, len(b.jobs))
	for i, j := range b.jobs {
		name := clip(j.Target, 24)
		switch {
		case i < len(b.results) && b.results[i].err != nil:
			parts[i] = theme.Error.Render("✗ " + name)
		case i < b.pos || (done && i == b.pos):
			parts[i] = theme.Muted.Render("✓ " + name)
		case i == b.pos:
			parts[i] = theme.Bold.Render("● " + name)
		default:
			parts[i] = theme.Faint.Render("· " + name)
		}
	}
	sep := theme.Faint.Render("  ")
	start := 0
	for start < b.pos && lipgloss.Width(strings.Join(parts[start:b.pos+1], sep)) > w-8 {
		start++
	}
	line := strings.Join(parts[start:], sep)
	if start > 0 {
		line = theme.Faint.Render("… ") + line
	}
	if lipgloss.Width(line) > w {
		line = clipStyled(line, w)
	}
	return line
}

// lines lists each finished target for the summary, newest last.
func (b *batch) lines(w, room int) []string {
	var out []string
	for _, r := range b.results {
		name := pad(clip(r.target, 26), 26)
		var line string
		switch {
		case r.err != nil:
			line = theme.Error.Render("✗ ") + theme.Text.Render(name) + "  " + theme.Error.Render(clip(r.err.Error(), w-36))
		case r.stats.Err != nil:
			line = theme.Error.Render("✗ ") + theme.Text.Render(name) + "  " + theme.Error.Render(clip(r.stats.Err.Error(), w-36))
		default:
			mark := theme.Accent.Render("✓ ")
			note := ""
			switch {
			case r.skip:
				mark, note = theme.Print.Render("» "), theme.Print.Render("  skipped")
			case r.stats.Stopped:
				mark, note = theme.Print.Render("■ "), theme.Print.Render("  stopped")
			}
			line = mark + theme.Text.Render(name) + "  " + theme.Strong.Render(fmt.Sprintf("%7s", theme.Count(r.stats.Saved))) +
				theme.Muted.Render(" saved   "+fmt.Sprintf("%6s", theme.Count(r.stats.Dupes))+" duplicates") + note
		}
		out = append(out, "  "+line)
	}
	if room > 0 && len(out) > room {
		hidden := len(out) - room + 1
		out = append([]string{"  " + theme.Muted.Render("… "+theme.Count(int64(hidden))+" more above")}, out[hidden:]...)
	}
	return out
}

// clipStyled cuts a styled line to w cells, keeping its colours.
func clipStyled(s string, w int) string {
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}
