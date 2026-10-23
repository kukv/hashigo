package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
	"github.com/kukv/hashigo/internal/testutil"
)

// upYAML は偽 ssh（テストバイナリ自身）を ssh として使う設定。
func upYAML(port int, protected bool) string {
	return fmt.Sprintf(`
defaults:
  ssh: %q
environments:
  prod:
    protected: %t
    tunnels:
      db:
        host: example
        forwards: ["%d:db:5432"]
`, os.Args[0], protected, port)
}

func startUp(t *testing.T, stdin string, tty bool, args ...string) (context.CancelFunc, <-chan int, *testutil.SyncBuffer) {
	t.Helper()
	return startUpHost(t, testHost(t), stdin, tty, args...)
}

func startUpHost(t *testing.T, h *platform.Host, stdin string, tty bool, args ...string) (context.CancelFunc, <-chan int, *testutil.SyncBuffer) {
	t.Helper()
	t.Setenv("HASHIGO_FAKE", "1")
	var stderr testutil.SyncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := Deps{Host: h, Stdin: strings.NewReader(stdin), Stdout: io.Discard, Stderr: &stderr, IsTTY: func() bool { return tty }}
	done := make(chan int, 1)
	go func() { done <- Run(ctx, d, args) }()
	return cancel, done, &stderr
}

func waitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("up が終わらない")
		return -1
	}
}

func TestUpRunsAndStops(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, false))
	cancel, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("Ctrl-C で止めたら終了コード 0: %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "[prod.db] 停止しました") {
		t.Errorf("stderr = %s", stderr.String())
	}
}

func TestUpProtected(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, true))

	_, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("TTY でなければ --yes が必要: code=%d %s", code, stderr.String())
	}

	_, done, stderr = startUp(t, "n\n", true, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "起動を取りやめました") {
		t.Fatalf("n で取りやめ: code=%d %s", code, stderr.String())
	}

	cancel, done, stderr := startUp(t, "y\n", true, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("y で起動: code=%d %s", code, stderr.String())
	}

	cancel, done, _ = startUp(t, "", false, "up", "--config", p, "--yes", "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("--yes で確認を省略: code=%d", code)
	}
}

func TestUpPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	p := writeConfig(t, upYAML(port, false))

	_, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "使用中") {
		t.Fatalf("使用中のポートがあれば起動しない: code=%d %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "起動しました") {
		t.Error("何も起動してはいけない")
	}
	if !strings.Contains(stderr.String(), "hashigo 外のプロセス") {
		t.Errorf("状態ファイルに無ければ hashigo 外のプロセス: %s", stderr.String())
	}
}

func TestUpWritesStateFile(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, false))
	h := testHost(t)
	dir, _ := h.StateDir()
	cancel, done, stderr := startUpHost(t, h, "", false, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	testutil.Eventually(t, 5*time.Second, func() bool {
		runs, _ := state.List(dir, platform.IsAlive)
		return len(runs) == 1 && runs[0].PID == os.Getpid() && len(runs[0].Slots) == 1 && runs[0].Slots[0].Tunnel == "prod.db"
	}, "状態ファイルが書かれない")
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("code=%d %s", code, stderr.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("終了後に状態ファイルが残っている: %v", entries)
	}
}

func TestUpPortOwner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	h := testHost(t)
	dir, _ := h.StateDir()
	st, err := platform.StartTime(os.Getpid())
	if err != nil {
		t.Skip("この OS ではプロセスの起動時刻を取れない")
	}
	run := state.Run{PID: os.Getpid(), StartedAt: st, Slots: []state.Slot{{Tunnel: "other.db", Ports: []string{fmt.Sprintf("127.0.0.1:%d", port)}}}}
	if err := state.New(dir, os.Getpid()).Write(run); err != nil {
		t.Fatal(err)
	}
	_, done, stderr := startUpHost(t, h, "", false, "up", "--config", writeConfig(t, upYAML(port, false)), "prod")
	if code := waitCode(t, done); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if want := fmt.Sprintf("other.db（pid %d の up）", os.Getpid()); !strings.Contains(stderr.String(), want) {
		t.Errorf("衝突相手を状態ファイルから特定する: %s", stderr.String())
	}
}

func ecsUpYAML(p1, p2 int) string {
	return fmt.Sprintf(`
defaults:
  aws: %q
tunnels:
  a: { type: ecs, cluster: c, service: s, container: app, profile: p1, forwards: ["%d:db:5432"] }
  b: { type: ecs, cluster: c, service: s, container: app, profile: p1, forwards: ["%d:db:5432"] }
groups:
  both: [a, b]
`, os.Args[0], p1, p2)
}

// ecsHost は aws の呼び出しを偽の応答にする Host。sts の呼び出しを「Interactive 引数...」の形で記録する。
func ecsHost(t *testing.T, stsErr error) (*platform.Host, func() []string) {
	h := testHost(t)
	var mu sync.Mutex
	var sts []string
	h.RunWith = func(o platform.RunOptions) platform.Runner {
		return funcRunner(func(_ string, args []string) ([]byte, error) {
			switch args[1] {
			case "get-caller-identity":
				mu.Lock()
				sts = append(sts, fmt.Sprintf("%v %s", o.Interactive, strings.Join(args, " ")))
				mu.Unlock()
				return []byte("{}"), stsErr
			case "list-tasks":
				return []byte("arn:aws:ecs:ap-northeast-1:1:task/c/t1\n"), nil
			case "describe-tasks":
				return []byte("t1-1\n"), nil
			}
			return nil, fmt.Errorf("想定外の呼び出し: %v", args)
		})
	}
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sts...)
	}
}

func TestUpECS(t *testing.T) {
	p1, p2 := testutil.FreePort(t), testutil.FreePort(t)
	h, sts := ecsHost(t, nil)
	cancel, done, stderr := startUpHost(t, h, "", false, "up", "--config", writeConfig(t, ecsUpYAML(p1, p2)), "both")
	testutil.WaitPort(t, p1, 5*time.Second)
	testutil.WaitPort(t, p2, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("code=%d %s", code, stderr.String())
	}
	got := sts()
	if len(got) != 1 || got[0] != "true sts get-caller-identity --profile p1" {
		t.Errorf("同じプロファイルの事前認証は1回だけ、端末につないで行う: %v", got)
	}
	if !strings.Contains(stderr.String(), "AWS の認証を確認しています（profile: p1）") {
		t.Errorf("stderr = %s", stderr.String())
	}
}

func TestUpECSPreauthFails(t *testing.T) {
	h, _ := ecsHost(t, errors.New("MFA の入力に失敗しました"))
	_, done, stderr := startUpHost(t, h, "", false, "up", "--config",
		writeConfig(t, ecsUpYAML(testutil.FreePort(t), testutil.FreePort(t))), "both")
	if code := waitCode(t, done); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if s := stderr.String(); !strings.Contains(s, "profile p1 の認証を確認できませんでした") || strings.Contains(s, "起動しました") {
		t.Errorf("事前認証に失敗したら何も起動しない: %s", s)
	}
}

func TestWarnExposed(t *testing.T) {
	h := testHost(t)
	h.Run = funcRunner(func(string, []string) ([]byte, error) {
		return []byte("0.0.0.0 15432\r\n172.29.16.1 16379\r\n"), nil
	})
	a := &app{d: Deps{Host: h}}
	var out bytes.Buffer
	a.warnExposed(context.Background(), &out, []builtUnit{{Ports: []int{15432, 16379}}}, 0)
	if s := out.String(); !strings.Contains(s, "ポート 15432 が 0.0.0.0 で待ち受けています") || strings.Contains(s, "16379") {
		t.Errorf("出力 = %s", s)
	}
}

func TestCleanupWindowsAfterUp(t *testing.T) {
	var killed []string
	h := wslHost(t, nil)
	h.Run = funcRunner(func(name string, args []string) ([]byte, error) {
		switch name {
		case "powershell.exe":
			return []byte("100\tssh.exe\tssh.exe -L 127.0.0.1:15432:db:5432 -- x\r\n200\tssh.exe\tssh.exe -L 127.0.0.1:9:db:5432 -- y\r\n"), nil
		case "taskkill.exe":
			killed = append(killed, args[2])
			return nil, nil
		}
		return nil, fmt.Errorf("想定外: %s", name)
	})
	a := &app{d: Deps{Host: h}}
	var out bytes.Buffer
	a.cleanupWindows(&out, []builtUnit{
		{Bin: "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe", Ports: []int{15432}},
		{Bin: "/usr/bin/ssh", Ports: []int{9}},
	})
	if len(killed) != 1 || killed[0] != "100" {
		t.Errorf(".exe を使ったトンネルのポートだけを片付ける: %v", killed)
	}
	if !strings.Contains(out.String(), "Windows 側に残っていた ssh.exe (pid 100) を終了しました") {
		t.Errorf("出力 = %s", out.String())
	}

	plain := testHost(t)
	plain.Run = funcRunner(func(name string, _ []string) ([]byte, error) {
		t.Errorf("WSL でなければ何も実行しない: %s", name)
		return nil, nil
	})
	(&app{d: Deps{Host: plain}}).cleanupWindows(&out, []builtUnit{{Bin: "/usr/bin/ssh", Ports: []int{1}}})
}
