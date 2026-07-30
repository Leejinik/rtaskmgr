//go:build windows

package monitor

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ensureLocalRoom refuses a download that clearly cannot fit, before we spend
// minutes pulling gigabytes across an SSH channel. The 5% margin covers the
// filesystem overhead of a file this size.
func ensureLocalRoom(localPath string, need int64) error {
	dir := filepath.Dir(localPath)
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil // unusual path: let the write itself decide
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &total, &totalFree); err != nil {
		return nil // network drive or an odd volume: don't block on a failed probe
	}
	want := need + need/20
	if freeAvail < uint64(want) {
		return fmt.Errorf("저장 위치의 여유 공간이 부족합니다: %s 필요, %s 사용 가능 (%s)",
			humanBytes(want), humanBytes(int64(freeAvail)), dir)
	}
	return nil
}
