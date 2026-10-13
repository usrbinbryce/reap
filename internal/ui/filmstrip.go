package ui

import (
	"fmt"
	"strings"

	"github.com/usrbinbryce/reap/internal/theme"
)

const (
	frameW  = 5 // cells per frame
	gapW    = 1
	rollLen = 36 // exposures on a roll, for runs without a limit
)

// filmstrip draws progress as a strip of negative: sprocket holes along both
// edges, exposed frames in film-base orange, unexposed ones dark, and edge
// print underneath. Without a limit it counts rolls of 36 instead.
//
// The frame being exposed shimmers while the run is live; that's the screen's
// one bit of ambient motion.
func filmstrip(width int, done, limit int64, live bool, tick int) string {
	if width < 24 {
		width = 24
	}
	n := (width + gapW) / (frameW + gapW)
	inner := n*(frameW+gapW) - gapW

	var frac float64
	right := ""
	if limit > 0 {
		frac = float64(done) / float64(limit)
		right = "of " + theme.Count(limit)
	} else {
		frac = float64(done%rollLen) / rollLen
		right = fmt.Sprintf("roll %d", done/rollLen+1)
	}
	if frac > 1 {
		frac = 1
	}
	exposed := frac * float64(n)
	full := int(exposed)
	part := int((exposed - float64(full)) * frameW)

	holes := []rune(strings.Repeat("▪ ", (inner+1)/2))
	sprockets := theme.Faint.Render(string(holes[:inner]))

	var fr strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			fr.WriteString(" ")
		}
		switch {
		case i < full:
			fr.WriteString(theme.Accent.Render(strings.Repeat("█", frameW)))
		case i == full && full < n:
			lit := strings.Repeat("█", part)
			head := ""
			rest := frameW - part
			if live && rest > 0 {
				head = theme.Print.Render([]string{"▓", "▒"}[tick%2])
				rest--
			}
			fr.WriteString(theme.Accent.Render(lit) + head + theme.Faint.Render(strings.Repeat("░", rest)))
		default:
			fr.WriteString(theme.Faint.Render(strings.Repeat("░", frameW)))
		}
	}
	frames := fr.String()

	// Edge print: the count sits under the frame being exposed.
	left := "▸" + theme.Count(done)
	col := full * (frameW + gapW)
	if col+len([]rune(left)) > inner-len(right)-2 {
		col = inner - len(right) - 2 - len([]rune(left))
	}
	if col < 0 {
		col = 0
	}
	gap := inner - col - len([]rune(left)) - len(right)
	if gap < 1 {
		gap = 1
	}
	edge := strings.Repeat(" ", col) + theme.Print.Render(left) + strings.Repeat(" ", gap) + theme.Print.Render(right)

	pad := "  "
	return pad + sprockets + "\n" + pad + frames + "\n" + pad + frames + "\n" + pad + sprockets + "\n" + pad + edge
}
