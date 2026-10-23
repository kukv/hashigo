// Package state は up 1回ごとの状態ファイルを読み書きする。status と、起動前の衝突相手の特定に使う。
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// スロットの状態。
const (
	Starting  = "starting"
	Up        = "up"
	Unhealthy = "unhealthy"
	Backoff   = "backoff"
	Failed    = "failed"
	Stopped   = "stopped"
)

// Slot はスロット1つの状態。
type Slot struct {
	Tunnel      string     `json:"tunnel"`
	Env         string     `json:"env"`
	Protected   bool       `json:"protected"`
	Ports       []string   `json:"ports"`
	State       string     `json:"state"`
	ChildPID    int        `json:"child_pid"`
	Reconnects  int        `json:"reconnects"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	LastError   string     `json:"last_error"`
	Healthy     bool       `json:"healthy"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
}

// Run は up 1回分の状態ファイルの中身。
type Run struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// StartToken は残骸の判定に使うプロセスの起動識別子。空なら StartedAt で判定する。
	StartToken string `json:"start_token,omitempty"`
	Config     string `json:"config"`
	Slots      []Slot `json:"slots"`
}

// File は状態ファイル1つ。複数の goroutine から Write してよい。
type File struct {
	Path string
	mu   sync.Mutex
}

// New は dir/<pid>.json の状態ファイルを返す。
func New(dir string, pid int) *File {
	return &File{Path: filepath.Join(dir, strconv.Itoa(pid)+".json")}
}

// Write は r を書く。読み手が途中の内容を見ないよう、一時ファイルに書いてから rename する。
func (f *File) Write(r Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Remove は状態ファイルを削除する。無ければ何もしない。
func (f *File) Remove() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List は dir の状態ファイルを読み、生きている up のものだけを pid 順に返す。
// 記録した pid が生きていない、起動時刻が違う（pid が再利用された）、JSON として壊れているファイルは残骸として削除する。
func List(dir string, alive func(Run) bool) ([]Run, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".tmp-") {
			continue
		}
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue // 書き換えの途中などで読めないものは、残骸とみなさない
		}
		var r Run
		if err := json.Unmarshal(b, &r); err != nil || !alive(r) {
			_ = os.Remove(p)
			continue
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].PID < runs[j].PID })
	return runs, nil
}

// FindPort は addr（127.0.0.1:15432 の形）を使っているスロットを探す。
func FindPort(runs []Run, addr string) (Run, Slot, bool) {
	for _, r := range runs {
		for _, s := range r.Slots {
			if slices.Contains(s.Ports, addr) {
				return r, s, true
			}
		}
	}
	return Run{}, Slot{}, false
}
