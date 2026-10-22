package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/testutil"
	"github.com/kukv/hashigo/internal/testutil/fakebin"
)

func TestMain(m *testing.M) {
	if fakebin.Enabled() {
		fakebin.Main()
	}
	os.Exit(m.Run())
}

type staticDriver struct{ specs []driver.ProcessSpec }

func (d staticDriver) Preview() []driver.ProcessSpec { return d.specs }
func (d staticDriver) Prepare(context.Context) ([]driver.ProcessSpec, error) {
	return d.specs, nil
}

func fakeUnit(id string, port int, env ...string) Unit {
	sp := driver.ProcessSpec{
		Tunnel: id,
		Path:   os.Args[0],
		Args:   []string{"-L", fmt.Sprintf("127.0.0.1:%d:db:5432", port)},
		Env:    append([]string{"HASHIGO_FAKE=1"}, env...),
		Ports:  []driver.Listen{{Addr: "127.0.0.1", Port: port}},
	}
	return Unit{ID: id, Driver: staticDriver{specs: []driver.ProcessSpec{sp}}}
}

func start(t *testing.T, s *Supervisor, units ...Unit) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, units) }()
	t.Cleanup(cancel)
	return cancel, done
}

func wait(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("Run が %s 以内に戻らない", timeout)
		return nil
	}
}

func TestStopOnCancel(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out}, fakeUnit("a", port))
	testutil.WaitPort(t, port, 5*time.Second)

	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatalf("キャンセル時は nil: %v", err)
	}
	if testutil.PortOpen(port) {
		t.Error("停止後もポートが開いている")
	}
	if s := out.String(); !strings.Contains(s, "[a] 起動しました") || !strings.Contains(s, "[a] 停止しました") {
		t.Errorf("出力 = %s", s)
	}
}

func TestEscalateToKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では Terminate が即 Kill になる")
	}
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out, StopTimeout: 300 * time.Millisecond},
		fakeUnit("a", port, "FAKE_IGNORE_TERM=1"))
	testutil.WaitPort(t, port, 5*time.Second)

	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if testutil.PortOpen(port) {
		t.Error("SIGTERM を無視するプロセスが SIGKILL で止まっていない")
	}
}

func TestOtherSlotsKeepRunning(t *testing.T) {
	var out testutil.SyncBuffer
	pa, pb := testutil.FreePort(t), testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out},
		fakeUnit("a", pa, "FAKE_EXIT_AFTER=300ms"), fakeUnit("b", pb))
	testutil.WaitPort(t, pb, 5*time.Second)
	testutil.Eventually(t, 5*time.Second, func() bool { return strings.Contains(out.String(), "[a] 終了しました") }, "a の終了が出力されない")

	if !testutil.PortOpen(pb) {
		t.Error("a が終了しても b は動き続ける")
	}
	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestAllExited(t *testing.T) {
	var out testutil.SyncBuffer
	_, done := start(t, &Supervisor{Out: &out},
		fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"),
		fakeUnit("b", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"))
	if err := wait(t, done, 5*time.Second); !errors.Is(err, ErrAllExited) {
		t.Fatalf("全スロットが終了したら ErrAllExited: %v", err)
	}
}

func TestStderrPrefixed(t *testing.T) {
	var out testutil.SyncBuffer
	cancel, done := start(t, &Supervisor{Out: &out}, fakeUnit("dev.db", testutil.FreePort(t), "FAKE_STDERR=hello"))
	testutil.Eventually(t, 5*time.Second, func() bool { return strings.Contains(out.String(), "[dev.db] hello") }, "stderr に接頭辞が付かない")
	cancel()
	wait(t, done, 5*time.Second)
}

func TestStartFailure(t *testing.T) {
	var out testutil.SyncBuffer
	u := Unit{ID: "x", Driver: staticDriver{specs: []driver.ProcessSpec{{Tunnel: "x", Path: "/nonexistent/ssh"}}}}
	_, done := start(t, &Supervisor{Out: &out}, u)
	if err := wait(t, done, 5*time.Second); !errors.Is(err, ErrAllExited) {
		t.Fatalf("起動できなければ終了扱い: %v", err)
	}
	if !strings.Contains(out.String(), "[x] 起動できませんでした") {
		t.Errorf("出力 = %s", out.String())
	}
}
