// The index package is the dataset-wide memory of what has been downloaded. It
// lives in <dataset>/.reap/index.db and is shared by every run that writes into that
// folder. This allows an essentially completely unique dataset if the user desires.
//
// seen				sourced:id -> note 		skip posts we've handled, before any request
// sha256 		sum				 -> path 		exact duplicate detection
// phash0-3 	band|hash  -> path		look-alike detection (ClaimPHash)
package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketSeen  = []byte("seen")
	bucketSHA   = []byte("sha256")
	bucketBands = [4][]byte{[]byte("phash0"), []byte("phash1"), []byte("phash2"), []byte("phash3")}
)

// largest Hamming distance a look-alike lookup is guaranteed
// to find. With four 16-bit bands, two hashes at most 3 bits
// apart must agree exaclty on at least one band (pigeonhole),
// so we only capture against hashes sharing a band instead of
// scanning everything. source: https://en.wikipedia.org/wiki/Hamming_distance
const MaxNear = 3

type DB struct {
	db   *bolt.DB
	Path string
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w (is another reap run using this folder?)", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		names := append([][]byte{bucketSeen, bucketSHA}, bucketBands[:]...)
		for _, n := range names {
			if _, err := tx.CreateBucketIfNotExists(n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db, Path: path}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// reports whether a key was marked handled by an earlier run
func (d *DB) Seen(key string) bool {
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bucketSeen).Get([]byte(key)) != nil
		return nil
	})
	return found
}

func (d *DB) MarkSeen(key, note string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSeen).Put([]byte(key), []byte(note))
	})
}

// records that the file with this content lives at path. If the hash is
// already known, nothing is written and the existing path is retunred with
// fresh=false. The check and write happen in one transaction, so two workers
// can't both claim the same content
func (d *DB) ClaimSHA(sum []byte, path string) (existing string, fresh bool, err error) {
	err = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSHA)
		if v := b.Get(sum); v != nil {
			existing = string(v)
			return nil
		}
		fresh = true
		return b.Put(sum, []byte(path))
	})

	return existing, fresh, err
}

// forgets an SHA claim, for when saving the file failed after claiming
func (d *DB) ReleaseSHA(sum []byte) error {
	return d.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketSHA).Delete(sum) })
}

func (d *DB) ClaimPHash(h uint64, maxDist int, path string, keepOnMatch bool) (match string, dist int, found bool, err error) {
	if maxDist > MaxNear {
		maxDist = MaxNear
	}
	var full [8]byte
	binary.BigEndian.PutUint64(full[:], h)

	err = d.db.Update(func(tx *bolt.Tx) error {
		best := maxDist + 1
		for i := 0; i < 4; i++ {
			prefix := full[i*2 : i*2+2]
			c := tx.Bucket(bucketBands[i]).Cursor()
			for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
				if len(k) != 10 {
					continue
				}
				other := binary.BigEndian.Uint64(k[2:])
				if dd := bits.OnesCount64(other ^ h); dd < best {
					best, match, found = dd, string(v), true
				}
			}
		}
		if found {
			dist = best
			if !keepOnMatch {
				return nil
			}
		}
		for i := 0; i < 4; i++ {
			key := make([]byte, 10)
			copy(key, full[i*2:i*2+2])
			copy(key[2:], full[:])
			if err := tx.Bucket(bucketBands[i]).Put(key, []byte(path)); err != nil {
				return err
			}
		}
		return nil
	})
	return match, dist, found, err
}

// returns how many items are marked seen and how many files are indexed
func (d *DB) Counts() (seen, files int) {
	_ = d.db.View(func(tx *bolt.Tx) error {
		seen = tx.Bucket(bucketSeen).Stats().KeyN
		files = tx.Bucket(bucketSHA).Stats().KeyN
		return nil
	})
	return seen, files
}
