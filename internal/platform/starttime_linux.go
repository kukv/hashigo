//go:build linux

package platform

import (
	"os"
	"strconv"
	"time"
)

// clockTicks は /proc の時刻の単位（USER_HZ）。Linux では事実上 100 に固定されている。
const clockTicks = 100

// StartToken は "<boot_id>:<起動からの tick 数>" を返す。壁時計が動いても変わらない。
func StartToken(pid int) (string, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	ticks, err := parseStatStartTicks(stat)
	if err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return formatStartToken(string(boot), ticks), nil
}

// StartTime は pid のプロセスが起動した時刻を返す。
func StartTime(pid int) (time.Time, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	ticks, err := parseStatStartTicks(stat)
	if err != nil {
		return time.Time{}, err
	}
	sys, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	btime, err := parseBootTime(sys)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(btime, 0).Add(time.Duration(ticks) * (time.Second / clockTicks)), nil
}
