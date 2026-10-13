package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/httpx"
)

// reddit enpoints
var (
	wwwBase   = "https://www.reddit.com"
	oauthBase = "https://oauth.reddit.com"
)

type client struct {
	env  *core.Env
	auth *auth
}

type listingResp struct {
	Data struct {
		After    string `json:"after"`
		Children []struct {
			Kind string `json:"kind"`
			Data post   `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

func (c *client) walk(ctx context.Context, l listing, progress func(page int), each func(*post) error) error {
	after := ""
	for page := 1; ; page++ {
		progress(page)
		q := url.Values{}
		q.Set("limit", "100")
		q.Set("raw_json", "1") // unescaped URLs, no &amp;
		if l.sort != "" {
			q.Set("sort", l.sort)
		}
		if l.t != "" {
			q.Set("t", l.t)
		}
		if after != "" {
			q.Set("after", after)
		}
		var res listingResp
		if err := c.get(ctx, l.path, q, &res); err != nil {
			return err
		}
		for i := range res.Data.Children {
			ch := &res.Data.Children[i]
			if ch.Kind != "t3" {
				continue
			}
			if err := each(&ch.Data); err != nil {
				return err
			}
		}
		if res.Data.After == "" || len(res.Data.Children) == 0 {
			return nil
		}
		after = res.Data.After
	}
}

func (c *client) get(ctx context.Context, path string, q url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		base, hdr, err := c.auth.prepare(ctx)
		if err != nil {
			return err
		}
		u := base + path
		if c.auth.mode() != modeOAuth {
			u += ".json"
		}
		u += "?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := c.env.HTTP.Do(ctx, req)
		if err != nil {
			return err
		}
		c.respectLimits(resp)

		switch resp.StatusCode {
		case http.StatusOK:
			defer resp.Body.Close()
			// Unknown subreddits redirect to search when signed out.
			if strings.Contains(resp.Request.URL.Path, "/search") {
				return &fatalErr{"Reddit couldn't find that subreddit."}
			}
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return fmt.Errorf("reading Reddit's reply: %w", err)
			}
			return nil
		case http.StatusUnauthorized:
			resp.Body.Close()
			if attempt == 0 && c.auth.invalidate() {
				continue
			}
			return &fatalErr{c.auth.rejected()}
		case http.StatusForbidden:
			resp.Body.Close()
			if c.auth.mode() == modeCookie {
				return &fatalErr{"Reddit refused access. The subreddit may be private, banned or quarantined, or your cookie has expired."}
			}
			return &fatalErr{"Reddit refused access. The subreddit may be private, banned or quarantined, or Reddit is blocking signed-out requests from your network."}
		case http.StatusNotFound:
			resp.Body.Close()
			return &fatalErr{"Reddit couldn't find that subreddit or user."}
		default:
			resp.Body.Close()
			return &httpx.StatusError{Code: resp.StatusCode, URL: u}
		}
	}
	return &fatalErr{"Reddit sign-in keeps expiring."}
}

// respectLimits pauses when Reddit says the quota for this window is spent
func (c *client) respectLimits(resp *http.Response) {
	rem, err1 := strconv.ParseFloat(resp.Header.Get("X-Ratelimit-Remaining"), 64)
	reset, err2 := strconv.ParseFloat(resp.Header.Get("X-Ratelimit-Reset"), 64)
	if err1 != nil || err2 != nil || rem >= 2 {
		return
	}
	c.env.HTTP.Backoff(resp.Request.URL.Hostname(), time.Duration((reset+1)*float64(time.Second)))
}

// auth picks one of three ways to talk to Reddit, best first:
//
//   - OAuth with a "script" app (REAP_REDDIT_CLIENT_ID + REAP_REDDIT_SECRET,
//     optionally a username and password to act as that account)
//   - your browser's signed-in cookie (REAP_REDDIT_COOKIE), sent to the same
//     .json endpoints the website uses
//   - signed out, which is slow, often blocked, and may hide NSFW subreddits
type authMode int

const (
	modePublic authMode = iota
	modeCookie
	modeOAuth
)

type auth struct {
	env                    *core.Env
	id, secret, user, pass string
	cookie                 string // full Cookie header value
	cookieErr              error  // why the configured cookie couldn't be used

	mu    sync.Mutex
	token string
	exp   time.Time
	who   string // account the cookie belongs to, once checked
}

func newAuth(env *core.Env) *auth {
	a := &auth{
		env:    env,
		id:     env.Secret("reddit.client_id"),
		secret: env.Secret("reddit.secret"),
		user:   env.Secret("reddit.username"),
		pass:   env.Secret("reddit.password"),
	}
	if raw := env.Secret("reddit.cookie"); raw != "" {
		a.cookie, a.cookieErr = parseCookie(raw)
	}
	return a
}

func (a *auth) mode() authMode {
	switch {
	case a.id != "":
		return modeOAuth
	case a.cookie != "" || a.cookieErr != nil:
		return modeCookie
	}
	return modePublic
}

func (a *auth) describe() string {
	switch a.mode() {
	case modeOAuth:
		if a.user != "" && a.pass != "" {
			return "Signed in to Reddit as u/" + a.user + "."
		}
		return "Using your Reddit app key. Add a username and password to see everything your account can."
	case modeCookie:
		if a.cookieErr != nil {
			return "Your Reddit cookie can't be used: " + a.cookieErr.Error()
		}
		if a.who != "" {
			return "Signed in to Reddit with your browser cookie as u/" + a.who + "."
		}
		return "Using your Reddit browser cookie."
	}
	return "Reading Reddit signed out, which is slow and may hide NSFW subreddits. Set REAP_REDDIT_COOKIE, or REAP_REDDIT_CLIENT_ID and REAP_REDDIT_SECRET, to sign in."
}

func (a *auth) rejected() string {
	if a.mode() == modeCookie {
		return "Reddit no longer accepts your cookie. Sign in to reddit.com in your browser and copy a fresh reddit_session."
	}
	return "Reddit rejected the sign-in. Check your Reddit app ID, secret and password."
}

// ensure checks credentials before the first listing, so a bad cookie or key
// fails with a clear message instead of an empty run
func (a *auth) ensure(ctx context.Context) error {
	switch a.mode() {
	case modeOAuth:
		_, _, err := a.prepare(ctx)
		return err
	case modeCookie:
		if a.cookieErr != nil {
			return &fatalErr{"Your Reddit cookie can't be used: " + a.cookieErr.Error()}
		}
		return a.checkCookie(ctx)
	}
	return nil
}

// checkCookie asks Reddit who the cookie belongs to
func (a *auth) checkCookie(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wwwBase+"/api/me.json", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", a.cookie)
	resp, err := a.env.HTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("checking your Reddit cookie: %w", err)
	}
	defer resp.Body.Close()
	var me struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&me)
	}
	if me.Data.Name == "" {
		return &fatalErr{"Reddit didn't recognise your cookie. It has probably expired; sign in to reddit.com in your browser and copy a fresh reddit_session."}
	}
	a.mu.Lock()
	a.who = me.Data.Name
	a.mu.Unlock()
	return nil
}

func (a *auth) prepare(ctx context.Context) (string, http.Header, error) {
	h := http.Header{}
	switch a.mode() {
	case modePublic:
		h.Set("Cookie", "over18=1") // skip the NSFW interstitial
		return wwwBase, h, nil
	case modeCookie:
		h.Set("Cookie", a.cookie)
		return wwwBase, h, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == "" || time.Now().After(a.exp) {
		if err := a.refresh(ctx); err != nil {
			return "", nil, err
		}
	}
	h.Set("Authorization", "bearer "+a.token)
	return oauthBase, h, nil
}

func (a *auth) invalidate() bool {
	if a.mode() != modeOAuth {
		return false
	}
	a.mu.Lock()
	a.token = ""
	a.mu.Unlock()
	return true
}

func (a *auth) refresh(ctx context.Context) error {
	form := url.Values{}
	if a.user != "" && a.pass != "" {
		form.Set("grant_type", "password")
		form.Set("username", a.user)
		form.Set("password", a.pass)
	} else {
		form.Set("grant_type", "client_credentials")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wwwBase+"/api/v1/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.id, a.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.env.HTTP.Do(ctx, req)
	if err != nil {
		return fmt.Errorf("signing in to Reddit: %w", err)
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		why := tr.Error
		if why == "" {
			why = resp.Status
		}
		return &fatalErr{fmt.Sprintf("Reddit sign-in failed (%s). Check the app ID, secret, username and password.", why)}
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	a.token = tr.AccessToken
	a.exp = time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - time.Minute)
	return nil
}
