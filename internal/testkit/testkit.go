package testkit

import (
	"bytes"
	"context"
	"hash/fnv"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/httpx"
)

func init() {
	httpx.BackoffBase = time.Millisecond
	core.RegisterSource(&Source{})
	core.RegisterResolver(&Resolver{})
}

type Script struct {
	Items []core.Item
	Err   error
	Hold  bool
}

var (
	mu     sync.Mutex
	script Script
)

func Set(s Script) {
	mu.Lock()
	script = s
	mu.Unlock()
}

type Source struct{}

func (*Source) Info() core.Info {
	return core.Info{
		Name: "fake", Title: "fake", TargetLabel: "Thing", TargetHint: "anything",
		TargetHelp: "Any text.",
		Options: []core.Option{
			{Key: "mode", Label: "Mode", Kind: core.OptChoice, Default: "a",
				Choices: []core.Choice{core.C("a", "Alpha"), core.C("b", "Beta")}},
			{Key: "flag", Label: "Flag", Kind: core.OptBool, Default: "false", Placeholder: "Turn it on"},
			{Key: "count", Label: "Count", Kind: core.OptInt, Placeholder: "any"},
			{Key: "note", Label: "Note", Kind: core.OptText, Placeholder: "none"},
		},
	}
}

func (*Source) MatchURL(u *url.URL) bool { return core.HostIs(u, "fake.example") }

func (*Source) SetupNote(*core.Env) string { return "Fake source ready." }

func (*Source) Rates() map[string]time.Duration {
	return map[string]time.Duration{"fake.example": time.Millisecond}
}

func (*Source) Fetch(ctx context.Context, req core.Request, emit func(core.Item) error) error {
	mu.Lock()
	s := script
	mu.Unlock()
	req.Statusf("Reading %s", req.Target)
	for _, it := range s.Items {
		if err := emit(it); err != nil {
			return err
		}
	}
	if s.Hold {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Err
}

type Resolver struct{}

func (*Resolver) Name() string { return "fake" }

func (*Resolver) Match(u *url.URL) bool {
	return strings.HasPrefix(u.Path, "/album/") || strings.HasPrefix(u.Path, "/gone/") ||
		strings.HasPrefix(u.Path, "/needskey/") || strings.HasPrefix(u.Path, "/broken/") ||
		strings.HasPrefix(u.Path, "/alts/")
}

func (*Resolver) Resolve(ctx context.Context, u *url.URL, env *core.Env) ([]core.Media, error) {
	base := u.Scheme + "://" + u.Host
	rest := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	switch rest[0] {
	case "gone":
		return nil, core.Gone("the fake host removed it")
	case "needskey":
		return nil, core.NeedsSetup("the fake host needs a key")
	case "broken":
		return nil, context.DeadlineExceeded
	case "alts":
		// A gif first, then a png alternate of the same thing
		return []core.Media{{
			URL: base + "/gif/" + rest[1] + ".gif", Ext: "gif", Kind: core.KindAnimated, Variant: "hd",
			Alternates: []core.Media{{URL: base + "/img/" + rest[1] + ".png", Ext: "png", Kind: core.KindImage}},
		}}, nil
	}
	var out []core.Media
	for _, p := range strings.Split(rest[1], ",") {
		out = append(out, core.Media{URL: base + "/" + p, Ext: core.ExtFromURL(p), Header: http.Header{"X-Test": {"1"}}})
	}
	return out, nil
}

// Server is a fake server meant for validation and testing
// Server serves generated media:
//
//	/img/<seed>.png       a distinct image per seed
//	/jpg/<seed>.jpg       the same picture as /img/<seed>.png, as a jpeg
//	/big/<seed>.png       the same picture rendered larger (a look-alike)
//	/gif/<seed>.gif       a small gif
//	/blob.avif            unidentifiable bytes; only the URL gives the type
//	/flat.png             a single-colour image
//	/page                 an HTML page
//	/missing.png          404
//	/oops.png             500 every time
//	/removed.png          a valid image served at a "removed" placeholder path
//	/raw/<seed>           a png with no extension and a generic Content-Type
//	/short.jpg            promises more bytes than it sends
//	/slow/<seed>.png      waits for the request to be cancelled
type Server struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func NewServer(t testing.TB) *Server {
	s := &Server{hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) U(path string) string { return s.URL + "/" + strings.TrimPrefix(path, "/") }

func (s *Server) Hits(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits["/"+strings.TrimPrefix(path, "/")]
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits[r.URL.Path]++
	s.mu.Unlock()

	p := r.URL.Path
	seed := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(path(p), ".png"), ".jpg"), ".gif")
	switch {
	case strings.HasPrefix(p, "/img/"):
		writePNG(w, Pattern(seed, 64))
	case strings.HasPrefix(p, "/raw/"):
		// No extension and a misleading Content-Type: only sniffing would get it right
		w.Header().Set("Content-Type", "application/octet-stream")
		_ = png.Encode(w, Pattern(seed, 64))
	case strings.HasPrefix(p, "/big/"):
		writePNG(w, Pattern(seed, 160))
	case strings.HasPrefix(p, "/jpg/"):
		w.Header().Set("Content-Type", "image/jpeg")
		_ = jpeg.Encode(w, Pattern(seed, 64), &jpeg.Options{Quality: 90})
	case strings.HasPrefix(p, "/gif/"):
		w.Header().Set("Content-Type", "image/gif")
		_ = gif.Encode(w, Pattern(seed, 32), nil)
	case p == "/blob.avif":
		// Bytes the sniffer can't identify only the URL says what it is
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bytes.Repeat([]byte{0, 1, 2, 3, 250, 251}, 100))
	case p == "/flat.png":
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for i := range img.Pix {
			img.Pix[i] = 200
		}
		writePNG(w, img)
	case p == "/page":
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>not an image</body></html>"))
	case p == "/missing.png":
		http.NotFound(w, r)
	case p == "/oops.png":
		http.Error(w, "boom", http.StatusInternalServerError)
	case p == "/teapot.png":
		w.WriteHeader(http.StatusTeapot)
	case p == "/removed.png":
		writePNG(w, Pattern("removed", 64))
	case p == "/short.jpg":
		// Promise 1000 more bytes than are sent then hang up
		var buf bytes.Buffer
		_ = jpeg.Encode(&buf, Pattern("short", 64), nil)
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: image/jpeg\r\nContent-Length: " + strconv.Itoa(buf.Len()+1000) + "\r\n\r\n")
		rw.Write(buf.Bytes())
		rw.Flush()
		c.Close()
	case strings.HasPrefix(p, "/slow/"):
		<-r.Context().Done()
	default:
		http.NotFound(w, r)
	}
}

func path(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

func writePNG(w http.ResponseWriter, img image.Image) {
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, img)
}

// Draw a random 8x8 grid of random greys seeded by name at size x size
// Different seeds hash far apart; the same seed at another size hashes close
func Pattern(seed string, size int) *image.RGBA {
	h := fnv.New64a()
	h.Write([]byte(seed))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))
	var cells [8][8]uint8
	for y := range cells {
		for x := range cells[y] {
			cells[y][x] = uint8(rng.Intn(256))
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			v := cells[y*8/size][x*8/size]
			img.Set(x, y, color.RGBA{v, v / 2, 255 - v, 255})
		}
	}
	return img
}

// Returns the encoded bytes of pattern
func PNG(seed string, size int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, Pattern(seed, size))
	return buf.Bytes()
}

func Env(secrets map[string]string) *core.Env {
	return core.NewEnv("reap-test", func(k string) string { return secrets[k] })
}

func Item(id, rawURL string) core.Item {
	return core.Item{Source: "fake", Collection: "box", ID: id, URL: rawURL, Title: "Title " + id,
		Author: "someone", Created: time.Date(2024, 5, 31, 12, 0, 0, 0, time.UTC), Score: 7}
}

func DeadURL() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "http://127.0.0.1:1/x.png"
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr + "/x.png"
}

func SetHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func Isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("APPDATA", filepath.Join(dir, "cfg"))
	SetHome(t, filepath.Join(dir, "home"))
	return dir
}
