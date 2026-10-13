package reddit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/httpx"
	tk "github.com/usrbinbryce/reap/internal/testkit"
)

func init() { httpx.BackoffBase = time.Millisecond }

func TestInfoAndPlumbing(t *testing.T) {
	r := &Reddit{}
	info := r.Info()
	if info.Name != "reddit" || len(info.Options) != 4 {
		t.Errorf("info = %+v", info)
	}
	for _, raw := range []string{"https://www.reddit.com/r/pics", "https://redd.it/abc", "https://old.reddit.com/u/x"} {
		u, _ := url.Parse(raw)
		if !r.MatchURL(u) {
			t.Errorf("MatchURL(%q) = false", raw)
		}
	}
	u, _ := url.Parse("https://imgur.com/x")
	if r.MatchURL(u) {
		t.Error("imgur isn't reddit")
	}
	if len(r.Rates()) != 2 {
		t.Error("Rates")
	}
	if !strings.Contains(r.SetupNote(env(nil)), "signed out") {
		t.Error("SetupNote without credentials")
	}
	if s, ok := core.GetSource("reddit"); !ok || s.Info().Title != "Reddit" {
		t.Error("reddit should register itself")
	}
}

func TestParseTarget(t *testing.T) {
	ok := map[string]string{
		"pics":                          "r/pics",
		"r/pics":                        "r/pics",
		"R/Pics":                        "r/Pics",
		"/r/pics/":                      "r/pics",
		"earthporn+cityporn":            "r/earthporn+cityporn",
		"u/someone":                     "u/someone",
		"user/someone":                  "u/someone",
		"U/someone":                     "u/someone",
		"https://www.reddit.com/r/aww/": "r/aww",
		"reddit.com/r/aww/top":          "r/aww",
		"https://old.reddit.com/user/x/submitted": "u/x",
		"https://reddit.com/u/y":                  "u/y",
	}
	for in, want := range ok {
		tg, err := parseTarget(in)
		if err != nil || tg.String() != want {
			t.Errorf("parseTarget(%q) = %v, %v; want %s", in, tg, err, want)
		}
	}
	for _, bad := range []string{"", "a", "has space", "https://www.reddit.com/", "https://reddit.com/comments/x", "http://%zz", "bad/char!"} {
		if tg, err := parseTarget(bad); err == nil {
			t.Errorf("parseTarget(%q) = %v, want an error", bad, tg)
		}
	}
}

func TestPlan(t *testing.T) {
	sub := target{name: "pics"}
	user := target{user: true, name: "bob"}
	cases := []struct {
		tg    target
		sweep string
		n     int
		first string
	}{
		{sub, "deep", 11, "new"},
		{sub, "new", 1, "new"},
		{sub, "hot", 1, "hot"},
		{sub, "top", 1, "top all time"},
		{user, "deep", 6, "new"},
		{user, "new", 1, "new"},
		{user, "hot", 1, "hot"},
		{user, "top", 1, "top all time"},
	}
	for _, c := range cases {
		ls := plan(c.tg, c.sweep)
		if len(ls) != c.n || ls[0].label != c.first {
			t.Errorf("plan(%v, %s) = %d listings starting %q", c.tg, c.sweep, len(ls), ls[0].label)
		}
	}
	for _, l := range plan(user, "deep") {
		if l.path != "/user/bob/submitted" || l.sort == "" {
			t.Errorf("user listing = %+v", l)
		}
	}
	seen := map[string]bool{}
	for _, l := range plan(sub, "deep") {
		key := l.path + "?" + l.t
		if seen[key] {
			t.Errorf("duplicate listing %s", key)
		}
		seen[key] = true
	}
}

func TestFilter(t *testing.T) {
	sfw := &post{Score: 5}
	nsfw := &post{Over18: true, Score: 50}
	cases := []struct {
		f         filter
		sfw, nsfw bool
	}{
		{filter{nsfw: "include"}, true, true},
		{filter{nsfw: "only"}, false, true},
		{filter{nsfw: "exclude"}, true, false},
		{filter{minScore: 10, hasMin: true}, false, true},
		{filter{minScore: 0, hasMin: true}, true, true},
	}
	for i, c := range cases {
		if c.f.keep(sfw) != c.sfw || c.f.keep(nsfw) != c.nsfw {
			t.Errorf("case %d: keep(sfw)=%v keep(nsfw)=%v", i, c.f.keep(sfw), c.f.keep(nsfw))
		}
	}
}

// ---- cookies ----

func TestParseCookieForms(t *testing.T) {
	cases := map[string]string{
		"abc123":                           "reddit_session=abc123; over18=1",
		"reddit_session=abc; token_v2=t":   "reddit_session=abc; token_v2=t; over18=1",
		"Cookie: reddit_session=abc":       "reddit_session=abc; over18=1",
		"  reddit_session=abc; over18=0  ": "reddit_session=abc; over18=0",
		"token_v2=only":                    "token_v2=only; over18=1",
	}
	for in, want := range cases {
		got, err := parseCookie(in)
		if err != nil || got != want {
			t.Errorf("parseCookie(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseCookie("session_id=nope"); err == nil {
		t.Error("a cookie without reddit_session should be refused")
	}
}

func TestParseCookieFromFile(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().Add(24 * time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()
	body := strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"",
		fmt.Sprintf("#HttpOnly_.reddit.com\tTRUE\t/\tTRUE\t%d\treddit_session\told", future),
		fmt.Sprintf(".reddit.com\tTRUE\t/\tTRUE\t%d\treddit_session\tnew", future),
		"www.reddit.com\tFALSE\t/\tTRUE\t0\tloid\tsession-only",
		fmt.Sprintf(".reddit.com\tTRUE\t/\tTRUE\t%d\texpired\tx", past),
		fmt.Sprintf(".example.com\tTRUE\t/\tTRUE\t%d\tother\ty", future),
		"too\tfew\tfields",
	}, "\n")
	p := filepath.Join(dir, "cookies.txt")
	os.WriteFile(p, []byte(body), 0o600)

	got, err := parseCookie(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "reddit_session=new; loid=session-only; over18=1" {
		t.Errorf("cookie = %q", got)
	}

	// ~ expands to the home folder.
	tk.SetHome(t, dir)
	if got2, err := parseCookie("~/cookies.txt"); err != nil || got2 != got {
		t.Errorf("~ path: %q, %v", got2, err)
	}

	// A bare relative name works when the file exists.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	if _, err := parseCookie("cookies.txt"); err != nil {
		t.Errorf("relative path: %v", err)
	}
}

func TestParseCookieFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseCookie(filepath.Join(dir, "missing.txt")); err == nil {
		t.Error("missing file")
	}
	empty := filepath.Join(dir, "empty.txt")
	os.WriteFile(empty, []byte(".example.com\tTRUE\t/\tTRUE\t0\ta\tb\n"), 0o600)
	if _, err := parseCookie(empty); err == nil || !strings.Contains(err.Error(), "no unexpired reddit.com cookies") {
		t.Errorf("no reddit cookies: %v", err)
	}
	long := filepath.Join(dir, "long.txt")
	os.WriteFile(long, []byte(strings.Repeat("x", 2<<20)), 0o600)
	if _, err := parseCookie(long); err == nil {
		t.Error("a line too long to scan should error")
	}
	if !looksLikePath("./x") || !looksLikePath("/abs") || looksLikePath("a=b") || looksLikePath("nosuchfile") {
		t.Error("looksLikePath")
	}
}

// ---- auth modes ----

func env(secrets map[string]string) *core.Env {
	return core.NewEnv("reap-test", func(k string) string { return secrets[k] })
}

func TestAuthModesAndMessages(t *testing.T) {
	pub := newAuth(env(nil))
	if pub.mode() != modePublic || pub.invalidate() || !strings.Contains(pub.describe(), "signed out") {
		t.Error("public mode")
	}

	ck := newAuth(env(map[string]string{"reddit.cookie": "abc"}))
	if ck.mode() != modeCookie || ck.invalidate() || ck.describe() != "Using your Reddit browser cookie." ||
		!strings.Contains(ck.rejected(), "cookie") {
		t.Errorf("cookie mode: %q", ck.describe())
	}
	ck.who = "me"
	if !strings.Contains(ck.describe(), "u/me") {
		t.Error("describe should name the account once known")
	}

	bad := newAuth(env(map[string]string{"reddit.cookie": "x=y"}))
	if bad.mode() != modeCookie || !strings.Contains(bad.describe(), "can't be used") {
		t.Error("a broken cookie should still select cookie mode and explain itself")
	}
	if err := bad.ensure(context.Background()); err == nil {
		t.Error("ensure should refuse a broken cookie")
	}

	app := newAuth(env(map[string]string{"reddit.client_id": "id", "reddit.secret": "s", "reddit.cookie": "abc"}))
	if app.mode() != modeOAuth || !strings.Contains(app.describe(), "app key") || !strings.Contains(app.rejected(), "app ID") {
		t.Error("an app key beats a cookie")
	}
	user := newAuth(env(map[string]string{"reddit.client_id": "id", "reddit.username": "bob", "reddit.password": "pw"}))
	if !strings.Contains(user.describe(), "u/bob") || !user.invalidate() {
		t.Error("oauth with password")
	}
}

// ---- a fake Reddit ----

type fakeReddit struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	requests   []string
	tokenCalls int
	oauthFails int // 401s still to send on oauth listings
	limitOnce  bool
}

func post1(id string, score int, nsfw bool) map[string]any {
	return map[string]any{"kind": "t3", "data": map[string]any{
		"id": id, "subreddit": "pics", "url": "https://i.redd.it/" + id + ".jpg",
		"domain": "i.redd.it", "score": score, "over_18": nsfw, "created_utc": 1717113600,
	}}
}

func page(after string, children ...map[string]any) map[string]any {
	return map[string]any{"data": map[string]any{"after": after, "children": children}}
}

func newFakeReddit(t *testing.T) *fakeReddit {
	f := &fakeReddit{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	oldW, oldO := wwwBase, oauthBase
	wwwBase, oauthBase = f.srv.URL, f.srv.URL+"/oauth"
	t.Cleanup(func() { wwwBase, oauthBase = oldW, oldO })
	return f
}

func (f *fakeReddit) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeReddit) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" cookie="+r.Header.Get("Cookie")+" auth="+r.Header.Get("Authorization"))
	f.mu.Unlock()
	js := func(v any) { json.NewEncoder(w).Encode(v) }

	p := r.URL.Path
	switch {
	case p == "/api/me.json":
		if strings.Contains(r.Header.Get("Cookie"), "reddit_session=good") {
			js(map[string]any{"data": map[string]any{"name": "me"}})
			return
		}
		js(map[string]any{})
	case p == "/api/v1/access_token":
		id, secret, _ := r.BasicAuth()
		r.ParseForm()
		f.mu.Lock()
		f.tokenCalls++
		f.mu.Unlock()
		if id != "id" || secret != "s" {
			w.WriteHeader(http.StatusUnauthorized)
			js(map[string]any{"error": "invalid_grant"})
			return
		}
		if r.Form.Get("grant_type") == "password" && r.Form.Get("password") != "pw" {
			w.WriteHeader(http.StatusOK)
			js(map[string]any{"error": "invalid_grant"})
			return
		}
		js(map[string]any{"access_token": "tok", "expires_in": 0})
	case strings.HasPrefix(p, "/oauth/"):
		if r.Header.Get("Authorization") != "bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		fail := f.oauthFails > 0
		if fail {
			f.oauthFails--
		}
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.listing(w, r, strings.TrimPrefix(p, "/oauth"))
	case strings.HasSuffix(p, ".json"):
		f.listing(w, r, strings.TrimSuffix(p, ".json"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeReddit) listing(w http.ResponseWriter, r *http.Request, p string) {
	js := func(v any) { json.NewEncoder(w).Encode(v) }
	f.mu.Lock()
	limit := f.limitOnce
	f.limitOnce = false
	f.mu.Unlock()
	if limit {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "0")
	}
	switch p {
	case "/r/pics/new":
		if r.URL.Query().Get("raw_json") != "1" || r.URL.Query().Get("limit") != "100" {
			f.t.Errorf("listing query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("after") == "" {
			js(page("t3_b", post1("a", 1, false), post1("b", 50, true),
				map[string]any{"kind": "t1", "data": map[string]any{"id": "comment"}}))
			return
		}
		js(page("", post1("b", 50, true), post1("c", 100, false)))
	case "/r/pics/hot", "/r/pics/rising":
		js(page("", post1("a", 1, false), post1("d", 7, false)))
	case "/r/pics/top":
		if r.URL.Query().Get("t") == "week" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		js(page("", post1("e", 3, false)))
	case "/r/pics/controversial":
		js(page("", post1("f", 4, false)))
	case "/user/bob/submitted":
		js(page("", post1("u"+r.URL.Query().Get("sort"), 1, false)))
	case "/search":
		js(page(""))
	case "/r/empty/new":
		js(page(""))
	case "/r/private/new":
		w.WriteHeader(http.StatusForbidden)
	case "/r/missing/new":
		w.WriteHeader(http.StatusNotFound)
	case "/r/search/new":
		http.Redirect(w, r, "/search.json?q=x", http.StatusFound)
	case "/r/garbled/new":
		w.Write([]byte("{"))
	case "/r/broken/new", "/r/broken/hot":
		w.WriteHeader(http.StatusBadGateway)
	case "/r/teapot/new":
		w.WriteHeader(http.StatusTeapot)
	default:
		http.NotFound(w, r)
	}
}

type result struct {
	ids    []string
	status []string
	err    error
}

func fetch(t *testing.T, secrets map[string]string, target string, opts map[string]string) result {
	t.Helper()
	var res result
	var mu sync.Mutex
	src := &Reddit{}
	req := core.Request{
		Target:  target,
		Options: core.WithDefaults(src.Info(), opts),
		Env:     env(secrets),
		Status: func(f string, a ...any) {
			mu.Lock()
			res.status = append(res.status, fmt.Sprintf(f, a...))
			mu.Unlock()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res.err = src.Fetch(ctx, req, func(it core.Item) error {
		res.ids = append(res.ids, it.ID)
		return nil
	})
	return res
}

func TestFetchSignedOutPaginatesAndDedupes(t *testing.T) {
	f := newFakeReddit(t)
	res := fetch(t, nil, "pics", map[string]string{"sweep": "new"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if strings.Join(res.ids, ",") != "a,b,c" {
		t.Errorf("ids = %v", res.ids)
	}
	for _, r := range f.log() {
		if strings.Contains(r, "/r/pics/new.json") && !strings.Contains(r, "cookie=over18=1") {
			t.Errorf("signed-out requests should skip the NSFW gate: %s", r)
		}
	}
	if len(res.status) < 3 || !strings.Contains(res.status[len(res.status)-1], "Checked 3 posts") {
		t.Errorf("status = %v", res.status)
	}
}

func TestFetchDeepSweepMergesListingsAndSurvivesOneFailure(t *testing.T) {
	newFakeReddit(t)
	res := fetch(t, nil, "r/pics", nil) // default sweep is deep
	if res.err != nil {
		t.Fatal(res.err)
	}
	if strings.Join(res.ids, ",") != "a,b,c,d,e,f" {
		t.Errorf("ids = %v", res.ids)
	}
	found := false
	for _, s := range res.status {
		found = found || strings.Contains(s, "Couldn't finish top this week")
	}
	if !found {
		t.Errorf("a failed listing should be reported: %v", res.status)
	}
}

func TestFetchFilters(t *testing.T) {
	newFakeReddit(t)
	if res := fetch(t, nil, "pics", map[string]string{"sweep": "new", "nsfw": "only"}); strings.Join(res.ids, ",") != "b" {
		t.Errorf("nsfw only = %v", res.ids)
	}
	if res := fetch(t, nil, "pics", map[string]string{"sweep": "new", "nsfw": "exclude", "min_score": " 10 "}); strings.Join(res.ids, ",") != "c" {
		t.Errorf("exclude + min score = %v", res.ids)
	}
	if res := fetch(t, nil, "pics", map[string]string{"sweep": "new", "min_score": "lots"}); len(res.ids) != 3 {
		t.Errorf("a non-number min score is ignored: %v", res.ids)
	}
}

func TestFetchUser(t *testing.T) {
	newFakeReddit(t)
	res := fetch(t, nil, "u/bob", nil)
	if res.err != nil || strings.Join(res.ids, ",") != "unew,uhot,utop,ucontroversial" {
		t.Errorf("user ids = %v, %v", res.ids, res.err)
	}
}

func TestFetchWithCookie(t *testing.T) {
	f := newFakeReddit(t)
	res := fetch(t, map[string]string{"reddit.cookie": "good"}, "pics", map[string]string{"sweep": "new"})
	if res.err != nil || len(res.ids) != 3 {
		t.Fatalf("%v %v", res.ids, res.err)
	}
	if !strings.Contains(res.status[0], "as u/me") {
		t.Errorf("first status should name the account: %v", res.status)
	}
	for _, r := range f.log() {
		if strings.Contains(r, "/r/pics/") && !strings.Contains(r, "reddit_session=good; over18=1") {
			t.Errorf("listing sent without the cookie: %s", r)
		}
	}

	res = fetch(t, map[string]string{"reddit.cookie": "expired"}, "pics", nil)
	if res.err == nil || !strings.Contains(res.err.Error(), "didn't recognise your cookie") || len(res.ids) != 0 {
		t.Errorf("expired cookie: %v", res.err)
	}
}

func TestFetchWithOAuth(t *testing.T) {
	f := newFakeReddit(t)
	f.oauthFails = 1 // the first listing call is rejected, forcing a token refresh
	secrets := map[string]string{"reddit.client_id": "id", "reddit.secret": "s"}
	res := fetch(t, secrets, "pics", map[string]string{"sweep": "new"})
	if res.err != nil || strings.Join(res.ids, ",") != "a,b,c" {
		t.Fatalf("%v %v", res.ids, res.err)
	}
	f.mu.Lock()
	calls := f.tokenCalls
	f.oauthFails = 5
	f.mu.Unlock()
	if calls != 2 {
		t.Errorf("token calls = %d, want 2 (initial + refresh after 401)", calls)
	}
	for _, r := range f.log() {
		if strings.Contains(r, "/oauth/r/pics/new") && strings.Contains(r, ".json") {
			t.Errorf("oauth listings don't use .json: %s", r)
		}
	}

	res = fetch(t, secrets, "pics", map[string]string{"sweep": "new"})
	if res.err == nil || !strings.Contains(res.err.Error(), "rejected the sign-in") {
		t.Errorf("repeated 401 = %v", res.err)
	}

	res = fetch(t, map[string]string{"reddit.client_id": "id", "reddit.secret": "wrong"}, "pics", nil)
	if res.err == nil || !strings.Contains(res.err.Error(), "invalid_grant") {
		t.Errorf("bad secret = %v", res.err)
	}

	res = fetch(t, map[string]string{"reddit.client_id": "id", "reddit.secret": "s", "reddit.username": "bob", "reddit.password": "nope"},
		"pics", nil)
	if res.err == nil || !strings.Contains(res.err.Error(), "sign-in failed") {
		t.Errorf("bad password = %v", res.err)
	}

	f.mu.Lock()
	f.oauthFails = 0
	f.mu.Unlock()
	res = fetch(t, map[string]string{"reddit.client_id": "id", "reddit.secret": "s", "reddit.username": "bob", "reddit.password": "pw"},
		"pics", map[string]string{"sweep": "new"})
	if res.err != nil || len(res.ids) != 3 {
		t.Errorf("password grant: %v %v", res.ids, res.err)
	}
}

func TestFetchErrors(t *testing.T) {
	newFakeReddit(t)
	cases := map[string]string{
		"private": "refused access",
		"missing": "couldn't find that subreddit or user",
		"search":  "couldn't find that subreddit",
		"garbled": "reading Reddit's reply",
		"teapot":  "418",
		"x y":     "doesn't look like a subreddit",
	}
	for target, want := range cases {
		res := fetch(t, nil, target, map[string]string{"sweep": "new"})
		if res.err == nil || !strings.Contains(res.err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", target, res.err, want)
		}
	}
	res := fetch(t, map[string]string{"reddit.cookie": "good"}, "private", map[string]string{"sweep": "new"})
	if res.err == nil || !strings.Contains(res.err.Error(), "cookie has expired") {
		t.Errorf("403 with a cookie should mention it: %v", res.err)
	}
	if res := fetch(t, nil, "broken", map[string]string{"sweep": "new"}); res.err == nil {
		t.Error("when every listing fails, Fetch reports the error")
	}
	if res := fetch(t, nil, "empty", map[string]string{"sweep": "new"}); res.err != nil || len(res.ids) != 0 {
		t.Errorf("an empty subreddit is fine: %v", res.err)
	}
}

func TestFetchStopsWhenEmitFails(t *testing.T) {
	newFakeReddit(t)
	src := &Reddit{}
	stop := errors.New("enough")
	n := 0
	err := src.Fetch(context.Background(), core.Request{
		Target: "pics", Options: core.WithDefaults(src.Info(), nil), Env: env(nil),
	}, func(core.Item) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Errorf("err = %v after %d items; Fetch must stop at the first emit error", err, n)
	}
}

func TestFetchHonoursCancellation(t *testing.T) {
	newFakeReddit(t)
	src := &Reddit{}
	ctx, cancel := context.WithCancel(context.Background())
	err := src.Fetch(ctx, core.Request{
		Target: "pics", Options: core.WithDefaults(src.Info(), nil), Env: env(nil),
	}, func(core.Item) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestRateLimitHeadersPause(t *testing.T) {
	f := newFakeReddit(t)
	f.limitOnce = true
	start := time.Now()
	res := fetch(t, nil, "pics", map[string]string{"sweep": "new"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("an exhausted quota should pause the next request until the reset")
	}
}
