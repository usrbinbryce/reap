package core

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestKindString(t *testing.T) {
	cases := map[Kind]string{KindImage: "image", KindAnimated: "animated", KindVideo: "video", KindUnknown: "unknown", Kind(99): "unknown"}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestNormalizeExt(t *testing.T) {
	cases := map[string]string{
		"https://i.redd.it/abc.JPEG":            "jpg",
		"https://i.redd.it/abc.png?width=640":   "png",
		"https://example.com/a/b.gifv":          "",
		"https://example.com/page":              "",
		"https://example.com/x.mp4#t=1":         "mp4",
		"://bad url":                            "",
		"https://example.com/archive.tar.gz":    "",
		"https://preview.redd.it/x.webp?s=sig":  "webp",
		"https://example.com/dir.png/file.html": "",
	}
	for in, want := range cases {
		if got := ExtFromURL(in); got != want {
			t.Errorf("ExtFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtFromURL(t *testing.T) {
	cases := map[string]string{
		"https://i.redd.it/abc.JPEG":            "jpg",
		"https://i.redd.it/abc.png?width=640":   "png",
		"https://example.com/a/b.gifv":          "",
		"https://example.com/page":              "",
		"https://example.com/x.mp4#t=1":         "mp4",
		"://bad url":                            "",
		"https://example.com/archive.tar.gz":    "",
		"https://preview.redd.it/x.webp?s=sig":  "webp",
		"https://example.com/dir.png/file.html": "",
	}

	for in, want := range cases {
		if got := ExtFromURL(in); got != want {
			t.Errorf("ExtFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtFromMIME(t *testing.T) {
	cases := map[string]string{
		"image/jpeg": "jpg", "IMAGE/PNG": "png", "image/png; charset=binary": "png",
		" video/mp4 ": "mp4", "text/html": "", "": "", "image/pjpeg": "jpg",
	}
	for in, want := range cases {
		if got := ExtFromMIME(in); got != want {
			t.Errorf("ExtFromMIME(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostIs(t *testing.T) {
	u, _ := url.Parse("https://I.Imgur.Com/x.jpg")
	if !HostIs(u, "imgur.com") {
		t.Error("subdomain should match")
	}
	if !HostIs(u, "other.com", "i.imgur.com") {
		t.Error("exact host should match")
	}
	evil, _ := url.Parse("https://notimgur.com/x.jpg")
	if HostIs(evil, "imgur.com") {
		t.Error("a different domain ending in the same letters must not match")
	}
}

func TestTaggedErrors(t *testing.T) {
	cases := []struct {
		err  error
		kind error
		msg  string
	}{
		{Unsupported("no %s", "host"), ErrUnsupported, "no host"},
		{Gone("bye %d", 1), ErrGone, "bye 1"},
		{NeedsSetup("key"), ErrNeedsSetup, "key"},
	}

	for _, c := range cases {
		if !errors.Is(c.err, c.kind) {
			t.Errorf("%v should match %v", c.err, c.kind)
		}
		if c.err.Error() != c.msg {
			t.Errorf("message = %q, want %q", c.err.Error(), c.msg)
		}
	}
	if errors.Is(Gone("x"), ErrUnsupported) {
		t.Error("kind must not cross-match")
	}
}

type testSource struct {
	name  string
	match string
}

func (s *testSource) Info() Info {
	return Info{Name: s.name, Title: "T", Options: []Option{
		{Key: "a", Default: "1"}, {Key: "b", Default: "x"},
	}}
}
func (s *testSource) Fetch(context.Context, Request, func(Item) error) error { return nil }
func (s *testSource) MatchURL(u *url.URL) bool                               { return s.match != "" && HostIs(u, s.match) }
func (s *testSource) Rates() map[string]time.Duration {
	return map[string]time.Duration{"src.example": 5 * time.Second}
}

type testResolver struct{ host string }

func (r *testResolver) Name() string          { return r.host }
func (r *testResolver) Match(u *url.URL) bool { return HostIs(u, r.host) }
func (r *testResolver) Resolve(context.Context, *url.URL, *Env) ([]Media, error) {
	return nil, nil
}
func (r *testResolver) Rates() map[string]time.Duration {
	return map[string]time.Duration{r.host: time.Second}
}

func register(s *testSource) {
	if _, ok := GetSource(s.name); !ok {
		RegisterSource(s)
	}
}

func TestRegistry(t *testing.T) {
	register(&testSource{name: "zeta", match: "zeta.example"})
	register(&testSource{name: "alpha"})
	RegisterResolver(&testResolver{host: "res.example"})

	names := []string{}
	for _, s := range Sources() {
		names = append(names, s.Info().Name)
	}
	if len(names) < 2 || names[0] > names[len(names)-1] {
		t.Errorf("Sources not sorted: %v", names)
	}

	if s, ok := GetSource("   ZETA  "); !ok || s.Info().Name != "zeta" {
		t.Errorf("GetSource should trim and lowercase")
	}
	if _, ok := GetSource("missing"); ok {
		t.Error("unknown source found")
	}

	if s, ok := SourceForURL("https://www.zeta.example/r/x"); !ok || s.Info().Name != "zeta" {
		t.Error("SourceForURL should not find the claiming source")
	}
	for _, raw := range []string{"https://nobody.example/", "not a url", "%zz"} {
		if _, ok := SourceForURL(raw); ok {
			t.Errorf("SourceForURL(%q) should not match", raw)
		}
	}

	u, _ := url.Parse("https://cdn.res.example/x")
	if r := ResolverFor(u); r == nil || r.Name() != "res.example" {
		t.Error("ResolverFor should find the resolver")
	}
	u2, _ := url.Parse("https://elsewhere.example/x")
	if ResolverFor(u2) != nil {
		t.Error("ResolverFor should return nil when nothing matches")
	}
	if len(Resolvers()) == 0 {
		t.Error("Resolvers is empty")
	}
}

func TestRegisterSourcePanics(t *testing.T) {
	for _, name := range []string{"", "Upper", "alpha-dup"} {
		if name == "alpha-dup" {
			register(&testSource{name: "dup"})
			name = "dup"
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterSource(%q) should panic", name)
				}
			}()
			RegisterSource(&testSource{name: name})
		}()
	}
}

func TestWithDefaults(t *testing.T) {
	info := (&testSource{name: "x"}).Info()
	got := WithDefaults(info, map[string]string{"b": "y", "extra": "z"})
	if got["a"] != "1" || got["b"] != "y" || got["extra"] != "z" {
		t.Errorf("WithDefaults = %v", got)
	}
	if C("v", "L") != (Choice{Value: "v", Label: "L"}) {
		t.Error("C builds a choice")
	}
}

func TestRequestOptions(t *testing.T) {
	var status string
	r := Request{
		Options: map[string]string{"on": " Yes ", "off": "nope", "n": " 42 ", "bad": "x", "s": "v"},
		Status:  func(f string, a ...any) { status = f },
	}
	if !r.OptBool("on") || r.OptBool("off") || r.OptBool("missing") {
		t.Error("OptBool")
	}
	for _, v := range []string{"true", "1", "on", "YES"} {
		if !(Request{Options: map[string]string{"k": v}}).OptBool("k") {
			t.Errorf("OptBool(%q) should be true", v)
		}
	}
	if r.OptInt("n", 0) != 42 || r.OptInt("bad", 7) != 7 || r.OptInt("missing", 3) != 3 {
		t.Error("OptInt")
	}
	if r.Opt("s") != "v" {
		t.Error("Opt")
	}
	r.Statusf("hello %d", 1)
	if status != "hello %d" {
		t.Errorf("Statusf didn't call Status: %q", status)
	}
	Request{}.Statusf("no status func is fine")
}

func TestNewEnv(t *testing.T) {
	register(&testSource{name: "envsrc"})
	env := NewEnv("ua/1", func(k string) string {
		if k == "a.b" {
			return "  secret \n"
		}
		return ""
	})
	if env.UserAgent != "ua/1" || env.HTTP == nil || env.HTTP.UserAgent != "ua/1" {
		t.Error("NewEnv should build a client with the user agent")
	}
	if env.Secret("a.b") != "secret" {
		t.Errorf("Secret should trim, got %q", env.Secret("a.b"))
	}
	if NewEnv("ua", nil).Secret("anything") != "" {
		t.Error("nil secret func should return empty secrets")
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cases := map[string]string{
		"~": home, "~/x/y": filepath.Join(home, "x", "y"), "~" + string(rune(92)) + "x": filepath.Join(home, "x"),
		" rel/a ": "rel/a", "rel/~": "rel/~", "~user": "~user",
	}
	for in, want := range cases {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
