//go:build unix

package cli

import (
	"io/fs"
	"syscall"
)

// allocatedBytes reports the disk space actually allocated to a file, not
// its apparent size: Badger preallocates a sparse 2 GB value log while
// open, so apparent size overstates a near-empty store by gigabytes.
func allocatedBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
