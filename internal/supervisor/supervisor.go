// Package supervisor は子プロセスを起動・監視・停止する。
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
)

// ErrAllExited は、停止の指示が無いまま全スロットが終了したことを表す。
var ErrAllExited = errors.New("すべてのトンネルが終了しました")

// Unit は監視する1トンネル。
type Unit struct {
	ID     string
	Driver driver.Driver
}

// Supervisor はトンネルのプロセスを起動し、停止の指示まで監視する。
// MVP では再接続しない。終了したスロットはそのまま終了扱いにする。
type Supervisor struct {
	Out         io.Writer     // 状態の変化と子プロセスの出力を書く先
	StopTimeout time.Duration // SIGTERM から SIGKILL までの猶予。0 なら 5 秒
}

// Run は全ユニットを起動する。ctx がキャンセルされたら全停止して nil を返す。
// 停止の指示が無いまま全スロットが終了したら ErrAllExited を返す。
func (s *Supervisor) Run(ctx context.Context, units []Unit) error {
	out := &syncWriter{w: s.Out}
	var wg sync.WaitGroup
	for _, u := range units {
		specs, err := u.Driver.Prepare(ctx)
		if err != nil {
			out.printf("[%s] 準備に失敗しました: %v\n", u.ID, err)
			continue
		}
		for _, sp := range specs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runSlot(ctx, out, sp)
			}()
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return ErrAllExited
}

func (s *Supervisor) runSlot(ctx context.Context, out *syncWriter, sp driver.ProcessSpec) {
	label := "[" + sp.Tunnel + "] "
	lw := &lineWriter{out: out, label: label}
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = append(os.Environ(), sp.Env...)
	cmd.Stdout, cmd.Stderr = lw, lw
	cmd.WaitDelay = 2 * time.Second
	platform.PrepareCmd(cmd)

	if err := cmd.Start(); err != nil {
		out.printf("%s起動できませんでした: %v\n", label, err)
		return
	}
	out.printf("%s起動しました (pid %d)\n", label, cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		lw.flush()
		out.printf("%s終了しました: %s\n", label, exitText(err))
	case <-ctx.Done():
		s.stop(cmd, done)
		lw.flush()
		out.printf("%s停止しました\n", label)
	}
}

func (s *Supervisor) stop(cmd *exec.Cmd, done <-chan error) {
	_ = platform.Terminate(cmd.Process)
	timeout := s.StopTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	_ = platform.Kill(cmd.Process)
	<-done
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

// lineWriter は子プロセスの出力を行ごとに区切り、接頭辞を付けて書く。
type lineWriter struct {
	out   *syncWriter
	label string
	mu    sync.Mutex
	buf   []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
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
