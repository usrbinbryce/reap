package reddit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
)

func init() { core.RegisterSource(&Reddit{}) }

type Reddit struct{}

func (r *Reddit) Info() core.Info {
	return core.Info{
		Name:        "reddit",
		Title:       "Reddit",
		TargetLabel: "Subreddit",
		TargetHint:  "earthporn, pics+aww, u/someone or a link",
		TargetHelp:  "A subreddit, several joined with +, a user as u/name, or any reddit.com link",
		Options: []core.Option{
			{
				Key: "sweep", Label: "Listings", Kind: core.OptChoice, Default: "deep",
				Help: "Reddit shows about 1,000 posts per listing. Every listing walks new, hot, rising, top and controversial over each time range and merges them.",
				Choices: []core.Choice{
					core.C("deep", "Every listing"),
					core.C("new", "New only"),
					core.C("hot", "Hot only"),
					core.C("top", "Top of all time"),
				},
			},
			{
				Key: "nsfw", Label: "NSFW posts", Kind: core.OptChoice, Default: "include",
				Choices: []core.Choice{core.C("include", "Include"), core.C("only", "Only NSFW"), core.C("exclude", "Leave out")},
			},
			{
				Key: "min_score", Label: "Min score", Kind: core.OptInt, Placeholder: "any",
				Help: "Skip posts with fewer upvotes than this.",
			},
			{
				Key: "previews", Label: "Fallback", Kind: core.OptBool, Default: "true",
				Placeholder: "Save Reddit's cached copy when the original is gone",
				Help:        "Helps with deleted Imgur and RedGIFs posts. Reddit's copy is usually recompressed, so it can be lower quality.",
			},
		},
	}
}

func (*Reddit) MatchURL(u *url.URL) bool { return core.HostIs(u, "reddit.com", "redd.it") }

func (*Reddit) Rates() map[string]time.Duration {
	return map[string]time.Duration{
		"oauth.reddit.com": 650 * time.Millisecond, // 100 requests a minute
		"www.reddit.com":   2 * time.Second,        // signed out is far stricter
	}
}

func (*Reddit) SetupNote(env *core.Env) string { return newAuth(env).describe() }

type target struct {
	user bool
	name string
}

func (t target) String() string {
	if t.user {
		return "u/" + t.name
	}
	return "r/" + t.name
}

var validName = regexp.MustCompile(`^[A-Za-z0-9_+-]{2,300}$`)

func parseTarget(raw string) (target, error) {
	s := strings.TrimSpace(raw)
	if strings.Contains(s, "reddit.com") || strings.HasPrefix(s, "http") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil {
			return target{}, err
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) >= 2 {
			switch strings.ToLower(segs[0]) {
			case "r":
				return target{name: segs[1]}, nil
			case "u", "user":
				return target{user: true, name: segs[1]}, nil
			}
		}
		return target{}, fmt.Errorf("couldn't find a subreddit or user in %s", raw)
	}
	s = strings.Trim(s, "/ ")
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "r/"):
		s = s[2:]
	case strings.HasPrefix(low, "u/"):
		return target{user: true, name: s[2:]}, nil
	case strings.HasPrefix(low, "user/"):
		return target{user: true, name: s[5:]}, nil
	}
	if !validName.MatchString(s) {
		return target{}, fmt.Errorf("%q doesn't look like a subreddit name", s)
	}
	return target{name: s}, nil
}

type listing struct {
	path  string
	sort  string // query parameter, for user listings
	t     string // time range
	label string
}

var ranges = map[string]string{"all": "all time", "year": "this year", "month": "this month", "week": "this week", "day": "today"}

func plan(tg target, sweep string) []listing {
	var all []listing
	if tg.user {
		base := "/user/" + tg.name + "/submitted"
		all = []listing{{base, "new", "", "new"}, {base, "hot", "", "hot"}}
		for _, t := range []string{"all", "year", "month"} {
			all = append(all, listing{base, "top", t, "top " + ranges[t]})
		}
		all = append(all, listing{base, "controversial", "all", "controversial " + ranges["all"]})
		switch sweep {
		case "new":
			return all[:1]
		case "hot":
			return all[1:2]
		case "top":
			return all[2:3]
		}
		return all
	}
	base := "/r/" + tg.name + "/"
	all = []listing{{base + "new", "", "", "new"}, {base + "hot", "", "", "hot"}, {base + "rising", "", "", "rising"}}
	for _, t := range []string{"all", "year", "month", "week", "day"} {
		all = append(all, listing{base + "top", "", t, "top " + ranges[t]})
	}
	for _, t := range []string{"all", "year", "month"} {
		all = append(all, listing{base + "controversial", "", t, "controversial " + ranges[t]})
	}
	switch sweep {
	case "new":
		return all[:1]
	case "hot":
		return all[1:2]
	case "top":
		return all[3:4]
	}
	return all
}

func (*Reddit) Fetch(ctx context.Context, req core.Request, emit func(core.Item) error) error {
	tg, err := parseTarget(req.Target)
	if err != nil {
		return err
	}
	cl := &client{env: req.Env, auth: newAuth(req.Env)}
	if err := cl.auth.ensure(ctx); err != nil {
		return err
	}
	req.Statusf("%s", cl.auth.describe())

	f := filter{nsfw: req.Opt("nsfw"), previews: req.OptBool("previews")}
	if s := strings.TrimSpace(req.Opt("min_score")); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.minScore, f.hasMin = n, true
		}
	}

	seen := map[string]bool{}
	var lastErr, emitErr error
	ok := 0
	for _, l := range plan(tg, req.Opt("sweep")) {
		l := l
		err := cl.walk(ctx, l,
			func(page int) {
				req.Statusf("Reading %s, %s, page %d. %d posts so far.", tg, l.label, page, len(seen))
			},
			func(p *post) error {
				if seen[p.ID] {
					return nil
				}
				seen[p.ID] = true
				if !f.keep(p) {
					return nil
				}
				for _, it := range extract(p, f.previews) {
					if err := emit(it); err != nil {
						emitErr = err
						return err
					}
				}
				return nil
			})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr != nil {
				return emitErr // the caller asked us to stop
			}
			var fatal *fatalErr
			if errors.As(err, &fatal) {
				return err
			}
			lastErr = err
			req.Statusf("Couldn't finish %s (%v). Moving on.", l.label, err)
			continue
		}
		ok++
	}
	if ok == 0 && lastErr != nil {
		return lastErr
	}
	req.Statusf("Checked %d posts in %s. That's everything Reddit lists.", len(seen), tg)
	return nil
}

type filter struct {
	nsfw     string
	minScore int
	hasMin   bool
	previews bool
}

func (f filter) keep(p *post) bool {
	switch f.nsfw {
	case "only":
		if !p.Over18 {
			return false
		}
	case "exclude":
		if p.Over18 {
			return false
		}
	}
	return !f.hasMin || p.Score >= f.minScore
}

type fatalErr struct{ msg string }

func (e *fatalErr) Error() string { return e.msg }
