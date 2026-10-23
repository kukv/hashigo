package platform

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseStatStartTicks(t *testing.T) {
	// comm に空白と ) を含む場合も、最後の ) の後ろから数える
	stat := "1234 (my (weird) cmd) S 1 1234 1234 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000 100 18446744073709551615"
	got, err := parseStatStartTicks([]byte(stat))
	if err != nil || got != 98765 {
		t.Fatalf("parseStatStartTicks = %d, %v", got, err)
	}
	if _, err := parseStatStartTicks([]byte("1234 (x) S 1")); err == nil {
		t.Error("項目が足りなければエラー")
	}
	if _, err := parseStatStartTicks([]byte("no paren")); err == nil {
		t.Error(") が無ければエラー")
	}
}

func TestParseBootTime(t *testing.T) {
	got, err := parseBootTime([]byte("cpu  1 2 3\nbtime 1700000000\nprocesses 10\n"))
	if err != nil || got != 1700000000 {
		t.Fatalf("parseBootTime = %d, %v", got, err)
	}
	if _, err := parseBootTime([]byte("cpu 1\n")); err == nil {
		t.Error("btime が無ければエラー")
	}
}

func TestStartTimeSelf(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		t.Skip("未対応の OS")
	}
	st, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if st.After(time.Now().Add(time.Second)) || st.Before(time.Now().Add(-24*time.Hour)) {
		t.Errorf("自分の起動時刻が不自然: %s", st)
	}
	again, _ := StartTime(os.Getpid())
	if !again.Equal(st) {
		t.Errorf("同じプロセスの起動時刻は毎回同じ: %s と %s", st, again)
	}
	if !IsAlive(os.Getpid(), st) {
		t.Error("自分は生きている")
	}
	if IsAlive(os.Getpid(), st.Add(-time.Hour)) {
		t.Error("起動時刻が違えば pid の再利用とみなす")
	}
}
