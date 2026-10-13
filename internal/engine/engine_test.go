package engine_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
	tk "github.com/usrbinbryce/reap/internal/testkit"
)

func start(t *testing.T, j engine.Job) *engine.Run {
	t.Helper()
	r, err := engine.Start(context.Background(), j, tk.Env(nil))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func run(t *testing.T, j engine.Job, items ...core.Item) ([]engine.Event, engine.Stats) {
	t.Helper()
	tk.Set(tk.Script{Items: items})
	return tk.Drain(t, start(t, j))
}

// files lists saved files under dir, relative, ignoring .reap.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".reap" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

type rec struct {
	Path, SHA256, PHash, Ext, Kind, Source, Collection, ID, MediaURL, Variant, Title, Author, Created string
	NearDupOf                                                                                         string `json:"near_dup_of"`
	PageURL                                                                                           string `json:"page_url"`
	Bytes                                                                                             int64
	Score                                                                                             int
}

func manifest(t *testing.T, dir string) []rec {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, ".reap", "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []rec
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r rec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func reasons(evs []engine.Event, k engine.EventKind) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e.Item.ID+": "+e.Reason)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestSavesFilesAndManifest(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Prefix = "p_"
	evs, st := run(t, j,
		tk.Item("a", srv.U("img/a.png")),
		tk.Item("b", srv.U("jpg/b.jpg")),
		tk.Item("c", srv.U("gif/c.gif")),
	)
	if st.Saved != 3 || st.Found != 3 || st.Failed != 0 || st.Bytes <= 0 || st.Err != nil {
		t.Fatalf("stats = %+v\nskipped: %v\nfailed: %v", st, reasons(evs, engine.EvSkipped), reasons(evs, engine.EvFailed))
	}
	want := []string{"fake/box/p_a.png", "fake/box/p_b.jpg", "fake/box/p_c.gif"}
	if got := files(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v", got, want)
	}
	for _, k := range []engine.EventKind{engine.EvStatus, engine.EvFound, engine.EvStart, engine.EvIdle} {
		if tk.Count(evs, k) == 0 {
			t.Errorf("no events of kind %d", k)
		}
	}
	if st.Ended.Before(st.Started) || st.Stopped || st.LimitHit {
		t.Errorf("final stats = %+v", st)
	}

	m := manifest(t, dir)
	if len(m) != 3 {
		t.Fatalf("manifest has %d lines", len(m))
	}
	byID := map[string]rec{}
	for _, r := range m {
		byID[r.ID] = r
	}
	a := byID["a"]
	if a.Path != "fake/box/p_a.png" || len(a.SHA256) != 64 || a.PHash == "" || a.Ext != "png" || a.Kind != "image" ||
		a.Source != "fake" || a.Collection != "box" || a.Title != "Title a" || a.Author != "someone" ||
		a.Created != "2024-05-31T12:00:00Z" || a.Score != 7 || a.Bytes == 0 || a.PageURL != srv.U("img/a.png") {
		t.Errorf("manifest record = %+v", a)
	}
	if byID["c"].Kind != "animated" || byID["c"].PHash != "" {
		t.Error("gifs are animated and don't get a perceptual hash")
	}
}

func TestExactDuplicatesAcrossItemsAndRuns(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Workers = 1
	evs, st := run(t, j, tk.Item("one", srv.U("img/same.png")), tk.Item("two", srv.U("img/same.png?copy=1")))
	if st.Saved != 1 || st.Dupes != 1 {
		t.Fatalf("stats = %+v", st)
	}
	for _, e := range evs {
		if e.Kind == engine.EvDuplicate && e.Other != "fake/box/one.png" {
			t.Errorf("duplicate points at %q", e.Other)
		}
	}

	// A different collection in the same dataset folder still dedupes.
	it := tk.Item("three", srv.U("img/same.png?copy=2"))
	it.Collection = "other"
	_, st2 := run(t, j, it)
	if st2.Saved != 0 || st2.Dupes != 1 {
		t.Errorf("cross-collection dedupe failed: %+v", st2)
	}
	if len(files(t, dir)) != 1 {
		t.Errorf("files = %v", files(t, dir))
	}
}

func TestRerunSkipsSeenAndRescanRechecks(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	items := []core.Item{tk.Item("a", srv.U("img/a.png")), tk.Item("b", srv.U("img/b.png"))}
	run(t, j, items...)

	evs, st := run(t, j, items...)
	if st.Known != 2 || st.Found != 0 || st.Saved != 0 || tk.Count(evs, engine.EvKnown) != 2 {
		t.Errorf("rerun stats = %+v", st)
	}
	if srv.Hits("img/a.png") != 1 {
		t.Errorf("seen items were downloaded again: %d hits", srv.Hits("img/a.png"))
	}

	j.Rescan = true
	_, st = run(t, j, items...)
	if st.Known != 0 || st.Found != 2 || st.Dupes != 2 || st.Saved != 0 {
		t.Errorf("rescan stats = %+v", st)
	}
}

func TestLimitStopsAtNewFiles(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Limit = 2
	j.Workers = 1
	var items []core.Item
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		items = append(items, tk.Item(id, srv.U("img/"+id+".png")))
	}
	_, st := run(t, j, items...)
	if st.Saved != 2 || !st.LimitHit || st.Stopped {
		t.Errorf("stats = %+v", st)
	}
	if n := len(files(t, dir)); n != 2 {
		t.Errorf("%d files saved, want 2", n)
	}
	// Items that weren't reached stay unseen, so the next run carries on.
	j.Limit = 0
	_, st = run(t, j, items...)
	if st.Saved != 3 {
		t.Errorf("follow-up run saved %d, want 3", st.Saved)
	}
}

func TestFileTypes(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Types = []string{".JPEG"} // normalized to jpg
	evs, st := run(t, j,
		tk.Item("png", srv.U("img/a.png")),   // rejected by the URL's extension
		tk.Item("raw", srv.U("album/raw/b")), // no extension; rejected after sniffing
		tk.Item("jpg", srv.U("jpg/c.jpg")),   // allowed
		tk.Item("alt", srv.U("alts/d")),      // gif with a png alternate: neither allowed
	)
	if st.Saved != 1 || st.Skipped != 3 {
		t.Fatalf("stats = %+v, skipped: %v", st, reasons(evs, engine.EvSkipped))
	}
	sk := reasons(evs, engine.EvSkipped)
	if !contains(sk, "png: png isn't in your file types") || !contains(sk, "raw: png isn't in your file types") ||
		!contains(sk, "alt: gif isn't in your file types") {
		t.Errorf("skip reasons = %v", sk)
	}

	// Type skips aren't remembered, so allowing more types later picks them up.
	j.Types = []string{"jpg", "png"}
	_, st = run(t, j,
		tk.Item("png", srv.U("img/a.png")),
		tk.Item("raw", srv.U("album/raw/b")),
		tk.Item("alt", srv.U("alts/d")),
	)
	if st.Saved != 3 {
		t.Errorf("second run saved %d, want 3", st.Saved)
	}
	got := strings.Join(files(t, dir), ",")
	if !strings.Contains(got, "fake/box/raw.png") {
		t.Errorf("sniffed file should get the real extension: %s", got)
	}
	for _, r := range manifest(t, dir) {
		if r.ID == "alt" && (r.Ext != "png" || r.Variant != "hd") {
			t.Errorf("alternate record = %+v (variant should be inherited)", r)
		}
	}
}

func TestPermanentSkipsAreRemembered(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	items := []core.Item{
		tk.Item("missing", srv.U("missing.png")),
		tk.Item("page", srv.U("album/page")),
		tk.Item("removed", srv.U("removed.png")),
		tk.Item("gone", srv.U("gone/x")),
	}
	evs, st := run(t, j, items...)
	if st.Skipped != 4 || st.Saved != 0 {
		t.Fatalf("stats = %+v, %v", st, reasons(evs, engine.EvSkipped))
	}
	sk := reasons(evs, engine.EvSkipped)
	for _, want := range []string{"no longer has it (404)", "sent a text/html page", "has removed it", "the fake host removed it"} {
		if !contains(sk, want) {
			t.Errorf("missing skip reason %q in %v", want, sk)
		}
	}
	_, st = run(t, j, items...)
	if st.Known != 4 {
		t.Errorf("permanent skips should be remembered: %+v", st)
	}
}

func TestSoftSkipsAreRetried(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	items := []core.Item{
		tk.Item("key", srv.U("needskey/x")),
		tk.Item("host", "https://unknown.example/page"),
		tk.Item("bad", "::not a url"),
		tk.Item("empty", ""),
	}
	evs, st := run(t, j, items...)
	sk := reasons(evs, engine.EvSkipped)
	if st.Skipped != 4 || !contains(sk, "needs a key") || !contains(sk, "unknown.example isn't supported yet") ||
		!contains(sk, "not a usable link") || !contains(sk, "nothing to download") {
		t.Fatalf("stats = %+v, %v", st, sk)
	}
	_, st = run(t, j, items...)
	if st.Known != 0 || st.Found != 4 {
		t.Errorf("soft skips should be retried: %+v", st)
	}
}

func TestFailuresAreRetriedNextRun(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	items := []core.Item{
		tk.Item("oops", srv.U("oops.png")),
		tk.Item("short", srv.U("short.jpg")),
		tk.Item("teapot", srv.U("teapot.png")),
		tk.Item("dead", tk.DeadURL()),
		tk.Item("broken", srv.U("broken/x")),
	}
	evs, st := run(t, j, items...)
	if st.Failed != 5 {
		t.Fatalf("stats = %+v\nfailed: %v\nskipped: %v", st, reasons(evs, engine.EvFailed), reasons(evs, engine.EvSkipped))
	}
	f := reasons(evs, engine.EvFailed)
	if !contains(f, "oops: 127.0.0.1 answered 500") || !contains(f, "teapot: 127.0.0.1 answered 418") {
		t.Errorf("failure reasons = %v", f)
	}
	for _, e := range evs {
		if e.Kind == engine.EvFailed && e.Err == nil {
			t.Error("failed events carry the error")
		}
	}
	if len(files(t, dir)) != 0 {
		t.Errorf("failed downloads left files: %v", files(t, dir))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".reap", "tmp", "*.part")); len(left) != 0 {
		t.Errorf("partial files left behind: %v", left)
	}
	_, st = run(t, j, items...)
	if st.Known != 0 || st.Found != 5 {
		t.Errorf("failures must not be marked seen: %+v", st)
	}
}

func TestGalleries(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Workers = 1
	evs, st := run(t, j, tk.Item("g", srv.U("album/img/g1.png,img/g2.png,img/g3.png")))
	if st.Saved != 3 {
		t.Fatalf("stats = %+v %v", st, reasons(evs, engine.EvFailed))
	}
	want := "fake/box/g_1.png,fake/box/g_2.png,fake/box/g_3.png"
	if got := strings.Join(files(t, dir), ","); got != want {
		t.Errorf("files = %s", got)
	}
	_, st = run(t, j, tk.Item("g", srv.U("album/img/g1.png,img/g2.png,img/g3.png")))
	if st.Known != 1 {
		t.Errorf("a complete gallery is marked seen: %+v", st)
	}
}

func TestPartialGalleryResumesOnlyTheMissingPart(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Workers = 1
	it := tk.Item("p", srv.U("album/img/p1.png,oops.png,img/p3.png"))
	_, st := run(t, j, it)
	if st.Saved != 2 || st.Failed != 1 {
		t.Fatalf("stats = %+v", st)
	}
	_, st = run(t, j, it)
	if st.Known != 0 || st.Saved != 0 || st.Failed != 1 || st.Dupes != 0 {
		t.Errorf("rerun should retry only the failed part: %+v", st)
	}
	if srv.Hits("img/p1.png") != 1 {
		t.Errorf("finished gallery parts were fetched again: %d", srv.Hits("img/p1.png"))
	}
}

func TestFallbacks(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Types = []string{"png"}

	gone := tk.Item("gone", srv.U("gone/x"))
	gone.Fallbacks = []string{"", srv.U("img/fb1.png")}

	wrongType := tk.Item("type", srv.U("gif/x.gif"))
	wrongType.Fallbacks = []string{srv.U("img/fb2.png")}

	allBad := tk.Item("bad", srv.U("gone/y"))
	allBad.Fallbacks = []string{srv.U("needskey/z")}

	evs, st := run(t, j, gone, wrongType, allBad)
	if st.Saved != 2 || st.Skipped != 1 {
		t.Fatalf("stats = %+v %v", st, reasons(evs, engine.EvSkipped))
	}
	// The first error is the one reported.
	if sk := reasons(evs, engine.EvSkipped); !contains(sk, "bad: the fake host removed it") {
		t.Errorf("skip reasons = %v", sk)
	}
}

func TestLookAlikes(t *testing.T) {
	srv := tk.NewServer(t)
	items := []core.Item{tk.Item("orig", srv.U("img/n.png")), tk.Item("big", srv.U("big/n.png")), tk.Item("flat", srv.U("flat.png"))}

	t.Run("flag", func(t *testing.T) {
		dir := t.TempDir()
		j := tk.Job(dir)
		j.Workers = 1
		evs, st := run(t, j, items...)
		if st.Saved != 3 || st.Near != 1 {
			t.Fatalf("stats = %+v", st)
		}
		for _, e := range evs {
			if e.Kind == engine.EvNear && (e.Path == "" || e.Other != "fake/box/orig.png" || !strings.Contains(e.Reason, "kept")) {
				t.Errorf("near event = %+v", e)
			}
		}
		for _, r := range manifest(t, dir) {
			if r.ID == "big" && r.NearDupOf != "fake/box/orig.png" {
				t.Errorf("manifest near_dup_of = %q", r.NearDupOf)
			}
		}
	})

	t.Run("skip", func(t *testing.T) {
		dir := t.TempDir()
		j := tk.Job(dir)
		j.Workers = 1
		j.Dedupe = engine.DedupeSkip
		j.Limit = 10
		_, st := run(t, j, items...)
		if st.Saved != 2 || st.Near != 1 {
			t.Fatalf("stats = %+v", st)
		}
		if got := strings.Join(files(t, dir), ","); strings.Contains(got, "big") {
			t.Errorf("skipped look-alike still on disk: %s", got)
		}
		if len(manifest(t, dir)) != 2 {
			t.Error("skipped look-alike written to the manifest")
		}
	})

	t.Run("exact", func(t *testing.T) {
		dir := t.TempDir()
		j := tk.Job(dir)
		j.Dedupe = engine.DedupeExact
		_, st := run(t, j, items...)
		if st.Saved != 3 || st.Near != 0 {
			t.Errorf("stats = %+v", st)
		}
		for _, r := range manifest(t, dir) {
			if r.PHash != "" {
				t.Error("exact mode shouldn't spend time hashing")
			}
		}
	})
}

func TestNameCollisionsGetSuffixes(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Folder = "flat"
	j.Name = "same"
	j.Workers = 1
	_, st := run(t, j, tk.Item("a", srv.U("img/a.png")), tk.Item("b", srv.U("img/b.png")))
	if st.Saved != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if got := strings.Join(files(t, dir), ","); got != "flat/same-2.png,flat/same.png" {
		t.Errorf("files = %s", got)
	}
}

func TestSourceErrorIsReported(t *testing.T) {
	srv := tk.NewServer(t)
	tk.Set(tk.Script{Items: []core.Item{tk.Item("a", srv.U("img/a.png"))}, Err: errors.New("listing broke")})
	r := start(t, tk.Job(t.TempDir()))
	_, st := tk.Drain(t, r)
	if st.Err == nil || st.Err.Error() != "listing broke" || st.Saved != 1 {
		t.Errorf("stats = %+v", st)
	}
	if r.Info().Name != "fake" {
		t.Error("Info")
	}
}

func TestStopAbandonsInFlightWork(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	tk.Set(tk.Script{Items: []core.Item{tk.Item("slow", srv.U("slow/x.png"))}, Hold: true})
	r := start(t, tk.Job(dir))
	for ev := range r.Events {
		if ev.Kind == engine.EvStart && strings.Contains(ev.URL, "slow") {
			break
		}
	}
	r.Stop()
	_, st := tk.Drain(t, r)
	if !st.Stopped || st.Err != nil || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
	_, st = run(t, tk.Job(dir), tk.Item("slow2", srv.U("img/s.png")))
	if st.Saved != 1 {
		t.Error("the index should be closed and reusable after a stop")
	}
}

func TestPauseAndResume(t *testing.T) {
	srv := tk.NewServer(t)
	tk.Set(tk.Script{Items: []core.Item{tk.Item("a", srv.U("img/a.png"))}, Hold: true})
	r := start(t, tk.Job(t.TempDir()))
	r.Pause()
	if !r.Paused() {
		t.Fatal("Paused")
	}
	r.Resume()
	if r.Paused() {
		t.Fatal("Resume")
	}
	deadline := time.After(10 * time.Second)
	for saved := false; !saved; {
		select {
		case ev := <-r.Events:
			saved = ev.Kind == engine.EvSaved
		case <-deadline:
			t.Fatal("nothing saved after resuming")
		}
	}
	r.Pause()
	r.Stop() // stopping a paused run must not hang
	tk.Drain(t, r)
	if s := r.Stats(); s.Saved != 1 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestStartValidation(t *testing.T) {
	dir := t.TempDir()
	base := tk.Job(dir)
	cases := map[string]func(*engine.Job){
		"unknown source": func(j *engine.Job) { j.Source = "nope" },
		"no target":      func(j *engine.Job) { j.Target = "  " },
		"no folder":      func(j *engine.Job) { j.OutDir = "" },
		"bad folder":     func(j *engine.Job) { j.Folder = "{oops}" },
		"bad name":       func(j *engine.Job) { j.Name = "{x}" },
		"no types":       func(j *engine.Job) { j.Types = []string{" ", ""} },
		"bad dedupe":     func(j *engine.Job) { j.Dedupe = "maybe" },
	}
	for name, mod := range cases {
		j := base
		mod(&j)
		if _, err := engine.Start(context.Background(), j, tk.Env(nil)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	blocked := filepath.Join(dir, "afile")
	os.WriteFile(blocked, nil, 0o644)
	j := base
	j.OutDir = blocked
	if _, err := engine.Start(context.Background(), j, tk.Env(nil)); err == nil {
		t.Error("a file as the output folder should fail")
	}
}

func TestStartNormalizesDefaults(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".reap", "tmp", "dl-old.part")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("x"), 0o644)

	tk.Set(tk.Script{})
	j := tk.Job(dir)
	j.Workers, j.Limit, j.Dedupe, j.Folder, j.Name = 0, -5, "", " ", ""
	r := start(t, j)
	tk.Drain(t, r)
	if r.Job.Workers != 6 || r.Job.Limit != 0 || r.Job.Dedupe != engine.DedupeFlag ||
		r.Job.Folder != engine.DefaultFolder || r.Job.Name != engine.DefaultName || !filepath.IsAbs(r.Job.OutDir) {
		t.Errorf("normalized job = %+v", r.Job)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale partial downloads should be cleaned up at start")
	}

	j.Workers = 100
	r = start(t, j)
	tk.Drain(t, r)
	if r.Job.Workers != 32 {
		t.Errorf("workers capped at %d, want 32", r.Job.Workers)
	}
}

func TestSecondRunOnSameFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	tk.Set(tk.Script{Hold: true})
	r := start(t, tk.Job(dir))
	defer func() { r.Stop(); tk.Drain(t, r) }()
	if _, err := engine.Start(context.Background(), tk.Job(dir), tk.Env(nil)); err == nil ||
		!strings.Contains(err.Error(), "another reap run") {
		t.Errorf("err = %v", err)
	}
}

func TestBlockedManifestFailsStart(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".reap", "manifest.jsonl"), 0o755)
	if _, err := engine.Start(context.Background(), tk.Job(dir), tk.Env(nil)); err == nil {
		t.Error("an unwritable manifest should fail")
	}
	// And the index lock was released on the way out.
	os.Remove(filepath.Join(dir, ".reap", "manifest.jsonl"))
	tk.Set(tk.Script{})
	tk.Drain(t, start(t, tk.Job(dir)))
}

func TestUnwritableDestinationFails(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "fake"), 0o755)
	os.WriteFile(filepath.Join(dir, "fake", "box"), []byte("a file where a folder goes"), 0o644)
	evs, st := run(t, tk.Job(dir), tk.Item("a", srv.U("img/a.png")))
	if st.Failed != 1 || st.Saved != 0 {
		t.Fatalf("stats = %+v", st)
	}
	_ = evs
	// The failed claim was released, so fixing the folder lets the next run save it.
	os.Remove(filepath.Join(dir, "fake", "box"))
	_, st = run(t, tk.Job(dir), tk.Item("a", srv.U("img/a.png")))
	if st.Saved != 1 || st.Dupes != 0 {
		t.Errorf("retry stats = %+v", st)
	}
}

func TestUnsniffableFileUsesURLExtension(t *testing.T) {
	srv := tk.NewServer(t)
	dir := t.TempDir()
	j := tk.Job(dir)
	j.Types = []string{"avif"}
	it := tk.Item("blob", srv.U("blob.avif"))
	it.Source = "" // filled in from the source's name
	_, st := run(t, j, it)
	if st.Saved != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if got := strings.Join(files(t, dir), ","); got != "fake/box/blob.avif" {
		t.Errorf("files = %s", got)
	}
}
