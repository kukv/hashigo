// Package testutil はテスト用のヘルパー。
package testutil

import (
	"bytes"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// FreePort は空いているローカルポートを返す。
func FreePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// PortOpen は 127.0.0.1:port に接続できるかを返す。
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Eventually は cond が true になるまで待つ。timeout を過ぎたら失敗にする。
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s（%s 待っても満たされない）", msg, timeout)
}

// WaitPort はポートに接続できるようになるまで待つ。
func WaitPort(t testing.TB, port int, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, func() bool { return PortOpen(port) }, "ポート "+strconv.Itoa(port)+" が開かない")
}

// SyncBuffer は複数の goroutine から書き込める bytes.Buffer。
type SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
