//go:build !unix

package cli

import "io/fs"

func allocatedBytes(info fs.FileInfo) int64 {
	return info.Size()
}
