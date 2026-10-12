package index

import (
	"bufio"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" //
	_ "image/png"  //
	"io"
	"os"

	_ "golang.org/x/image/webp"
)

// no decompression bombs please!
const maxPixels = 80_000_000

// Decodes the image at path and returns its difference hash. ok is false
// for files that can't be decoded, are tiny, or are flat (flat images
// hash to all zeroes and would "match" every other flat image)
func ImageHash(path string) (h uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil || cfg.Width < 16 || cfg.Height < 16 || cfg.Width*cfg.Height > maxPixels {
		return 0, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, false
	}
	img, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return 0, false
	}
	h = DHash(img)
	if h == 0 || h == ^uint64(0) {
		return 0, false
	}
	return h, true
}

// DHash is a 64-bit difference hash: shrink to 9x8 grayscale and record
// whether each pixel is darker than its right neighbor. It survives resizing
// and recompression, which is what reposts usually go through
//
// Instead of resampling every pixel, each cell averages a 6x6 grid of samples.
// That keeps hashing a 12 MP photo cheap and the result is stable enough for
// near duplicate matching!
func DHash(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return 0
	}
	const s = 6
	var grid [8][9]float64
	for cy := 0; cy < 8; cy++ {
		for cx := 0; cx < 9; cx++ {
			var sum float64
			for sy := 0; sy < s; sy++ {
				y := b.Min.Y + ((cy*s+sy)*2+1)*h/(8*s*2)
				for sx := 0; sx < s; sx++ {
					x := b.Min.X + ((cx*s+sx)*2+1)*w/(9*s*2)
					r, g, bl, _ := img.At(x, y).RGBA()
					sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
				}
			}
			grid[cy][cx] = sum
		}
	}
	var hash uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			hash <<= 1
			if grid[y][x] < grid[y][x+1] {
				hash |= 1
			}
		}
	}
	return hash
}
