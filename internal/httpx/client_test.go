package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() { BackoffBase = time.Millisecond }

func TestDoSetsUserAgentAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.UserAgent(), r.Header.Get("X-A"))
	}))
	defer srv.Close()

	c := NewClient("reap-test/1")
	resp, err := c.Get(context.Background(), srv.URL, http.Header{"X-A": {"b"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "reap-test/1|b" {
		t.Errorf("body = %q", body)
	}

	// A caller's own User-Agent wins
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "mine")
	resp2, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.HasPrefix(string(b2), "mine|") {
		t.Errorf("custom UA overwritten: %q", b2)
	}
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()

	resp, err := NewClient("t").Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if n.Load() != 3 {
		t.Errorf("attempts = %d, want 3", n.Load())
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient("t")
	c.MaxRetries = 2
	_, err := c.Get(context.Background(), srv.URL, nil)
	if StatusCode(err) != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want a 503 StatusError", err)
	}
	if n.Load() != 3 {
		t.Errorf("attempts = %d, want 3", n.Load())
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error text = %q", err.Error())
	}
}

func TestOtherStatusesAreReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	resp, err := NewClient("t").Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestNetworkErrorsRetryThenFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing listening any more

	c := NewClient("t")
	c.MaxRetries = 1
	if _, err := c.Get(context.Background(), addr, nil); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestBodyIsResentOnRetry(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	resp, err := NewClient("t").Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[1] != "payload" {
		t.Errorf("bodies = %q", bodies)
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient("t")
	c.SetRate("127.0.0.1", time.Hour) // the second attempt would wait an hour
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.Get(ctx, srv.URL, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancel didn't interrupt the wait")
	}

	// A request started with a cancelled context fails straight away.
	if _, err := c.Get(ctx, srv.URL, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			io.WriteString(w, `{"n": 5}`)
		case "/bad":
			io.WriteString(w, `{nope`)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	c := NewClient("t")

	var out struct{ N int }
	if err := c.GetJSON(context.Background(), srv.URL+"/ok", nil, &out); err != nil || out.N != 5 {
		t.Errorf("GetJSON ok: %v %+v", err, out)
	}
	if err := c.GetJSON(context.Background(), srv.URL+"/bad", nil, &out); err == nil {
		t.Error("malformed JSON should error")
	}
	err := c.GetJSON(context.Background(), srv.URL+"/x", nil, &out)
	if StatusCode(err) != http.StatusForbidden {
		t.Errorf("err = %v, want 403", err)
	}
	if err := c.GetJSON(context.Background(), "::bad", nil, &out); err == nil {
		t.Error("bad URL should error")
	}
}

func TestStatusCodeAndHost(t *testing.T) {
	if StatusCode(errors.New("x")) != 0 || StatusCode(nil) != 0 {
		t.Error("non-status errors give 0")
	}
	wrapped := fmt.Errorf("wrap: %w", &StatusError{Code: 418, URL: "https://www.x.com/a"})
	if StatusCode(wrapped) != 418 {
		t.Error("StatusCode should unwrap")
	}
	cases := map[string]string{"https://www.reddit.com/r/x": "reddit.com", "https://i.imgur.com/a": "i.imgur.com", "not a url": "not a url"}
	for in, want := range cases {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRateLimiterSpacesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := NewClient("t")
	c.SetRate("127.0.0.1", 40*time.Millisecond)
	start := time.Now()
	for i := 0; i < 3; i++ {
		resp, err := c.Get(context.Background(), srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Errorf("3 requests took %v, want at least 80ms", el)
	}
}

func TestSetRateAppliesToSubdomains(t *testing.T) {
	c := NewClient("t")
	c.SetRate("example.com", time.Second)
	if l := c.limiter("cdn.example.com"); l.every != time.Second {
		t.Errorf("new subdomain limiter every = %v", l.every)
	}
	existing := c.limiter("img.other.com")
	c.SetRate("other.com", 2*time.Second)
	if existing.every != 2*time.Second {
		t.Error("SetRate should update existing subdomain limiters")
	}
	if c.limiter("CDN.EXAMPLE.COM") != c.limiter("cdn.example.com") {
		t.Error("hosts are case-insensitive")
	}
}

func TestBackoffPushesNextRequest(t *testing.T) {
	c := NewClient("t")
	c.Backoff("slow.example", 60*time.Millisecond)
	start := time.Now()
	if err := c.limiter("slow.example").wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("Backoff should delay the next request")
	}
	// A shorter push doesn't shorten an existing wait
	l := c.limiter("slow.example")
	l.push(time.Hour)
	before := l.next
	l.push(time.Millisecond)
	if !l.next.Equal(before) {
		t.Error("push must not move the deadline earlier")
	}
}

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), 0); err != nil {
		t.Error(err)
	}
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if err := Sleep(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("zero sleep on a done context should report it, got %v", err)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	old := BackoffBase
	BackoffBase = time.Second
	defer func() { BackoffBase = old }()
	if d := backoff(1); d < time.Second || d > 1250*time.Millisecond {
		t.Errorf("backoff(1) = %v", d)
	}
	if d := backoff(3); d < 4*time.Second {
		t.Errorf("backoff(3) = %v", d)
	}
	if d := backoff(30); d > 25*time.Second {
		t.Errorf("backoff should cap near 20s, got %v", d)
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	if retryAfter(h) != 0 {
		t.Error("missing header gives 0")
	}
	h.Set("Retry-After", "3")
	if retryAfter(h) != 3*time.Second {
		t.Error("seconds form")
	}
	h.Set("Retry-After", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat))
	if d := retryAfter(h); d < 50*time.Second || d > time.Minute {
		t.Errorf("date form = %v", d)
	}
	h.Set("Retry-After", "soon")
	if retryAfter(h) != 0 {
		t.Error("garbage gives 0")
	}
}
