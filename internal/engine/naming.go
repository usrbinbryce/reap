package engine

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/usrbinbryce/reap/internal/core"
)

const (
	DefaultFolder = "{source}/{collection}"
	DefaultName   = "{prefix}{id}"
)

var Tokens = map[string]string{
	"prefix":     "your prefix",
	"source":     "your source",
	"collection": "subreddit or user",
	"id":         "post id, plus _2, _3 for galleries",
	"author":     "who posted it",
	"title":      "post title, first 60 characters",
	"date":       "post date, 2026-10-31",
	"hash":       "first 12 characters of the SHA-256",
	"variant":    "hd, sd and so on when a host offers several",
}

var tokenRe = regexp.MustCompile(`\{([a-z_]+)\}`)

// Reports unknown tokens in a folder or file name template

func CheckTemplate(t string) error {
	var bad []string
	for _, m := range tokenRe.FindAllStringSubmatch(t, -1) {
		if _, ok := Tokens[m[1]]; !ok {
			bad = append(bad, m[0])
		}
	}
	if len(bad) == 0 {
		return nil
	}
	known := make([]string, 0, len(Tokens))
	for k := range Tokens {
		known = append(known, "{"+k+"}")
	}
	sort.Strings(known)
	return fmt.Errorf("%s isn't a name token. use %s", strings.Join(bad, ", "), strings.Join(known, " "))
}

type namer struct{ folder, name, prefix string }

func (n namer) rel(it core.Item, m core.Media, ext string, sum []byte) string {
	h := hex.EncodeToString(sum)
	if len(h) > 12 {
		h = h[:12]
	}
	vals := map[string]string{
		"prefix":     n.prefix,
		"source":     it.Source,
		"collection": it.Collection,
		"id":         it.ID,
		"author":     it.Author,
		"title":      truncRunes(it.Title, 60),
		"hash":       h,
		"variant":    m.Variant,
		"date":       "",
	}
	if !it.Created.IsZero() {
		vals["date"] = it.Created.Format("2006-01-02")
	}

	var segs []string
	for _, s := range strings.Split(render(n.folder, vals), "/") {
		if s = cleanSegment(s); s != "" {
			segs = append(segs, s)
		}
	}
	file := cleanSegment(render(n.name, vals))
	if file == "" {
		file = h
	}
	segs = append(segs, file+"."+ext)
	return filepath.Join(segs...)
}

var slashes = strings.NewReplacer("/", "-", "\\", "-")

func render(t string, vals map[string]string) string {
	return tokenRe.ReplaceAllStringFunc(t, func(tok string) string {
		v, ok := vals[tok[1:len(tok)-1]]
		if !ok {
			return tok
		}
		return slashes.Replace(v)
	})
}

// makes s safe as one path segment on Linux, MacOS and Windows
func cleanSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 32 || r == 127:
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return truncBytes(strings.Trim(b.String(), " ."), 120)
}

func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], " .")
}

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n]))
}

// uniquePath returns p, or p with -2, -3... before the extension if taken.
// Only an existing file counts as taken. Any other Lstat error (say, a parent
// that's a file) returns the path as is, so the write fails with a real error
// instead of this looping forever.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Lstat(c); err != nil {
			return c
		}
	}
}
