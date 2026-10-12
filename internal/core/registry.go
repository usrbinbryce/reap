package core

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OptionKind is for the UI
type OptionKind int

const (
	OptText OptionKind = iota
	OptInt
	OptBool
	OptChoice
)

// Choice is one choice of an OptChoice option
type Choice struct {
	Value string
	Label string
}

func C(value, label string) Choice { return Choice{Value: value, Label: label} }

// One single setting a plugin accepts. The TUI and the -opt flag
// are both generated from these
type Option struct {
	Key         string
	Label       string // short, sentence case
	Help        string // will be shown under the form when option is focused
	Kind        OptionKind
	Default     string // "true" or "false" for optbool
	Choices     []Choice
	Placeholder string // for OptInt or OptText only
}

// Info describes a source to the rest of the program
type Info struct {
	Name        string // registry, e.g. "reddit"
	Title       string // display name, e.g. "Reddit"
	TargetLabel string // what the user types, e.g. "Subreddit"
	TargetHint  string // placeholder
	TargetHelp  string
	Options     []Option
}

// Everything a Source gets for one run
type Request struct {
	Target  string
	Options map[string]string
	Env     *Env
	Status  func(format string, args ...any)
}

func (r Request) Opt(key string) string { return r.Options[key] }

func (r Request) OptBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.Options[key])) {
	case "true", "1", "yes", "on":
		return true
	}

	return false
}

func (r Request) OptInt(key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.Options[key]))
	if err != nil {
		return def
	}
	return n
}

// StatusF reports what a source is doing e.g. "Reading new, page 3"
func (r Request) Statusf(format string, args ...any) {
	if r.Status != nil {
		r.Status(format, args...)
	}
}

// Source lists the items in a target
type Source interface {
	Info() Info
	Fetch(ctx context.Context, req Request, emit func(Item) error) error
}

// Resolver turns a link on one host into downloadable files
type Resolver interface {
	Name() string
	Match(u *url.URL) bool
	Resolve(ctx context.Context, u *url.URL, env *Env) ([]Media, error)
}

// Lets a source claim a pasted link so the user needn't pick it
type URLMatcher interface{ MatchURL(u *url.URL) bool }

// Returns a line for the setup screen
type SetupNoter interface{ SetupNote(env *Env) string }

// RateLimiter declares the minimum gap between requests per host
type RateLimiter interface {
	Rates() map[string]time.Duration
}

var (
	sources     = map[string]Source{}
	sourceOrder []string
	resolvers   []Resolver
)

// Adds a source. Called from plugin init
func RegisterSource(s Source) {
	name := s.Info().Name
	if name == "" || name != strings.ToLower(name) {
		panic("core: source names must be non-empty and lowercase: " + name)
	}
	if _, dup := sources[name]; dup {
		panic("core: source registered twice: " + name)
	}

	sources[name] = s
	sourceOrder = append(sourceOrder, name)
	sort.Strings(sourceOrder)
}

// Add a resolver. Called from plugin init
func RegisterResolver(r Resolver) { resolvers = append(resolvers, r) }

// Sources returns every regsistered source sorted by name
func Sources() []Source {
	out := make([]Source, 0, len(sourceOrder))
	for _, n := range sourceOrder {
		out = append(out, sources[n])
	}
	return out
}

// Resolvers returns every registered resolver
func Resolvers() []Resolver { return append([]Resolver(nil), resolvers...) }

func GetSource(name string) (Source, bool) {
	s, ok := sources[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

// SourceForURL finds a source that claims a pasted link
func SourceForURL(raw string) (Source, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, false
	}
	for _, n := range sourceOrder {
		if m, ok := sources[n].(URLMatcher); ok && m.MatchURL(u) {
			return sources[n], true
		}
	}

	return nil, false
}

// ResolverFor returns the first resolver that matches u, or nil
func ResolverFor(u *url.URL) Resolver {
	for _, r := range resolvers {
		if r.Match(u) {
			return r
		}
	}

	return nil
}

// WithDefaults merges given over the option defaults in info
func WithDefaults(info Info, given map[string]string) map[string]string {
	out := make(map[string]string, len(info.Options))
	for _, o := range info.Options {
		out[o.Key] = o.Default
	}
	for k, v := range given {
		out[k] = v
	}

	return out
}
