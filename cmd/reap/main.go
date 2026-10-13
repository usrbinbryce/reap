// Command reap downloads images from Reddit (and, through plugins, other
// sites) into a deduplicated dataset folder.
//
//	reap                                  open the TUI
//	reap earthporn                        open the TUI with a target filled in
//	reap -no-tui -t earthporn -limit 500  run without the TUI
//	reap -no-tui -list subs.txt           run each line of a file in turn
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usrbinbryce/reap/internal/cli"

	"github.com/usrbinbryce/reap/internal/config"
	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
	_ "github.com/usrbinbryce/reap/internal/plugins"
	"github.com/usrbinbryce/reap/internal/ui"
)

const version = "0.1.0"

type kv map[string]string

func (k kv) String() string {
	var parts []string
	for key, v := range k {
		parts = append(parts, key+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (k kv) Set(s string) error {
	key, val, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("want key=value, got %q", s)
	}
	k[strings.TrimSpace(key)] = strings.TrimSpace(val)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reap:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, cfgErr := config.Load()

	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	source := fs.String("s", "", "source to download from (see -sources)")
	target := fs.String("t", "", "what to download: a subreddit, u/user or a link")
	list := fs.String("list", "", "a .txt file with one target per line, run one after another")
	out := fs.String("o", "", "dataset folder")
	types := fs.String("types", "", "comma-separated file types, e.g. jpg,png,gif,mp4")
	prefix := fs.String("prefix", "", "text for the {prefix} name token")
	folder := fs.String("folder", "", "folder template, default "+engine.DefaultFolder)
	name := fs.String("name", "", "file name template, default "+engine.DefaultName)
	limit := fs.Int("limit", -1, "stop after this many new files; 0 means no limit")
	workers := fs.Int("workers", 0, "parallel downloads")
	dedupe := fs.String("dedupe", "", "look-alikes: flag (keep and note), skip, or exact (ignore)")
	rescan := fs.Bool("rescan", false, "recheck posts earlier runs already handled")
	noTUI := fs.Bool("no-tui", false, "print progress lines instead of opening the TUI")
	listSources := fs.Bool("sources", false, "list sources, their options and supported hosts")
	showVersion := fs.Bool("version", false, "print the version")
	opts := kv{}
	fs.Var(opts, "opt", "source option as key=value; repeatable")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "reap %s downloads images into a deduplicated dataset folder.\n\n", version)
		fmt.Fprintf(fs.Output(), "Usage:\n  reap [flags] [target]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nSettings and credentials live in %s.\nCredentials can also come from REAP_REDDIT_COOKIE (a reddit_session value or a\ncookies.txt path), REAP_REDDIT_CLIENT_ID, REAP_REDDIT_SECRET, REAP_REDDIT_USERNAME,\nREAP_REDDIT_PASSWORD and REAP_IMGUR_CLIENT_ID.\n", config.Path())
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("reap", version)
		return nil
	}
	if *listSources {
		printSources()
		return nil
	}
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "reap: ignoring the config file:", cfgErr)
	}

	if *target == "" && fs.NArg() > 0 {
		*target = strings.Join(fs.Args(), " ")
	}
	if *source == "" && *target != "" {
		if s, ok := core.SourceForURL(*target); ok {
			*source = s.Info().Name
		}
	}

	// Flags override the remembered settings.
	l := &cfg.Last
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&l.Source, *source)
	set(&l.Target, *target)
	if *target != "" && *list == "" {
		l.List = "" // a target on the command line means just that one
	}
	set(&l.List, *list)
	set(&l.OutDir, *out)
	set(&l.Prefix, *prefix)
	set(&l.Folder, *folder)
	set(&l.Name, *name)
	set(&l.Dedupe, *dedupe)
	if *types != "" {
		l.Types = strings.Split(*types, ",")
	}
	if *limit >= 0 {
		l.Limit = *limit
	}
	if *workers > 0 {
		l.Workers = *workers
	}
	if _, ok := core.GetSource(l.Source); !ok {
		return fmt.Errorf("there's no source called %q; try -sources", l.Source)
	}
	if len(opts) > 0 {
		m := l.Options[l.Source]
		if m == nil {
			m = map[string]string{}
		}
		for k, v := range opts {
			m[k] = v
		}
		l.Options[l.Source] = m
	}

	env := core.NewEnv(userAgent(cfg), cfg.Secret)

	if *noTUI || !isTerminal() {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		j := cfg.Job()
		j.Rescan = *rescan
		if *list == "" {
			return cli.Run(ctx, j, env, os.Stdout)
		}
		targets, err := engine.ReadTargets(*list)
		if err != nil {
			return err
		}
		return cli.RunAll(ctx, j, targets, env, os.Stdout)
	}
	_, err := tea.NewProgram(ui.New(cfg, env, *rescan), tea.WithAltScreen()).Run()
	return err
}

// Reddit asks for a descriptive user agent that names the account.
func userAgent(cfg *config.Config) string {
	if cfg.UserAgent != "" {
		return cfg.UserAgent
	}
	if u := cfg.Secret("reddit.username"); u != "" {
		return fmt.Sprintf("reap/%s (dataset downloader; by /u/%s)", version, u)
	}
	return "reap/" + version + " (dataset downloader)"
}

func isTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func printSources() {
	for _, s := range core.Sources() {
		info := s.Info()
		fmt.Printf("%s (%s): %s\n", info.Name, info.Title, info.TargetHelp)
		for _, o := range info.Options {
			line := fmt.Sprintf("  -opt %s=", o.Key)
			switch o.Kind {
			case core.OptChoice:
				var vals []string
				for _, c := range o.Choices {
					vals = append(vals, c.Value)
				}
				line += strings.Join(vals, "|")
			case core.OptBool:
				line += "true|false"
			case core.OptInt:
				line += "<number>"
			default:
				line += "<text>"
			}
			if o.Default != "" {
				line += "  (default " + o.Default + ")"
			}
			fmt.Println(line)
		}
	}
	var hosts []string
	for _, r := range core.Resolvers() {
		hosts = append(hosts, r.Name())
	}
	fmt.Printf("\nHosts with their own resolver: %s. Direct image and video links work everywhere.\n", strings.Join(hosts, ", "))
}
