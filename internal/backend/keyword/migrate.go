package keyword

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/aleutian-ai/depctl/internal/backend"
)

// migrateBatch bounds how many points one transaction of the conversion
// writes, so converting a large file doesn't hold one huge transaction.
const migrateBatch = 10000

// legacyPointsKey is v0.3.0's layout: one "points" bucket per namespace,
// keyed ecosystem\0dependency\0version\0generation\0id, with fixed-width
// values, plus an "ids" identity index. It took about 4x the space of
// the current layout (half-empty pages, the index, and the repeated key
// prefix).
var legacyPointsKey = []byte("points")

// migrateIfNeeded converts a file in the v0.3.0 layout to the current
// one, by streaming it into a new file and swapping that in (which also
// compacts it). The original is untouched until the new file is
// complete, so a failure or crash midway loses nothing. It returns the
// handle to use: db itself when no conversion was needed.
func migrateIfNeeded(path string, db *bolt.DB) (*bolt.DB, error) {
	legacy := false
	if err := db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(_ []byte, b *bolt.Bucket) error {
			legacy = legacy || b.Bucket(legacyPointsKey) != nil
			return nil
		})
	}); err != nil {
		return nil, fmt.Errorf("keyword: inspect %s: %w", path, err)
	}
	if !legacy {
		return db, nil
	}

	tmpPath := path + ".migrating"
	_ = os.Remove(tmpPath)
	tmp, err := bolt.Open(tmpPath, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("keyword: create %s: %w", tmpPath, err)
	}
	if err := copyLegacy(db, tmp); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("keyword: convert %s to the current layout: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("keyword: close %s: %w", tmpPath, err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("keyword: close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("keyword: replace %s: %w", path, err)
	}
	db, err = bolt.Open(path, 0o600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("keyword: reopen %s: %w", path, err)
	}
	return db, nil
}

// copyLegacy writes every legacy namespace's points into dst in the
// current layout. Legacy keys are already in generation order, so each
// generation's bucket is written in key order.
func copyLegacy(src, dst *bolt.DB) error {
	return src.View(func(stx *bolt.Tx) error {
		return stx.ForEach(func(name []byte, sb *bolt.Bucket) error {
			if err := dst.Update(func(tx *bolt.Tx) error {
				bk, err := tx.CreateBucketIfNotExists(name)
				if err != nil {
					return err
				}
				if _, err := bk.CreateBucketIfNotExists(generationsKey); err != nil {
					return err
				}
				_, err = bk.CreateBucketIfNotExists(chunksKey)
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
					bk := tx.Bucket(name)
					for n := 0; k != nil && n < migrateBatch; n++ {
						f := strings.Split(string(k), "\x00")
						if len(f) != 5 {
							return fmt.Errorf("unexpected legacy key %q", k)
						}
						m, terms := decodeLegacyValue(v)
						m.Ecosystem, m.Dependency, m.Version, m.Generation = f[0], f[1], f[2], f[3]
						chunks, err := generationBucket(bk, m)
						if err != nil {
							return err
						}
						if err := chunks.Put([]byte(f[4]), encodeValue(m, terms)); err != nil {
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

// decodeLegacyValue reads a v0.3.0 value back into metadata and the term
// list it was built from (each term repeated by its count, so the
// document length and counts come out the same when re-encoded).
func decodeLegacyValue(v []byte) (backend.PointMetadata, []string) {
	n := int(binary.LittleEndian.Uint32(v))
	m := backend.PointMetadata{SourceType: string(v[4 : 4+n])}
	v = v[4+n:]
	m.Authority = int(int64(binary.LittleEndian.Uint64(v)))
	distinct := int(binary.LittleEndian.Uint32(v[12:]))
	v = v[16:]
	var terms []string
	for range distinct {
		l := int(binary.LittleEndian.Uint16(v))
		term := string(v[2 : 2+l])
		count := int(binary.LittleEndian.Uint32(v[2+l:]))
		for range count {
			terms = append(terms, term)
		}
		v = v[6+l:]
	}
	return m, terms
}
