package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/httpx"
	"github.com/usrbinbryce/reap/internal/index"
)

type Dedupe string

const (
	DedupeExact Dedupe = "exact" // only exact copies are skipped
	DedupeFlag  Dedupe = "flag"  // look-alikes are kept and noted
	DedupeSkip  Dedupe = "skip"  // look-alieks are deleted
)

type Job struct {
	Source  string
	Target  string
	Options map[string]string

	OutDir string // root folder for the dataset; it shares one deduping index
	Folder string // folder template below outdir
	Name   string // file name template, without ext
	Prefix string
	Types  []string

	Limit   int // how many new files to download
	Workers int
	Dedupe  Dedupe
	Rescan  bool
}

func (j *Job) normalize() error {
	j.Target = strings.TrimSpace(j.Target)
	if j.Target == "" {
		return errors.New("nothing to download yet; enter a target")
	}
	j.OutDir = core.ExpandHome(j.OutDir)
	if j.OutDir == "" {
		return errors.New("choose a folder to save into")
	}
	abs, err := filepath.Abs(j.OutDir)
	if err != nil {
		return err
	}
	j.OutDir = abs
	if strings.TrimSpace(j.Folder) == "" {
		j.Folder = DefaultFolder
	}
	if strings.TrimSpace(j.Name) == "" {
		j.Name = DefaultName
	}
	for _, t := range []string{j.Folder, j.Name} {
		if err := CheckTemplate(t); err != nil {
			return err
		}
	}
	if j.Workers <= 0 {
		j.Workers = 6
	}
	if j.Workers > 32 {
		j.Workers = 32
	}
	if j.Limit < 0 {
		j.Limit = 0
	}
	var types []string
	for _, t := range j.Types {
		if t = core.NormalizeExt(t); t != "" {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return errors.New("choose at least one file type")
	}
	j.Types = types
	switch j.Dedupe {
	case DedupeExact, DedupeFlag, DedupeSkip:
	case "":
		j.Dedupe = DedupeFlag
	default:
		return fmt.Errorf("duplicates setting %q should be exact, flag or skip", j.Dedupe)
	}
	return nil
}

type EventKind int

const (
	EvStatus EventKind = iota // source progress
	EvFound                   // an item was queued
	EvKnown                   // an item was skipped because an earlier run handled it

	EvStart     // a worker was started on item/url
	EvIdle      // a worker is free
	EvSaved     // a file was saved to path
	EvDuplicate // exact copy of other, not saved
	EvNear      // look-alike of other; saved depending on flag
	EvSkipped   // nothing saved
	EvFailed    // something went wrong; a rerun will try
	EvDone      // the run is over, state is final
)

type Event struct {
	Kind   EventKind
	Worker int
	Item   core.Item
	URL    string
	Path   string // relative to dataset root
	Other  string // the existing file, for dupes and look-alikes
	Bytes  int64
	Reason string
	Err    error
	Stats  Stats
	Time   time.Time
}

type Stats struct {
	Found, Known                        int64
	Saved, Dupes, Near, Skipped, Failed int64
	Bytes                               int64
	Started, Ended                      time.Time
	Err                                 error // why the source stopped early if it did
	LimitHit                            bool
	Stopped                             bool
}

type Run struct {
	Job    Job
	Events <-chan Event

	events chan Event
	ctx    context.Context
	cancel context.CancelFunc
	gate   *gate

	src   core.Source
	env   *core.Env
	idx   *index.DB
	man   *manifest
	names namer
	types map[string]bool
	tmp   string

	mu       sync.Mutex
	stats    Stats
	commitMu sync.Mutex
	saved    atomic.Int64
	limitHit atomic.Bool
}

func Start(parent context.Context, job Job, env *core.Env) (*Run, error) {
	src, ok := core.GetSource(job.Source)
	if !ok {
		return nil, fmt.Errorf("there's no source called %q", job.Source)
	}
	if err := job.normalize(); err != nil {
		return nil, err
	}

	meta := filepath.Join(job.OutDir, ".reap")
	tmp := filepath.Join(meta, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	if old, _ := filepath.Glob(filepath.Join(tmp, "*.part")); len(old) > 0 {
		for _, f := range old {
			os.Remove(f)
		}
	}

	idx, err := index.Open(filepath.Join(meta, "index.db"))
	if err != nil {
		return nil, err
	}

	man, err := openManifest(filepath.Join(meta, "manifest.jsonl"))
	if err != nil {
		idx.Close()
		return nil, err
	}

	types := map[string]bool{}
	for _, t := range job.Types {
		types[t] = true
	}

	ctx, cancel := context.WithCancel(parent)
	ch := make(chan Event, 1024)
	r := &Run{
		Job: job, Events: ch, events: ch, ctx: ctx, cancel: cancel, gate: newGate(),
		src: src, env: env, idx: idx, man: man, types: types, tmp: tmp,
		names: namer{folder: job.Folder, name: job.Name, prefix: job.Prefix},
	}
	r.stats.Started = time.Now()
	go r.loop()
	return r, nil
}

func (r *Run) Stop() {
	r.gate.Resume()
	r.cancel()
}

func (r *Run) Pause()       { r.gate.Pause() }
func (r *Run) Resume()      { r.gate.Resume() }
func (r *Run) Paused() bool { return r.gate.Paused() }

// return a snapshot of the counters
func (r *Run) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// return a source's description
func (r *Run) Info() core.Info { return r.src.Info() }

func (r *Run) count(f func(*Stats)) {
	r.mu.Lock()
	f(&r.stats)
	r.mu.Unlock()
}

func (r *Run) emit(ev Event) {
	ev.Time = time.Now()
	r.events <- ev
}

func (r *Run) loop() {
	defer close(r.events)
	ctx := r.ctx

	items := make(chan core.Item, 64)
	var wg sync.WaitGroup
	for w := 1; w <= r.Job.Workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for it := range items {
				r.gate.Wait(ctx)
				if ctx.Err() != nil {
					continue // drain
				}
				r.process(ctx, w, it)
			}
		}(w)
	}

	info := r.src.Info()
	req := core.Request{
		Target:  r.Job.Target,
		Options: core.WithDefaults(info, r.Job.Options),
		Env:     r.env,
		Status: func(format string, args ...any) {
			r.emit(Event{Kind: EvStatus, Reason: fmt.Sprintf(format, args...)})
		},
	}
	fetchErr := r.src.Fetch(ctx, req, func(it core.Item) error {
		r.gate.Wait(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if it.Source == "" {
			it.Source = info.Name
		}
		if !r.Job.Rescan && r.idx.Seen(seenKey(it)) {
			r.count(func(s *Stats) { s.Known++ })
			r.emit(Event{Kind: EvKnown, Item: it})
			return nil
		}
		r.count(func(s *Stats) { s.Found++ })
		r.emit(Event{Kind: EvFound, Item: it})
		select {
		case items <- it:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(items)
	wg.Wait()

	r.man.Close()
	r.idx.Close()

	r.mu.Lock()
	r.stats.Ended = time.Now()
	r.stats.LimitHit = r.limitHit.Load()
	if fetchErr != nil && !errors.Is(fetchErr, context.Canceled) {
		r.stats.Err = fetchErr
	}
	r.stats.Stopped = ctx.Err() != nil && !r.stats.LimitHit
	final := r.stats
	r.mu.Unlock()
	r.cancel()
	r.emit(Event{Kind: EvDone, Stats: final})
}

func seenKey(it core.Item) string { return it.Source + ":" + it.ID }

func (r *Run) markSeen(it core.Item, note string) { _ = r.idx.MarkSeen(seenKey(it), note) }

func (r *Run) atLimit() bool {
	return r.Job.Limit > 0 && r.saved.Load() >= int64(r.Job.Limit)
}

func (r *Run) skip(w int, it core.Item, reason string) {
	r.count(func(s *Stats) { s.Skipped++ })
	r.emit(Event{Kind: EvSkipped, Worker: w, Item: it, Reason: reason})
}

func (r *Run) fail(w int, it core.Item, u string, err error) {
	r.count(func(s *Stats) { s.Failed++ })
	r.emit(Event{Kind: EvFailed, Worker: w, Item: it, URL: u, Err: err, Reason: err.Error()})
}

type outcome int

const (
	outSaved  outcome = iota
	outDup            // exact copy or skipped look-alike
	outGone           // deleted at the source
	outJunk           // not a media file, retrying won't help
	outType           // a type the user didn't select; a rerun with more types may want it
	outFail           // transient failure
	outCancel         // run stopped
)

func (r *Run) process(ctx context.Context, w int, it core.Item) {
	defer func() {
		if ctx.Err() == nil {
			r.emit(Event{Kind: EvIdle, Worker: w})
		}
	}()
	if r.atLimit() {
		return
	}
	r.emit(Event{Kind: EvStart, Worker: w, Item: it, URL: it.URL})

	files, err := r.plan(ctx, it)
	if err != nil {
		switch {
		case ctx.Err() != nil:
		case errors.Is(err, core.ErrGone):
			r.skip(w, it, err.Error())
			r.markSeen(it, "gone")
		case errors.Is(err, errType), errors.Is(err, core.ErrUnsupported), errors.Is(err, core.ErrNeedsSetup):
			r.skip(w, it, err.Error())
		default:
			r.fail(w, it, it.URL, err)
		}
		return
	}

	clean := true
	for i, m := range files {
		sub := it
		if len(files) > 1 {
			sub.ID = it.ID + "_" + strconv.Itoa(i+1)
			if !r.Job.Rescan && r.idx.Seen(seenKey(sub)) {
				continue
			}
		}
		switch r.download(ctx, w, sub, m) {
		case outSaved, outDup, outGone, outJunk:
			if len(files) > 1 {
				r.markSeen(sub, "ok")
			}
		case outCancel:
			return
		default:
			clean = false
		}
	}
	if clean {
		r.markSeen(it, "ok")
	}
}

// resolves an item to the files to download, trying fallbacks in order
// and honoring the user's allowed types
func (r *Run) plan(ctx context.Context, it core.Item) ([]core.Media, error) {
	var firstErr error
	for _, raw := range append([]string{it.URL}, it.Fallbacks...) {
		if raw == "" {
			continue
		}
		ms, err := r.resolve(ctx, raw, it.Kind)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var chosen []core.Media
		var rejected []string
		for _, m := range ms {
			if pick, ok := r.choose(m); ok {
				chosen = append(chosen, pick)
			} else {
				rejected = append(rejected, m.Ext)
			}
		}
		if len(chosen) > 0 {
			return chosen, nil
		}
		if len(rejected) > 0 && (firstErr == nil || errors.Is(firstErr, core.ErrUnsupported)) {
			firstErr = core.Tag(errType, "%s isn't in your file types", strings.Join(uniq(rejected), ", "))
		}
	}
	if firstErr == nil {
		firstErr = core.Unsupported("nothing to download in this post")
	}
	return nil, firstErr
}

var directHosts = []string{"i.redd.it", "preview.redd.it", "external-preview.redd.it"}

func (r *Run) resolve(ctx context.Context, raw string, hint core.Kind) ([]core.Media, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, core.Unsupported("not a usable link: %s", raw)
	}
	if res := core.ResolverFor(u); res != nil {
		return res.Resolve(ctx, u, r.env)
	}
	ext := core.ExtFromURL(raw)
	if ext != "" || core.HostIs(u, directHosts...) {
		k := core.KindOfExt(ext)
		if k == core.KindUnknown {
			k = hint
		}
		return []core.Media{{URL: raw, Ext: ext, Kind: k}}, nil
	}
	return nil, core.Unsupported("%s isn't supported yet", strings.TrimPrefix(u.Hostname(), "www."))
}

// picks the first encoding whose types is allowed
func (r *Run) choose(m core.Media) (core.Media, bool) {
	for _, c := range append([]core.Media{m}, m.Alternates...) {
		if c.Ext == "" || r.types[c.Ext] {
			if c.Variant == "" {
				c.Variant = m.Variant
			}
			c.Alternates = nil
			return c, true
		}
	}
	return core.Media{}, false
}

func (r *Run) download(ctx context.Context, w int, it core.Item, m core.Media) outcome {
	if r.atLimit() {
		return outCancel
	}
	r.emit(Event{Kind: EvStart, Worker: w, Item: it, URL: m.URL})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		r.fail(w, it, m.URL, err)
		return outFail
	}
	for k, v := range m.Header {
		req.Header[k] = v
	}
	resp, err := r.env.HTTP.Do(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return outCancel
		}
		r.fail(w, it, m.URL, err)
		return outFail
	}
	defer resp.Body.Close()

	host := httpx.Host(m.URL)
	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone, resp.StatusCode == http.StatusForbidden:
		r.skip(w, it, fmt.Sprintf("%s no longer has it (%d)", host, resp.StatusCode))
		return outGone
	case resp.StatusCode != http.StatusOK:
		r.fail(w, it, m.URL, &httpx.StatusError{Code: resp.StatusCode, URL: m.URL})
		return outFail
	case isPlaceholder(resp):
		r.skip(w, it, host+" has removed it")
		return outGone
	}

	br := bufio.NewReaderSize(resp.Body, 64<<10)
	head, _ := br.Peek(512)
	sniffed := http.DetectContentType(head)
	ext := core.ExtFromMIME(sniffed)
	if ext == "" {
		ext = core.ExtFromMIME(resp.Header.Get("Content-Type"))
	}
	if ext == "" && !strings.HasPrefix(sniffed, "text/") {
		ext = m.Ext
	}
	if ext == "" {
		mime := sniffed
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = mime[:i]
		}
		r.skip(w, it, fmt.Sprintf("%s sent a %s page, not a file", host, mime))
		return outJunk
	}
	if !r.types[ext] {
		r.skip(w, it, ext+" isn't in your file types")
		return outType
	}

	f, err := os.CreateTemp(r.tmp, "dl-*.part")
	if err != nil {
		r.fail(w, it, m.URL, err)
		return outFail
	}
	tmp := f.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), br)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength > 0 && n != resp.ContentLength {
		err = fmt.Errorf("%s cut off the download at %d of %d bytes", host, n, resp.ContentLength)
	}
	if err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return outCancel
		}
		r.fail(w, it, m.URL, err)
		return outFail
	}
	return r.commit(w, it, m, ext, tmp, h.Sum(nil), n)
}

// @todo this is for imgur 404 but its not really working
func isPlaceholder(resp *http.Response) bool {
	p := strings.ToLower(resp.Request.URL.Path)
	return strings.HasSuffix(p, "/removed.png") || strings.HasSuffix(p, "/removed.jpg")
}

func (r *Run) commit(w int, it core.Item, m core.Media, ext, tmp string, sum []byte, n int64) outcome {
	r.commitMu.Lock()
	if r.atLimit() {
		r.commitMu.Unlock()
		os.Remove(tmp)
		return outCancel
	}
	abs := uniquePath(filepath.Join(r.Job.OutDir, r.names.rel(it, m, ext, sum)))
	rel, _ := filepath.Rel(r.Job.OutDir, abs)
	rel = filepath.ToSlash(rel)

	existing, fresh, err := r.idx.ClaimSHA(sum, rel)
	if err != nil {
		r.commitMu.Unlock()
		os.Remove(tmp)
		r.fail(w, it, m.URL, err)
		return outFail
	}
	if !fresh {
		r.commitMu.Unlock()
		os.Remove(tmp)
		r.count(func(s *Stats) { s.Dupes++ })
		r.emit(Event{Kind: EvDuplicate, Worker: w, Item: it, URL: m.URL, Other: existing, Bytes: n})
		return outDup
	}
	err = os.MkdirAll(filepath.Dir(abs), 0o755)
	if err == nil {
		err = os.Rename(tmp, abs)
	}
	if err != nil {
		_ = r.idx.ReleaseSHA(sum)
		r.commitMu.Unlock()
		os.Remove(tmp)
		r.fail(w, it, m.URL, err)
		return outFail
	}
	saved := r.saved.Add(1)
	r.commitMu.Unlock()

	var phash, nearOf string
	if r.Job.Dedupe != DedupeExact && core.KindOfExt(ext) == core.KindImage {
		if ph, ok := index.ImageHash(abs); ok {
			phash = fmt.Sprintf("%016x", ph)
			match, dist, found, err := r.idx.ClaimPHash(ph, index.MaxNear, rel, r.Job.Dedupe == DedupeFlag)
			if err == nil && found {
				r.count(func(s *Stats) { s.Near++ })
				if r.Job.Dedupe == DedupeSkip {
					os.Remove(abs)
					r.saved.Add(-1)
					r.emit(Event{Kind: EvNear, Worker: w, Item: it, URL: m.URL, Other: match, Bytes: n,
						Reason: fmt.Sprintf("looks like %s (%d bits apart), not kept", match, dist)})
					return outDup
				}
				nearOf = match
				r.emit(Event{Kind: EvNear, Worker: w, Item: it, URL: m.URL, Path: rel, Other: match, Bytes: n,
					Reason: fmt.Sprintf("kept, but looks like %s (%d bits apart)", match, dist)})
			}
		}
	}

	rec := record{
		Path: rel, SHA256: hex.EncodeToString(sum), PHash: phash, NearDupOf: nearOf, Bytes: n,
		Ext: ext, Kind: core.KindOfExt(ext).String(), Source: it.Source, Collection: it.Collection,
		ID: it.ID, PageURL: it.URL, MediaURL: m.URL, Variant: m.Variant, Title: it.Title,
		Author: it.Author, NSFW: it.NSFW, Score: it.Score, Permalink: it.Permalink, Extra: it.Extra,
		Fetched: time.Now().UTC().Format(time.RFC3339),
	}
	if !it.Created.IsZero() {
		rec.Created = it.Created.UTC().Format(time.RFC3339)
	}
	r.man.write(rec)

	r.count(func(s *Stats) { s.Saved++; s.Bytes += n })
	r.emit(Event{Kind: EvSaved, Worker: w, Item: it, URL: m.URL, Path: rel, Other: nearOf, Bytes: n})

	if r.Job.Limit > 0 && saved >= int64(r.Job.Limit) {
		r.limitHit.Store(true)
		r.cancel()
	}
	return outSaved
}

var errType = errors.New("file type not selected")

func uniq(in []string) []string {
	set := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" {
			s = "unknown"
		}
		if !set[s] {
			set[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
