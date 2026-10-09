package core

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Kind int

const (
	KindUnknown = iota
	KindImage
	KindAnimated
	KindVideo
)

func (k Kind) String() string {
	switch k {
	case KindImage:
		return "image"
	case KindAnimated:
		return "animated"
	case KindVideo:
		return "video"
	}
	return "unknown"
}

// Item is something a source found. URL may point towards a file or at
// a page a resolver knows how to turn into a file.
type Item struct {
	Source     string // name of the source, e.g. "reddit," "imgur," "wiki,"
	Collection string // subreddit, username, something to organize; used for foder names
	ID         string
	URL        string
	Fallbacks  []string
	Kind       Kind
	Title      string
	Author     string
	Created    time.Time
	NSFW       bool
	Permalink  string
	Score      int
	Extra      map[string]any // gonna use this for metadata
}

type Media struct {
	URL     string
	Kind    Kind
	Ext     string
	Header  http.Header
	Variant string

	// Other encodings of the same file in order of preference
	// e.g. gif and mp4 of same animation. The engine picks the first one
	// whose type the client allowed
	Alternates []Media
}

var extKinds = map[string]Kind{
	"jpg": KindImage, "png": KindImage, "webp": KindImage, "avif": KindImage, "heic": KindImage, "bmp": KindImage,
	"gif": KindAnimated, "mp4": KindAnimated, "webm": KindAnimated,
}

var mimeExt = map[string]string{
	"image/jpeg": "jpg", "image/jpg": "jpg", "image/pjpeg": "jpg",
	"image/png": "png", "image/webp": "webp", "image/gif": "gif",
	"image/avif": "avif", "image/heic": "heic", "image/bmp": "bmp",
	"video/mp4": "mp4", "video/webm": "webm",
}

// NormalizeExt lowercases an extension, drops the dot, and folds jpeg into jpg for uniformity
func NormalizeExt(e string) string {
	e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
	if e == "jpeg" || e == "jpe" {
		return "jpg"
	}
	return e
}

// KindOfExt returns which kind of media an extension holds
func KindOfExt(e string) Kind { return extKinds[NormalizeExt(e)] }

// ExtFromURL returns the media extension in a URL's path, or "" if there isn't one
func ExtFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	e := NormalizeExt(path.Ext(u.Path))
	if _, ok := extKinds[e]; ok {
		return e
	}
	return ""
}

// ExtFromMIME maps a Content-Type header to an extension, or ""
func ExtFromMIME(m string) string {
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	return mimeExt[strings.ToLower(strings.TrimSpace(m))]
}

// HostIs returns whether u's host is one of domains or a subdomain of one
func HostIs(u *url.URL, domains ...string) bool {
	h := strings.ToLower(u.Hostname())
	for _, d := range domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}
