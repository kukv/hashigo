//go:build windows

package platform

import (
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive は GetExitCodeProcess が「まだ動いている」ときに返す値（STILL_ACTIVE）。
const stillActive = 259

// StartToken は起動時刻（ナノ秒）の文字列を返す。この OS では起動時刻をカーネルが直接返すので、時計が動いても変わらない。
func StartToken(pid int) (string, error) {
	t, err := StartTime(pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(t.UnixNano(), 10), nil
}

// StartTime は pid のプロセスが起動した時刻を返す（OpenProcess と GetProcessTimes）。
func StartTime(pid int) (time.Time, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // pid は OS が返す 32 ビット以内の値なので切り詰めは起きない
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return time.Time{}, fmt.Errorf("pid %d は終了しています", pid)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()), nil
}
