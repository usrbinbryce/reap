package core

import (
	"strings"

	"github.com/usrbinbryce/reap/internal/httpx"
)

// simple struct that plugins get to talk to the outside
type Env struct {
	HTTP      *httpx.Client
	UserAgent string
	secret    func(string) string
}

// builds the shared http client and applies every plugin's rate limits
// call it after plugins are registered
func NewEnv(userAgent string, secret func(string) string) *Env {
	c := httpx.NewClient(userAgent)
	apply := func(v any) {
		if rl, ok := v.(RateLimiter); ok {
			for host, every := range rl.Rates() {
				c.SetRate(host, every)
			}
		}
	}

	for _, s := range Sources() {
		apply(s)
	}

	for _, r := range resolvers {
		apply(r)
	}

	if secret == nil {
		secret = func(string) string { return "" }
	}

	return &Env{HTTP: c, UserAgent: userAgent, secret: secret}
}

func (e *Env) Secret(key string) string { return strings.TrimSpace(e.secret(key)) }
