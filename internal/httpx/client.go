package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s answered %d %s", Host(e.URL), e.Code, http.StatusText(e.Code))
}

// returns the http status inside of err, or 0
func StatusCode(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// return name of a url without www.
// returns the input if it cannot be parsed
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

type Client struct {
	HC         *http.Client
	UserAgent  string
	MaxRetries int

	mu    sync.Mutex
	rates map[string]time.Duration
	lims  map[string]*limiter
}

func NewClient(userAgent string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 64
	tr.MaxIdleConnsPerHost = 16
	tr.ResponseHeaderTimeout = 45 * time.Second

	return &Client{
		HC:         &http.Client{Transport: tr},
		UserAgent:  userAgent,
		MaxRetries: 4,
		rates:      map[string]time.Duration{},
		lims:       map[string]*limiter{},
	}
}

// set rate limit (gap between requests) for each host and its subdomains
func (c *Client) SetRate(host string, every time.Duration) {
	host = strings.ToLower(host)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rates[host] = every
	for h, l := range c.lims {
		if h == host || strings.HasSuffix(h, "."+host) {
			l.setEvery(every)
		}
	}
}

// make the client leave the host alone for d, e.g. when a rate-limit
// header is sent
func (c *Client) Backoff(host string, d time.Duration) { c.limiter(host).push(d) }

func (c *Client) limiter(host string) *limiter {
	host = strings.ToLower(host)

	c.mu.Lock()
	defer c.mu.Unlock()

	if l, ok := c.lims[host]; ok {
		return l
	}

	every, ok := c.rates[host]
	if !ok {
		for h, d := range c.rates {
			if strings.HasSuffix(host, "."+h) {
				every = d
				break
			}
		}
	}

	l := &limiter{every: every}
	c.lims[host] = l
	return l
}

// sends a request, waiting for the host's rate limit
// retries on net errors, 429, and 5xx
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	lim := c.limiter(req.URL.Hostname())
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := Sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}

		if err := lim.wait(ctx); err != nil {
			return nil, err
		}

		r := req.Clone(ctx)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = body
		}

		resp, err := c.HC.Do(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if wait := retryAfter(resp.Header); wait > 0 {
				lim.push(wait)
			}
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			lastErr = &StatusError{Code: resp.StatusCode, URL: req.URL.String()}
			continue
		}
		return resp, nil
	}

	return nil, lastErr
}

// same as Do but with optional extra headers
func (c *Client) Get(ctx context.Context, rawURL string, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	for k, v := range hdr {
		req.Header[k] = v
	}
	return c.Do(ctx, req)
}

// fetches rawURL and decodes a 200 response into out. Any other
// status is returned as a *StatusError
func (c *Client) GetJSON(ctx context.Context, rawURL string, hdr http.Header, out any) error {
	resp, err := c.Get(ctx, rawURL, hdr)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &StatusError{Code: resp.StatusCode, URL: rawURL}
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var BackoffBase = 750 * time.Millisecond

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * BackoffBase
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d + time.Duration(rand.Int63n(int64(d/4)+1))
}

func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// makes every request to one host at least `every` apart
type limiter struct {
	mu    sync.Mutex
	every time.Duration
	next  time.Time
}

func (l *limiter) setEvery(d time.Duration) {
	l.mu.Lock()
	l.every = d
	l.mu.Unlock()
}

func (l *limiter) push(d time.Duration) {
	l.mu.Lock()
	if t := time.Now().Add(d); t.After(l.next) {
		l.next = t
	}
	l.mu.Unlock()
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.every)
	l.mu.Unlock()
	return Sleep(ctx, at.Sub(now))
}
