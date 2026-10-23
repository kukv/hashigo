package platform

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// IsAlive は pid のプロセスが生きていて、その起動時刻が startedAt と 1 秒以内で一致するかを返す。
// 一致しなければ、pid が別のプロセスに再利用されたとみなす。
// startedAt は同じ StartTime で取った値を渡す。
func IsAlive(pid int, startedAt time.Time) bool {
	st, err := StartTime(pid)
	if err != nil {
		return false
	}
	d := st.Sub(startedAt)
	return d > -time.Second && d < time.Second
}

// IsAliveToken は pid のプロセスが生きていて、StartToken が token と一致するかを返す。
// Linux では壁時計に依存しないので、時刻合わせがあっても生きているプロセスを残骸と誤らない。
func IsAliveToken(pid int, token string) bool {
	cur, err := StartToken(pid)
	return err == nil && cur == token
}

// formatStartToken は Linux 用の起動識別子を組み立てる。boot_id と、起動からの tick 数（壁時計が動いても変わらない）。
func formatStartToken(bootID string, ticks int64) string {
	return strings.TrimSpace(bootID) + ":" + strconv.FormatInt(ticks, 10)
}

// parseStatStartTicks は /proc/<pid>/stat から起動時刻（起動からの tick 数、22 番目の項目）を取り出す。
// 2 番目の項目（comm）は空白や ) を含みうるので、最後の ) より後ろを数える。
func parseStatStartTicks(stat []byte) (int64, error) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("/proc/<pid>/stat の形式が想定と違います")
	}
	// ) の後ろの最初の項目が 3 番目（state）なので、22 番目は添字 19
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return 0, errors.New("/proc/<pid>/stat の項目が足りません")
	}
	return strconv.ParseInt(fields[19], 10, 64)
}

// parseBootTime は /proc/stat の btime（起動した時刻の UNIX 秒）を取り出す。
func parseBootTime(stat []byte) (int64, error) {
	for _, line := range strings.Split(string(stat), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, errors.New("/proc/stat に btime がありません")
}
