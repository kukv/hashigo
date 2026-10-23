//go:build darwin

package platform

import (
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// StartToken は起動時刻（ナノ秒）の文字列を返す。この OS では起動時刻をカーネルが直接返すので、時計が動いても変わらない。
func StartToken(pid int) (string, error) {
	t, err := StartTime(pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(t.UnixNano(), 10), nil
}

// StartTime は pid のプロセスが起動した時刻を返す（sysctl kern.proc.pid）。
func StartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return time.Time{}, fmt.Errorf("pid %d のプロセスがありません", pid)
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}
