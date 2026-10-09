package core

import (
	"net/url"
	"testing"
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
