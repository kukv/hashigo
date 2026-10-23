package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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
	t.Setenv("HASHIGO_FAKE", "1")
	var stderr testutil.SyncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := Deps{Host: testHost(), Stdin: strings.NewReader(stdin), Stdout: io.Discard, Stderr: &stderr, IsTTY: func() bool { return tty }}
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
}
