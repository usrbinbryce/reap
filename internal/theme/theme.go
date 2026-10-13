// color scheme
package theme

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	Base   = lipgloss.AdaptiveColor{Light: "#A4561A", Dark: "#D9873B"} // film base
	Edge   = lipgloss.AdaptiveColor{Light: "#86660F", Dark: "#F3C969"} // edge print
	Cyan   = lipgloss.AdaptiveColor{Light: "#2A6F80", Dark: "#73B9C9"} // the inverse
	Bone   = lipgloss.AdaptiveColor{Light: "#2A241F", Dark: "#E8E0D2"}
	Smoke  = lipgloss.AdaptiveColor{Light: "#6B6157", Dark: "#8F8478"}
	Soot   = lipgloss.AdaptiveColor{Light: "#C6BBAE", Dark: "#463F38"}
	Grease = lipgloss.AdaptiveColor{Light: "#B3362C", Dark: "#E5584D"} // china marker
)

func fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

var (
	Text   = fg(Bone)
	Strong = fg(Bone).Bold(true)
	Muted  = fg(Smoke)
	Faint  = fg(Soot)
	Accent = fg(Base)
	Bold   = fg(Base).Bold(true)
	Print  = fg(Edge)
	Link   = fg(Cyan)
	Error  = fg(Grease)
	Help   = fg(Smoke).Italic(true)

	// Key is a keycap in the footer
	Key = lipgloss.NewStyle().Foreground(Bone).Background(Soot).Padding(0, 1)
	// Note is a block of help with a bar down its left edge, like a margin note
	Note = lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(Soot).PaddingLeft(1)
)

// Pill renders a short label in reverse, for status badges
func Pill(s string, c lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(lipgloss.AdaptiveColor{Light: "#FFF8EE", Dark: "#1C1713"}).Background(c).Render(s)
}

// The wordmark brightens left to right, like a strip toward the light
var ramp = []lipgloss.AdaptiveColor{
	{Light: "#7E3F12", Dark: "#B9632A"},
	{Light: "#A4561A", Dark: "#D9873B"},
	{Light: "#966413", Dark: "#E8A84F"},
	{Light: "#86660F", Dark: "#F3C969"},
}

var letters = [][3]string{
	{"█▀█", "█▀▄", "▀ ▀"},
	{"█▀▀", "█▀▀", "▀▀▀"},
	{"▄▀█", "█▀█", "▀ ▀"},
	{"█▀█", "█▀▀", "▀  "},
}

// Wordmark renders "reap" in three rows of block letters
func Wordmark() string {
	var rows [3]string
	for i, l := range letters {
		st := fg(ramp[i%len(ramp)])
		for r := 0; r < 3; r++ {
			if i > 0 {
				rows[r] += " "
			}
			rows[r] += st.Render(l[r])
		}
	}
	return strings.Join(rows[:], "\n")
}

// Count formats n with thousands separators
func Count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Bytes formats a size like 1.2 MB
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// Dur formats a duration like 3m 04s
func Dur(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	s := int(d % time.Minute / time.Second)
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
