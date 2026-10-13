package ui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/usrbinbryce/reap/internal/config"
	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
)

type App struct {
	cfg *config.Config
	env *core.Env

	setup *setupModel
	run   *runModel
	w, h  int
	armed bool // a second ctrl+c quits immediately
}

// maxW keeps lines readable on very wide terminals; the screen is centred.
const maxW = 120

// cw is the width the screens lay out in.
func (a *App) cw() int {
	if a.w > maxW {
		return maxW
	}
	return a.w
}

// New opens on the setup screen; rescan preselects checking every post again.
func New(cfg *config.Config, env *core.Env, rescan bool) *App {
	a := &App{cfg: cfg, env: env, setup: newSetup(cfg, env)}
	if rescan {
		a.setup.rescan.idx = 1
	}
	return a
}

type eventsMsg []engine.Event
type tickMsg time.Time

func waitEvents(ch <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsMsg(nil)
		}
		batch := []engine.Event{ev}
		for len(batch) < 256 {
			select {
			case ev, ok := <-ch:
				if !ok {
					return eventsMsg(batch)
				}
				batch = append(batch, ev)
			default:
				return eventsMsg(batch)
			}
		}
		return eventsMsg(batch)
	}
}

func tick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *App) Init() tea.Cmd { return tea.Batch(textinput.Blink, a.setup.focusCmd()) }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		a.setup.resize(a.cw(), a.h)
		if a.run != nil {
			a.run.width, a.run.height = a.cw(), a.h
		}
		return a, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if a.run != nil && !a.run.done && !a.armed {
				a.armed = true
				a.run.stopping = true
				a.run.run.Stop()
				return a, nil
			}
			return a, tea.Quit
		}
	}

	if a.run == nil {
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
			return a, tea.Quit
		}
		cmd, jobs := a.setup.Update(msg)
		if len(jobs) > 0 {
			return a, a.start(jobs)
		}
		return a, cmd
	}
	return a, a.updateRun(msg)
}

// start runs the first job; the rest, from a list file, follow it in turn.
func (a *App) start(jobs []engine.Job) tea.Cmd {
	r, err := engine.Start(context.Background(), jobs[0], a.env)
	if err != nil {
		a.setup.err = err.Error()
		return nil
	}
	if err := a.setup.remember(jobs[0]); err != nil {
		a.setup.err = "Couldn't save your settings: " + err.Error()
	}
	a.run = newRunModel(r, a.cw(), a.h)
	if len(jobs) > 1 {
		a.run.batch = &batch{jobs: jobs}
	}
	a.armed = false
	return tea.Batch(waitEvents(r.Events), tick())
}

// finish records a finished target and, in a batch, starts the next one.
// The tick loop carries over to the next run, so only events restart.
func (a *App) finish() tea.Cmd {
	m := a.run
	b := m.batch
	if b == nil || m.recorded {
		return nil
	}
	m.recorded = true
	b.results = append(b.results, result{target: m.job.Target, stats: m.stats, skip: m.skipping})
	if m.stats.Stopped && !m.skipping {
		return nil // stopped by the user: the whole batch ends
	}
	for b.more() {
		b.pos++
		j := b.jobs[b.pos]
		r, err := engine.Start(context.Background(), j, a.env)
		if err != nil {
			b.results = append(b.results, result{target: j.Target, err: err})
			continue
		}
		next := newRunModel(r, a.cw(), a.h)
		next.batch = b
		next.carry(m)
		a.run = next
		a.armed = false
		return waitEvents(r.Events)
	}
	return nil
}

func (a *App) updateRun(msg tea.Msg) tea.Cmd {
	m := a.run
	switch msg := msg.(type) {
	case eventsMsg:
		if len(msg) == 0 {
			m.done = true // channel closed
			return a.finish()
		}
		for _, ev := range msg {
			m.apply(ev)
		}
		if m.done {
			return a.finish()
		}
		return waitEvents(m.run.Events)
	case tickMsg:
		m.onTick(time.Time(msg))
		if m.done {
			return nil
		}
		return tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "p", " ":
			if !m.done && !m.stopping {
				if m.run.Paused() {
					m.run.Resume()
				} else {
					m.run.Pause()
				}
			}
		case "q", "esc":
			if m.done {
				if msg.String() == "q" {
					return tea.Quit
				}
				return a.backToSetup()
			}
			m.stopping = true
			m.run.Stop()
		case "enter":
			if m.done {
				return a.backToSetup()
			}
		case "n":
			if !m.done && !m.stopping && m.batch != nil && m.batch.more() {
				m.skipping = true
				m.stopping = true
				m.run.Stop()
			}
		}
	}
	return nil
}

func (a *App) backToSetup() tea.Cmd {
	a.run = nil
	a.setup.err = ""
	return a.setup.focusCmd()
}

func (a *App) View() string {
	v := a.setup.View()
	if a.run != nil {
		v = a.run.View()
	}
	if a.w > maxW {
		v = indent(v, strings.Repeat(" ", (a.w-maxW)/2))
	}
	return v
}
