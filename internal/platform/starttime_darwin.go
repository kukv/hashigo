//go:build darwin

package platform

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

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
