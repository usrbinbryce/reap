package index

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "nested", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSeen(t *testing.T) {
	db := open(t)
	if db.Seen("reddit:a") {
		t.Fatal("fresh db has nothing seen")
	}
	if err := db.MarkSeen("reddit:a", "ok"); err != nil {
		t.Fatal(err)
	}
	if !db.Seen("reddit:a") || db.Seen("reddit:b") {
		t.Error("Seen after MarkSeen")
	}
	if s, _ := db.Counts(); s != 1 {
		t.Errorf("seen count = %d", s)
	}
}

func TestClaimSHA(t *testing.T) {
	db := open(t)
	sum := sha256.Sum256([]byte("hello"))

	if ex, fresh, err := db.ClaimSHA(sum[:], "a.jpg"); err != nil || !fresh || ex != "" {
		t.Fatalf("first claim: %q %v %v", ex, fresh, err)
	}
	if ex, fresh, err := db.ClaimSHA(sum[:], "b.jpg"); err != nil || fresh || ex != "a.jpg" {
		t.Fatalf("second claim: %q %v %v", ex, fresh, err)
	}
	if _, files := db.Counts(); files != 1 {
		t.Errorf("files = %d", files)
	}
	if err := db.ReleaseSHA(sum[:]); err != nil {
		t.Fatal(err)
	}
	if _, fresh, _ := db.ClaimSHA(sum[:], "c.jpg"); !fresh {
		t.Error("released hash should be claimable again")
	}
}

func TestClaimSHAIsAtomicAcrossGoroutines(t *testing.T) {
	db := open(t)
	sum := sha256.Sum256([]byte("race"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, fresh, err := db.ClaimSHA(sum[:], "x"); err == nil && fresh {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Errorf("%d goroutines claimed the same hash, want 1", winners)
	}
}

// flip returns h with n distinct bits flipped, spread across all four bands.
func flip(h uint64, n int) uint64 {
	positions := []uint{1, 17, 33, 49, 5, 21, 37, 53}
	for i := 0; i < n; i++ {
		h ^= 1 << positions[i]
	}
	return h
}

func TestClaimPHashFindsEveryDistanceUpToMax(t *testing.T) {
	const base uint64 = 0xA5A5_5A5A_F00F_0FF0
	for d := 0; d <= MaxNear; d++ {
		db := open(t)
		if _, _, found, err := db.ClaimPHash(base, MaxNear, "orig.jpg", true); err != nil || found {
			t.Fatalf("empty db found a match: %v", err)
		}
		probe := flip(base, d)
		if got := bits.OnesCount64(probe ^ base); got != d {
			t.Fatalf("test setup: distance %d, want %d", got, d)
		}
		match, dist, found, err := db.ClaimPHash(probe, MaxNear, "new.jpg", true)
		if err != nil || !found || match != "orig.jpg" || dist != d {
			t.Errorf("distance %d: match=%q dist=%d found=%v err=%v", d, match, dist, found, err)
		}
	}
}

func TestClaimPHashIgnoresFarHashes(t *testing.T) {
	db := open(t)
	const base uint64 = 0x0123_4567_89AB_CDEF
	db.ClaimPHash(base, MaxNear, "a", true)
	if _, _, found, _ := db.ClaimPHash(flip(base, 8), MaxNear, "b", true); found {
		t.Error("8 bits apart should not match")
	}
	if _, _, found, _ := db.ClaimPHash(flip(base, 2), 1, "c", true); found {
		t.Error("a tighter maxDist should exclude a 2-bit match")
	}
	// maxDist above MaxNear is clamped rather than giving false confidence.
	db2 := open(t)
	db2.ClaimPHash(base, MaxNear, "a", true)
	if _, dist, found, _ := db2.ClaimPHash(flip(base, 3), 60, "d", true); !found || dist != 3 {
		t.Errorf("clamped lookup: found=%v dist=%d", found, dist)
	}
}

func TestClaimPHashPrefersClosest(t *testing.T) {
	db := open(t)
	const base uint64 = 0xFFFF_0000_FFFF_0000
	db.ClaimPHash(flip(base, 3), MaxNear, "far", true)
	db.ClaimPHash(flip(base, 1), MaxNear, "near", true)
	match, dist, found, _ := db.ClaimPHash(base, MaxNear, "probe", true)
	if !found || match != "near" || dist != 1 {
		t.Errorf("match=%q dist=%d", match, dist)
	}
}

func TestClaimPHashKeepOnMatchFalseDoesNotStore(t *testing.T) {
	db := open(t)
	const a uint64 = 0x1111_2222_3333_4444
	db.ClaimPHash(a, MaxNear, "a", true)
	if _, _, found, _ := db.ClaimPHash(flip(a, 1), MaxNear, "skipped", false); !found {
		t.Fatal("expected a match")
	}
	// If the skipped hash had been stored, an exact (distance 0) probe would find it.
	if match, _, found, _ := db.ClaimPHash(flip(a, 1), 0, "probe", false); found {
		t.Errorf("skipped hash was stored as %q", match)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	db.MarkSeen("k", "ok")
	sum := sha256.Sum256([]byte("x"))
	db.ClaimSHA(sum[:], "x.png")
	db.Close()

	db2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if !db2.Seen("k") {
		t.Error("seen entry lost on reopen")
	}
	if ex, fresh, _ := db2.ClaimSHA(sum[:], "y.png"); fresh || ex != "x.png" {
		t.Error("sha entry lost on reopen")
	}
	if db2.Path != p {
		t.Errorf("Path = %q", db2.Path)
	}
}

func TestOpenWhileLockedFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = Open(p)
	if err == nil || !strings.Contains(err.Error(), "another reap run") {
		t.Errorf("second Open: %v", err)
	}
}

func TestOpenBadPaths(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, []byte("x"), 0o644)
	if _, err := Open(filepath.Join(blocker, "sub", "index.db")); err == nil {
		t.Error("a file in place of the directory should fail")
	}
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, bytes.Repeat([]byte("not bolt"), 1000), 0o644)
	if _, err := Open(junk); err == nil {
		t.Error("a non-bolt file should fail")
	}
}

// ---- perceptual hash ----

func pattern(seed int64, w, h int) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	var cells [8][8]uint8
	for y := range cells {
		for x := range cells[y] {
			cells[y][x] = uint8(rng.Intn(256))
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := cells[y*8/h][x*8/w]
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func save(t *testing.T, name string, img image.Image, asJPEG bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if asJPEG {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 60})
	} else {
		err = png.Encode(f, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDHashSurvivesResizeAndRecompression(t *testing.T) {
	orig, ok := ImageHash(save(t, "a.png", pattern(1, 256, 192), false))
	if !ok {
		t.Fatal("hash failed")
	}
	small, ok := ImageHash(save(t, "b.jpg", pattern(1, 120, 90), true))
	if !ok {
		t.Fatal("hash failed")
	}
	if d := bits.OnesCount64(orig ^ small); d > MaxNear {
		t.Errorf("resized jpeg is %d bits from the original, want <= %d", d, MaxNear)
	}
	other, _ := ImageHash(save(t, "c.png", pattern(2, 256, 192), false))
	if d := bits.OnesCount64(orig ^ other); d <= MaxNear*3 {
		t.Errorf("different images only %d bits apart", d)
	}
}

func TestDHashOffsetBoundsAndEmpty(t *testing.T) {
	img := pattern(3, 90, 80)
	sub := img.SubImage(image.Rect(10, 10, 90, 80))
	if DHash(sub) == 0 {
		t.Error("sub-image hash should be nonzero")
	}
	if DHash(image.NewRGBA(image.Rect(0, 0, 0, 0))) != 0 {
		t.Error("empty image hashes to 0")
	}
}

func TestImageHashRejects(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{}

	cases["missing"] = filepath.Join(dir, "nope.png")

	notImage := filepath.Join(dir, "x.png")
	os.WriteFile(notImage, []byte("hello"), 0o644)
	cases["not an image"] = notImage

	cases["tiny"] = save(t, "tiny.png", pattern(4, 8, 8), false)

	flat := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	cases["flat"] = save(t, "flat.png", flat, false)

	// A valid header followed by garbage: DecodeConfig passes, Decode fails.
	var buf bytes.Buffer
	png.Encode(&buf, pattern(5, 64, 64))
	trunc := filepath.Join(dir, "trunc.png")
	os.WriteFile(trunc, buf.Bytes()[:60], 0o644)
	cases["truncated"] = trunc

	for name, p := range cases {
		if _, ok := ImageHash(p); ok {
			t.Errorf("%s: ImageHash should reject", name)
		}
	}
}
