package embedded

import (
	"fmt"
	"os"
	"strings"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/backend"
)

// migrateBatch bounds how many points one transaction of the conversion
// writes, so converting a large file doesn't hold one huge transaction.
const migrateBatch = 2000

// legacyPointsKey is v0.3.0's layout: one "points" bucket per namespace,
// keyed ecosystem\0dependency\0version\0generation\0id, plus an "ids"
// identity index. Values were already in today's encoding; the layout
// took about 2.5x the space of the current one (half-empty pages, the
// index, and the repeated key prefix).
var legacyPointsKey = []byte("points")

// migrateIfNeeded converts a file in the v0.3.0 layout to the current
// one, by streaming it into a new file and swapping that in (which also
// compacts it). No vector is recomputed. The original is untouched until
// the new file is complete, so a failure or crash midway loses nothing.
// It returns the handle to use: db itself when no conversion was needed.
func migrateIfNeeded(path string, db *bolt.DB) (*bolt.DB, error) {
	legacy := false
	if err := db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, b *bolt.Bucket) error {
			legacy = legacy || b.Bucket(legacyPointsKey) != nil
			return nil
		})
	}); err != nil {
		return nil, fmt.Errorf("embedded: inspect %s: %w", path, err)
	}
	if !legacy {
		return db, nil
	}

	tmpPath := path + ".migrating"
	_ = os.Remove(tmpPath)
	tmp, err := bolt.Open(tmpPath, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("embedded: create %s: %w", tmpPath, err)
	}
	if err := copyLegacy(db, tmp); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("embedded: convert %s to the current layout: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("embedded: close %s: %w", tmpPath, err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("embedded: close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("embedded: replace %s: %w", path, err)
	}
	db, err = bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("embedded: reopen %s: %w", path, err)
	}
	return db, nil
}

// copyLegacy writes every legacy namespace (its dimension and points)
// into dst in the current layout. Legacy keys are already in generation
// order, so each generation's bucket is written in key order.
func copyLegacy(src, dst *bolt.DB) error {
	return src.View(func(stx *bolt.Tx) error {
		return stx.ForEach(func(name []byte, sb *bolt.Bucket) error {
			if err := dst.Update(func(tx *bolt.Tx) error {
				b, err := tx.CreateBucketIfNotExists(name)
				if err != nil {
					return err
				}
				if dims := sb.Get(dimsKey); dims != nil {
					if err := b.Put(dimsKey, append([]byte(nil), dims...)); err != nil {
						return err
					}
				}
				if _, err := b.CreateBucketIfNotExists(generationsKey); err != nil {
					return err
				}
				_, err = b.CreateBucketIfNotExists(chunksKey)
				return err
			}); err != nil {
				return err
			}
			points := sb.Bucket(legacyPointsKey)
			if points == nil {
				return nil
			}
			c := points.Cursor()
			k, v := c.First()
			for k != nil {
				err := dst.Update(func(tx *bolt.Tx) error {
					b := tx.Bucket(name)
					for n := 0; k != nil && n < migrateBatch; n++ {
						f := strings.Split(string(k), "\x00")
						if len(f) != 5 {
							return fmt.Errorf("unexpected legacy key %q", k)
						}
						m := backend.PointMetadata{Ecosystem: f[0], Dependency: f[1], Version: f[2], Generation: f[3]}
						chunks, err := generationBucket(b, m)
						if err != nil {
							return err
						}
						if err := chunks.Put([]byte(f[4]), append([]byte(nil), v...)); err != nil {
							return err
						}
						k, v = c.Next()
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
	})
}
