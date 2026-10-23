package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/state"
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
func (d staticDriver) Classify(error, []byte) driver.Exit { return driver.Exit{} }

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

func wait(t *testing.T, done <-chan error) error {
	const timeout = 5 * time.Second
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
	if err := wait(t, done); err != nil {
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
	if err := wait(t, done); err != nil {
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
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestAllExited(t *testing.T) {
	var out testutil.SyncBuffer
	_, done := start(t, &Supervisor{Out: &out},
		fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"),
		fakeUnit("b", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"))
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatalf("全スロットが終了したら ErrAllExited: %v", err)
	}
}

func TestStderrPrefixed(t *testing.T) {
	var out testutil.SyncBuffer
	cancel, done := start(t, &Supervisor{Out: &out}, fakeUnit("dev.db", testutil.FreePort(t), "FAKE_STDERR=hello"))
	testutil.Eventually(t, 5*time.Second, func() bool { return strings.Contains(out.String(), "[dev.db] hello") }, "stderr に接頭辞が付かない")
	cancel()
	_ = wait(t, done)
}

func TestStartFailure(t *testing.T) {
	var out testutil.SyncBuffer
	u := Unit{ID: "x", Driver: staticDriver{specs: []driver.ProcessSpec{{Tunnel: "x", Path: "/nonexistent/ssh"}}}}
	s := &Supervisor{Out: &out}
	_, done := start(t, s, u)
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatalf("起動できなければ終了扱い: %v", err)
	}
	if !strings.Contains(out.String(), "[x] 起動できませんでした") {
		t.Errorf("出力 = %s", out.String())
	}
	if got := s.Slots()[0].State; got != state.Failed {
		t.Errorf("起動できなければ failed: %s", got)
	}
}

// fakeClock は After をすぐ発火させ、待った時間を記録する。Now は呼ぶたびに step だけ進む。
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	step  time.Duration
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// stuckClock の After は発火しない。backoff 中の停止を確かめるのに使う。
type stuckClock struct{}

func (stuckClock) Now() time.Time                       { return time.Now() }
func (stuckClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// firstState は最初のスロットの状態を返す。Run がスロットを作る前は ""。
func firstState(s *Supervisor) string {
	ss := s.Slots()
	if len(ss) == 0 {
		return ""
	}
	return ss[0].State
}

func reconnecting(u Unit) Unit {
	u.Reconnect = true
	return u
}

func TestBackoffDoubles(t *testing.T) {
	var out testutil.SyncBuffer
	clk := &fakeClock{}
	cancel, done := start(t, &Supervisor{Out: &out, Clock: clk},
		reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 10*time.Second, func() bool { return len(clk.Waits()) >= 8 }, "再接続が 8 回起きない")
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := clk.Waits()[i]; got != w*time.Second {
			t.Errorf("%d 回目の待ち = %s, want %s", i+1, got, w*time.Second)
		}
	}
	if s := out.String(); !strings.Contains(s, "[a] 1s 後に再接続します") || !strings.Contains(s, "[a] 60s 後に再接続します") {
		t.Errorf("出力 = %s", s)
	}
}

func TestBackoffResetsAfterStableRun(t *testing.T) {
	clk := &fakeClock{step: 61 * time.Second} // 毎回 60 秒以上動いたことにする
	cancel, done := start(t, &Supervisor{Out: io.Discard, Clock: clk},
		reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 10*time.Second, func() bool { return len(clk.Waits()) >= 3 }, "再接続が 3 回起きない")
	cancel()
	_ = wait(t, done)
	for i, got := range clk.Waits()[:3] {
		if got != time.Second {
			t.Errorf("%d 回目の待ち = %s, 安定して動いたあとは 1s に戻す", i+1, got)
		}
	}
}

// classifyDriver は stderr に "token expired" があれば Fatal にする。
type classifyDriver struct{ staticDriver }

func (classifyDriver) Classify(_ error, tail []byte) driver.Exit {
	if bytes.Contains(tail, []byte("token expired")) {
		return driver.Exit{Fatal: true, Message: "sso login してください"}
	}
	return driver.Exit{}
}

func TestFatalStops(t *testing.T) {
	var out testutil.SyncBuffer
	u := fakeUnit("a", testutil.FreePort(t), "FAKE_STDERR=token expired", "FAKE_EXIT_AFTER=50ms")
	u.Driver = classifyDriver{u.Driver.(staticDriver)}
	s := &Supervisor{Out: &out, Clock: &fakeClock{}}
	_, done := start(t, s, reconnecting(u))
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatalf("Fatal なら再接続せずに終わる: %v", err)
	}
	if !strings.Contains(out.String(), "[a] failed: sso login してください") {
		t.Errorf("出力 = %s", out.String())
	}
	if got := s.Slots()[0]; got.State != state.Failed || got.LastError != "sso login してください" {
		t.Errorf("状態 = %+v", got)
	}
}

// flakyDriver は最初の fails 回だけ Prepare に失敗する。
type flakyDriver struct {
	staticDriver
	mu    sync.Mutex
	fails int
}

func (d *flakyDriver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fails > 0 {
		d.fails--
		return nil, errors.New("RUNNING のタスクがありません")
	}
	return d.staticDriver.Prepare(ctx)
}

func TestPrepareFailureRetries(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	u := fakeUnit("a", port)
	u.Driver = &flakyDriver{staticDriver: u.Driver.(staticDriver), fails: 2}
	clk := &fakeClock{}
	cancel, done := start(t, &Supervisor{Out: &out, Clock: clk}, reconnecting(u))
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if got := clk.Waits(); len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Errorf("待ち = %v", got)
	}
	if !strings.Contains(out.String(), "[a] 準備に失敗しました: RUNNING のタスクがありません") {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestNoReconnectFails(t *testing.T) {
	var out testutil.SyncBuffer
	s := &Supervisor{Out: &out}
	_, done := start(t, s, fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=50ms"))
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatal(err)
	}
	if got := s.Slots()[0]; got.State != state.Failed || !strings.Contains(got.LastError, "reconnect: false") {
		t.Errorf("状態 = %+v", got)
	}
}

func TestStopDuringBackoff(t *testing.T) {
	var out testutil.SyncBuffer
	s := &Supervisor{Out: &out, Clock: stuckClock{}}
	cancel, done := start(t, s, reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Backoff }, "backoff にならない")
	if got := s.Slots()[0]; got.NextRetryAt == nil || got.ChildPID != 0 {
		t.Errorf("backoff 中の状態 = %+v", got)
	}
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if got := s.Slots()[0].State; got != state.Stopped {
		t.Errorf("状態 = %s", got)
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	lw := &lineWriter{out: &syncWriter{w: io.Discard}}
	_, _ = lw.Write(bytes.Repeat([]byte("x"), 10000))
	_, _ = lw.Write([]byte("end\n"))
	tail := lw.tail()
	if len(tail) != tailSize || !bytes.HasSuffix(tail, []byte("end\n")) {
		t.Errorf("末尾 %d バイトだけ残す: len=%d", tailSize, len(tail))
	}
}

func TestHealthUp(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	s := &Supervisor{Out: &out, HealthInterval: 100 * time.Millisecond, HealthTimeout: 200 * time.Millisecond}
	cancel, done := start(t, s, fakeUnit("a", port))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Up }, "up にならない")
	if got := s.Slots()[0]; !got.Healthy || got.CheckedAt == nil || got.ChildPID == 0 {
		t.Errorf("up の状態 = %+v", got)
	}
	cancel()
	_ = wait(t, done)
	if !strings.Contains(out.String(), "[a] 接続を確認しました") {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestHealthUnhealthyDoesNotKill(t *testing.T) {
	var out testutil.SyncBuffer
	port, closed := testutil.FreePort(t), testutil.FreePort(t)
	u := fakeUnit("a", port)
	sd := u.Driver.(staticDriver)
	sd.specs[0].Ports = append(sd.specs[0].Ports, driver.Listen{Addr: "127.0.0.1", Port: closed})
	u.Driver = sd
	s := &Supervisor{Out: &out, HealthInterval: 100 * time.Millisecond, HealthTimeout: 200 * time.Millisecond}
	cancel, done := start(t, s, u)
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Unhealthy }, "unhealthy にならない")
	if !testutil.PortOpen(port) {
		t.Error("unhealthy でもプロセスは kill しない")
	}
	cancel()
	_ = wait(t, done)
	if want := fmt.Sprintf("[a] unhealthy: 127.0.0.1:%d に接続できません", closed); !strings.Contains(out.String(), want) {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestStateFileLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	port := testutil.FreePort(t)
	f := state.New(dir, 4321)
	base := state.Run{PID: 4321, StartedAt: time.Now(), Config: "/x/hashigo.yaml"}
	s := &Supervisor{Out: io.Discard, HealthInterval: 100 * time.Millisecond, State: f, Base: base}
	u := fakeUnit("dev.db", port)
	u.Env, u.Protected = "dev", true
	cancel, done := start(t, s, u)
	alive := func(state.Run) bool { return true }
	testutil.Eventually(t, 5*time.Second, func() bool {
		runs, _ := state.List(dir, alive)
		return len(runs) == 1 && len(runs[0].Slots) == 1 && runs[0].Slots[0].State == state.Up
	}, "状態ファイルに up が書かれない")
	runs, _ := state.List(dir, alive)
	got := runs[0]
	if got.PID != 4321 || got.Config != "/x/hashigo.yaml" || got.Slots[0].Env != "dev" || !got.Slots[0].Protected {
		t.Errorf("状態ファイル = %+v", got)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", port); got.Slots[0].Ports[0] != want {
		t.Errorf("ports = %v", got.Slots[0].Ports)
	}
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Error("終了したら状態ファイルを削除する")
	}
}

func TestStateFileWriteErrorWarnsOnce(t *testing.T) {
	var out testutil.SyncBuffer
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// ファイルの下にディレクトリは作れないので、書き込みは必ず失敗する
	s := &Supervisor{Out: &out, HealthInterval: 50 * time.Millisecond, State: state.New(filepath.Join(blocker, "run"), 1)}
	cancel, done := start(t, s, fakeUnit("a", testutil.FreePort(t)))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Up }, "up にならない")
	cancel()
	_ = wait(t, done)
	if n := strings.Count(out.String(), "状態ファイルを書けません"); n != 1 {
		t.Errorf("警告は1回だけ: %d 回\n%s", n, out.String())
	}
}
