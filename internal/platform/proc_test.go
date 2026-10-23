package platform

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain は、テストバイナリ自身を「眠り続けるだけの子プロセス」として使えるようにする。
func TestMain(m *testing.M) {
	if os.Getenv("HASHIGO_PLATFORM_SLEEP") == "1" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "HASHIGO_PLATFORM_SLEEP=1")
	return cmd
}

func waitExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("子プロセスが終了しない")
	}
}

func TestStartChildTerminate(t *testing.T) {
	cmd := sleeper(t)
	c, err := StartChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := StartTime(c.Process.Pid)
	if err != nil {
		t.Fatalf("子の起動時刻: %v", err)
	}
	if err := c.Terminate(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, cmd)
	if IsAlive(c.Process.Pid, st) {
		t.Error("終了したプロセスは生きていない")
	}
}

func TestStartChildKill(t *testing.T) {
	cmd := sleeper(t)
	c, err := StartChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, cmd)
}

func TestStartChildError(t *testing.T) {
	if _, err := StartChild(exec.Command("/nonexistent/hashigo-test")); err == nil {
		t.Error("存在しないバイナリはエラー")
	}
}
