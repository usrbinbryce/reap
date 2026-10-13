// CLI version of the app
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
	"github.com/usrbinbryce/reap/internal/theme"
)

func RunAll(ctx context.Context, j engine.Job, targets []string, env *core.Env, out io.Writer) error {
	var failed []string
	for i, t := range targets {
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, theme.Print.Render(fmt.Sprintf("── %d of %d", i+1, len(targets))))
		j.Target = t
		if err := Run(ctx, j, env, out); err != nil {
			fmt.Fprintln(out, theme.Error.Render(t+": "+err.Error()))
			failed = append(failed, t)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d targets stopped early: %s", len(failed), len(targets), strings.Join(failed, ", "))
	}
	return nil
}

func Run(ctx context.Context, j engine.Job, env *core.Env, out io.Writer) error {
	r, err := engine.Start(ctx, j, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s into %s\n", theme.Bold.Render("reap"), theme.Strong.Render(r.Info().Title+" "+r.Job.Target), theme.Link.Render(r.Job.OutDir))

	line := func(word string, st lipgloss.Style, text string) {
		fmt.Fprintf(out, "%s %s\n", st.Render(fmt.Sprintf("%-10s", word)), text)
	}
	lastStatus := time.Time{}
	for ev := range r.Events {
		switch ev.Kind {
		case engine.EvStatus:
			// Page-by-page progress is chatty; show it at most every few seconds.
			if time.Since(lastStatus) > 3*time.Second {
				lastStatus = time.Now()
				line("", theme.Muted, theme.Muted.Render(ev.Reason))
			}
		case engine.EvSaved:
			text := ev.Path + theme.Muted.Render("  "+theme.Bytes(ev.Bytes))
			if ev.Other != "" {
				text += theme.Link.Render("  looks like " + ev.Other)
			}
			line("saved", theme.Accent, text)
		case engine.EvDuplicate:
			line("duplicate", theme.Muted, ev.Item.ID+theme.Muted.Render("  same file as "+ev.Other))
		case engine.EvNear:
			if ev.Path == "" {
				line("look-alike", theme.Link, ev.Item.ID+theme.Muted.Render("  "+ev.Reason))
			}
		case engine.EvSkipped:
			line("skipped", theme.Muted, ev.Item.ID+theme.Muted.Render("  "+ev.Reason))
		case engine.EvFailed:
			line("failed", theme.Error, ev.Item.ID+theme.Muted.Render("  "+ev.Reason))
		case engine.EvDone:
			s := ev.Stats
			fmt.Fprintf(out, "\n%s saved (%s), %s duplicates, %s look-alikes, %s skipped, %s failed, %s already handled. Took %s.\n",
				theme.Count(s.Saved), theme.Bytes(s.Bytes), theme.Count(s.Dupes), theme.Count(s.Near),
				theme.Count(s.Skipped), theme.Count(s.Failed), theme.Count(s.Known), theme.Dur(s.Ended.Sub(s.Started)))
			if s.LimitHit {
				fmt.Fprintf(out, "Stopped at your limit of %s new files.\n", theme.Count(int64(r.Job.Limit)))
			}
			if s.Err != nil {
				return s.Err
			}
		}
	}
	return nil
}
