// Package supervisor は子プロセスを起動・監視し、終了したら張り直す。
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

// ErrAllExited は、停止の指示が無いまま全スロットが failed になったことを表す。
var ErrAllExited = errors.New("すべてのトンネルが終了しました")

const (
	minBackoff  = time.Second
	maxBackoff  = 60 * time.Second
	stableAfter = 60 * time.Second // これだけ動いたら、次のバックオフを 1s に戻す
	tailSize    = 4096             // Classify に渡す stderr の末尾
)

// Unit は監視する1トンネル。
type Unit struct {
	ID        string
	Env       string // 状態ファイルと status の表示用
	Protected bool
	Reconnect bool // false なら、終了したスロットを再接続せずに failed にする
	Driver    driver.Driver
}

// Clock は時刻と待ち合わせ。テストで差し替える。
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Supervisor はトンネルのプロセスを起動し、停止の指示まで監視する。
// スロット（Driver.Preview の1要素）ごとに、終了したら指数バックオフで張り直す。
type Supervisor struct {
	Out         io.Writer     // 状態の変化と子プロセスの出力を書く先
	StopTimeout time.Duration // SIGTERM から SIGKILL までの猶予。0 なら 5 秒
	Clock       Clock         // nil なら実時間

	out   *syncWriter
	mu    sync.Mutex // slots の差し替えを守る（Run の途中で Slots が呼ばれてもよい）
	slots []*slot
}

type slot struct {
	unit  Unit
	index int // Prepare が返すスライスでの位置
	label string
	mu    sync.Mutex
	st    state.Slot
}

func (s *Supervisor) clock() Clock {
	if s.Clock == nil {
		return realClock{}
	}
	return s.Clock
}

// Run は全ユニットのスロットを起動する。ctx がキャンセルされたら全停止して nil を返す。
// 停止の指示が無いまま全スロットが failed になったら ErrAllExited を返す。
func (s *Supervisor) Run(ctx context.Context, units []Unit) error {
	s.out = &syncWriter{w: s.Out}
	var slots []*slot
	for _, u := range units {
		for i, sp := range u.Driver.Preview() {
			slots = append(slots, &slot{unit: u, index: i, label: "[" + u.ID + "] ", st: state.Slot{
				Tunnel: u.ID, Env: u.Env, Protected: u.Protected, Ports: portStrings(sp.Ports), State: state.Starting,
			}})
		}
	}
	s.mu.Lock()
	s.slots = slots
	s.mu.Unlock()
	s.changed()
	var wg sync.WaitGroup
	for _, sl := range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runSlot(ctx, sl)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return ErrAllExited
}

// Slots は各スロットの現在の状態を返す。Run がスロットを作る前は空。
func (s *Supervisor) Slots() []state.Slot {
	s.mu.Lock()
	slots := s.slots
	s.mu.Unlock()
	out := make([]state.Slot, len(slots))
	for i, sl := range slots {
		sl.mu.Lock()
		out[i] = sl.st
		out[i].Ports = append([]string(nil), sl.st.Ports...)
		sl.mu.Unlock()
	}
	return out
}

func (s *Supervisor) update(sl *slot, fn func(*state.Slot)) {
	sl.mu.Lock()
	fn(&sl.st)
	sl.mu.Unlock()
	s.changed()
}

// changed は状態が変わったときに呼ぶ。
func (s *Supervisor) changed() {}

func (s *Supervisor) runSlot(ctx context.Context, sl *slot) {
	clk := s.clock()
	delay := minBackoff
	for {
		var exit driver.Exit
		var ranFor time.Duration
		var lastErr string
		sp, err := s.prepare(ctx, sl)
		if err != nil {
			if ctx.Err() != nil {
				s.stopped(sl)
				return
			}
			s.out.printf("%s準備に失敗しました: %v\n", sl.label, err)
			lastErr = err.Error()
			exit = sl.unit.Driver.Classify(err, nil)
		} else {
			started := clk.Now()
			res := s.runOnce(ctx, sl, sp)
			ranFor = clk.Now().Sub(started)
			switch {
			case res.stopped:
				s.stopped(sl)
				return
			case res.startErr != nil:
				s.fail(sl, "起動できませんでした: "+res.startErr.Error())
				return
			}
			lastErr = exitText(res.err)
			exit = sl.unit.Driver.Classify(res.err, res.tail)
		}
		s.update(sl, func(st *state.Slot) { st.LastError = lastErr })

		if exit.Fatal {
			msg := exit.Message
			if msg == "" {
				msg = lastErr
			}
			s.fail(sl, msg)
			return
		}
		if !sl.unit.Reconnect {
			s.fail(sl, "reconnect: false のため再接続しません")
			return
		}
		if ranFor >= stableAfter {
			delay = minBackoff
		}
		next := clk.Now().Add(delay)
		s.update(sl, func(st *state.Slot) {
			st.State, st.NextRetryAt, st.ChildPID, st.Healthy = state.Backoff, &next, 0, false
		})
		s.out.printf("%s%ds 後に再接続します\n", sl.label, int(delay/time.Second))
		select {
		case <-ctx.Done():
			s.stopped(sl)
			return
		case <-clk.After(delay):
		}
		s.update(sl, func(st *state.Slot) {
			st.State, st.NextRetryAt = state.Starting, nil
			st.Reconnects++
		})
		delay = min(delay*2, maxBackoff)
	}
}

func (s *Supervisor) stopped(sl *slot) {
	s.update(sl, func(st *state.Slot) {
		st.State, st.ChildPID, st.NextRetryAt, st.Healthy = state.Stopped, 0, nil, false
	})
	s.out.printf("%s停止しました\n", sl.label)
}

func (s *Supervisor) fail(sl *slot, reason string) {
	s.update(sl, func(st *state.Slot) {
		st.State, st.LastError, st.ChildPID, st.NextRetryAt, st.Healthy = state.Failed, reason, 0, nil, false
	})
	s.out.printf("%sfailed: %s\n", sl.label, reason)
}

// prepare は Driver.Prepare を呼び、このスロットの位置の Spec を返す。
func (s *Supervisor) prepare(ctx context.Context, sl *slot) (driver.ProcessSpec, error) {
	specs, err := sl.unit.Driver.Prepare(ctx)
	if err != nil {
		return driver.ProcessSpec{}, err
	}
	if sl.index >= len(specs) {
		return driver.ProcessSpec{}, fmt.Errorf("起動するプロセスの数が変わりました（%d 個目がありません）", sl.index+1)
	}
	sp := specs[sl.index]
	s.update(sl, func(st *state.Slot) { st.Ports = portStrings(sp.Ports) })
	return sp, nil
}

type runResult struct {
	err      error  // プロセスの終了理由
	tail     []byte // stderr の末尾
	stopped  bool   // ctx のキャンセルで止めた
	startErr error  // 起動できなかった
}

// runOnce はプロセスを1回起動し、終了するか ctx がキャンセルされるまで待つ。
func (s *Supervisor) runOnce(ctx context.Context, sl *slot, sp driver.ProcessSpec) runResult {
	lw := &lineWriter{out: s.out, label: sl.label}
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = append(os.Environ(), sp.Env...)
	cmd.Stdout, cmd.Stderr = lw, lw
	cmd.WaitDelay = 2 * time.Second

	child, err := platform.StartChild(cmd)
	if err != nil {
		s.out.printf("%s起動できませんでした: %v\n", sl.label, err)
		return runResult{startErr: err}
	}
	defer child.Close()
	s.out.printf("%s起動しました (pid %d)\n", sl.label, child.Process.Pid)
	s.update(sl, func(st *state.Slot) { st.ChildPID, st.State = child.Process.Pid, state.Starting })

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		lw.flush()
		s.out.printf("%s終了しました: %s\n", sl.label, exitText(err))
		return runResult{err: err, tail: lw.tail()}
	case <-ctx.Done():
		s.stop(child, done)
		lw.flush()
		return runResult{stopped: true}
	}
}

func (s *Supervisor) stop(child *platform.Child, done <-chan error) {
	_ = child.Terminate()
	timeout := s.StopTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	_ = child.Kill()
	<-done
}

func portStrings(ls []driver.Listen) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.String()
	}
	return out
}

func exitText(err error) string {
	if err == nil {
		return "終了コード 0"
	}
	return err.Error()
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) printf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.w, format, args...)
}

// lineWriter は子プロセスの出力を行ごとに区切り、接頭辞を付けて書く。末尾 tailSize バイトを覚えておく。
type lineWriter struct {
	out   *syncWriter
	label string
	mu    sync.Mutex
	buf   []byte
	last  []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = append(l.last, p...)
	if len(l.last) > tailSize {
		l.last = l.last[len(l.last)-tailSize:]
	}
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.out.printf("%s%s\n", l.label, strings.TrimRight(string(l.buf[:i]), "\r"))
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.out.printf("%s%s\n", l.label, strings.TrimRight(string(l.buf), "\r"))
		l.buf = nil
	}
}

// tail は出力の末尾 tailSize バイトの写しを返す。
func (l *lineWriter) tail() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.last...)
}
