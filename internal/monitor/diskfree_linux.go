//go:build linux

package monitor

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// ensureLocalRoom refuses a download that clearly cannot fit, before we spend
// minutes pulling gigabytes across an SSH channel. The 5% margin covers the
// filesystem overhead of a file this size.
//
// This matters more on Linux than the !windows stub implied: log collection
// routinely pulls multi-gigabyte archives, and a full disk part-way through
// wastes the whole transfer.
func ensureLocalRoom(localPath string, need int64) error {
	dir := filepath.Dir(localPath)
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil // odd mount or a path that does not exist yet: let the write decide
	}
	// Bavail, not Bfree: the reserved-blocks pool is not ours to spend.
	avail := int64(st.Bavail) * int64(st.Bsize)
	want := need + need/20
	if avail < want {
		return fmt.Errorf("저장 위치의 여유 공간이 부족합니다: %s 필요, %s 사용 가능 (%s)",
			humanBytes(want), humanBytes(avail), dir)
	}
	return nil
}
