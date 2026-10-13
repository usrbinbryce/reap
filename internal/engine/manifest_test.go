package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.jsonl")
	m, err := openManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	m.write(record{Path: "a.jpg", SHA256: "x", MediaURL: "https://e.com/a?x=1&y=<2>"})
	m.write(record{Path: "b.jpg"})
	m.Close()

	// Appends on reopen rather than truncating
	m2, _ := openManifest(p)
	m2.write(record{Path: "c.jpg"})
	m2.Close()

	f, _ := os.Open(p)
	defer f.Close()
	var paths []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, r.Path)
		if r.Path == "a.jpg" && !strings.Contains(sc.Text(), "&y=<2>") {
			t.Error("URLs should be written without HTML escaping")
		}
	}
	if strings.Join(paths, ",") != "a.jpg,b.jpg,c.jpg" {
		t.Errorf("paths = %v", paths)
	}

	if _, err := openManifest(filepath.Join(t.TempDir(), "missing", "m.jsonl")); err == nil {
		t.Error("opening in a missing folder should fail")
	}
}
