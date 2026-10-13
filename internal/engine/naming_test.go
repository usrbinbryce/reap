package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
)

func TestCheckTemplate(t *testing.T) {
	for _, ok := range []string{DefaultFolder, DefaultName, "{date}/{author}_{title}_{hash}{variant}", "plain"} {
		if err := CheckTemplate(ok); err != nil {
			t.Errorf("CheckTemplate(%q) = %v", ok, err)
		}
	}
	err := CheckTemplate("{id}_{nope}_{bad}")
	if err == nil || !strings.Contains(err.Error(), "{nope}, {bad}") || !strings.Contains(err.Error(), "{prefix}") {
		t.Errorf("unknown tokens: %v", err)
	}
}

var sum = []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9}

func item() core.Item {
	return core.Item{Source: "reddit", Collection: "pics", ID: "abc", Author: "bob",
		Title: "A/B: test?", Created: time.Date(2024, 5, 31, 0, 0, 0, 0, time.UTC)}
}

func TestRelDefault(t *testing.T) {
	n := namer{folder: DefaultFolder, name: DefaultName, prefix: "ep_"}
	got := n.rel(item(), core.Media{}, "jpg", sum)
	if want := filepath.Join("reddit", "pics", "ep_abc.jpg"); got != want {
		t.Errorf("rel = %q, want %q", got, want)
	}
}

func TestRelAllTokens(t *testing.T) {
	n := namer{folder: "{source}/{date}/{author}", name: "{title}_{hash}_{variant}_{unknown}"}
	got := n.rel(item(), core.Media{Variant: "hd"}, "png", sum)
	// Slashes inside values become dashes so they can't create folders, and
	// unsafe characters become underscores.
	want := filepath.Join("reddit", "2024-05-31", "bob", "A-B_ test__deadbeef0102_hd_{unknown}.png")
	if got != want {
		t.Errorf("rel = %q\nwant  %q", got, want)
	}
}

func TestRelEmptyPartsCollapse(t *testing.T) {
	it := item()
	it.Created = time.Time{}
	it.Collection = ""
	n := namer{folder: "{source}//{collection}/{date}", name: "{prefix}"}
	got := n.rel(it, core.Media{}, "gif", []byte{0xab})
	if want := filepath.Join("reddit", "ab.gif"); got != want {
		t.Errorf("rel = %q, want %q (empty name falls back to the hash)", got, want)
	}
}

func TestCleanSegment(t *testing.T) {
	cases := map[string]string{
		`a<b>c:d"e/f\g|h?i*j`:    "a_b_c_d_e_f_g_h_i_j",
		"  .dots and spaces. ":   "dots and spaces",
		"tab\there\x7f":          "tabhere",
		strings.Repeat("x", 200): strings.Repeat("x", 120),
	}
	for in, want := range cases {
		if got := cleanSegment(in); got != want {
			t.Errorf("cleanSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes each
	got := truncBytes(s, 121)
	if len(got) != 120 || !strings.HasPrefix(s, got) {
		t.Errorf("truncBytes cut mid-rune: len %d", len(got))
	}
	if truncBytes("short", 10) != "short" {
		t.Error("short strings are unchanged")
	}
	if truncBytes("abc def. ", 8) != "abc def" {
		t.Errorf("trailing dots and spaces trimmed after cut: %q", truncBytes("abc def. ", 8))
	}
	if truncRunes("  héllo world  ", 5) != "héllo" || truncRunes("hi", 5) != "hi" {
		t.Error("truncRunes")
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.jpg")
	if uniquePath(p) != p {
		t.Error("free path is returned as is")
	}
	os.WriteFile(p, nil, 0o644)
	os.WriteFile(filepath.Join(dir, "a-2.jpg"), nil, 0o644)
	if got := uniquePath(p); got != filepath.Join(dir, "a-3.jpg") {
		t.Errorf("uniquePath = %q", got)
	}
}

// Regression: a parent that's a file used to make uniquePath loop forever.
func TestUniquePathParentIsAFile(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	os.WriteFile(parent, nil, 0o644)
	done := make(chan string, 1)
	go func() { done <- uniquePath(filepath.Join(parent, "a.jpg")) }()
	select {
	case got := <-done:
		if got != filepath.Join(parent, "a.jpg") {
			t.Errorf("uniquePath = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uniquePath hangs when the parent is a file")
	}
}

func TestUniq(t *testing.T) {
	got := uniq([]string{"png", "", "gif", "png"})
	if strings.Join(got, ",") != "gif,png,unknown" {
		t.Errorf("uniq = %v", got)
	}
}
