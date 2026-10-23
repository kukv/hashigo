package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample(pid int) Run {
	next := time.Date(2026, 10, 23, 10, 0, 12, 0, time.UTC)
	return Run{
		PID: pid, StartedAt: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC), Config: "/path/hashigo.yaml",
		Slots: []Slot{{
			Tunnel: "dev.db", Env: "dev", Ports: []string{"127.0.0.1:15432"},
			State: Backoff, ChildPID: 5678, Reconnects: 3, NextRetryAt: &next, LastError: "exit status 255",
		}},
	}
}

func TestWriteAndList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	f := New(dir, 1234)
	if err := f.Write(sample(1234)); err != nil {
		t.Fatal(err)
	}
	if f.Path != filepath.Join(dir, "1234.json") {
		t.Errorf("Path = %s", f.Path)
	}
	runs, err := List(dir, func(Run) bool { return true })
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %+v, %v", runs, err)
	}
	if got := runs[0].Slots[0]; got.State != Backoff || got.Reconnects != 3 || got.NextRetryAt == nil {
		t.Errorf("読み戻した値 = %+v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("一時ファイルが残っている: %v", entries)
	}
}

func TestJSONKeys(t *testing.T) {
	b, err := json.Marshal(sample(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		`"pid"`, `"started_at"`, `"config"`, `"slots"`, `"tunnel"`, `"env"`, `"protected"`, `"ports"`,
		`"state"`, `"child_pid"`, `"reconnects"`, `"next_retry_at"`, `"last_error"`, `"healthy"`,
	} {
		if !strings.Contains(string(b), k) {
			t.Errorf("キー %s が無い: %s", k, b)
		}
	}
}

func TestListRemovesStale(t *testing.T) {
	dir := t.TempDir()
	for _, pid := range []int{10, 20} {
		if err := New(dir, pid).Write(sample(pid)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "30.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := List(dir, func(r Run) bool { return r.PID == 20 })
	if err != nil || len(runs) != 1 || runs[0].PID != 20 {
		t.Fatalf("List = %+v, %v", runs, err)
	}
	for _, name := range []string{"10.json", "30.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s は残骸として削除する", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "memo.txt")); err != nil {
		t.Error("状態ファイル以外は触らない")
	}
}

func TestListMissingDir(t *testing.T) {
	runs, err := List(filepath.Join(t.TempDir(), "none"), func(Run) bool { return true })
	if err != nil || runs != nil {
		t.Fatalf("ディレクトリが無ければ空: %+v, %v", runs, err)
	}
}

func TestRemove(t *testing.T) {
	f := New(t.TempDir(), 1)
	if err := f.Write(sample(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(); err != nil {
		t.Errorf("2回目の Remove もエラーにしない: %v", err)
	}
}

func TestFindPort(t *testing.T) {
	runs := []Run{sample(1234)}
	run, slot, ok := FindPort(runs, "127.0.0.1:15432")
	if !ok || run.PID != 1234 || slot.Tunnel != "dev.db" {
		t.Fatalf("FindPort = %+v %+v %v", run, slot, ok)
	}
	if _, _, ok := FindPort(runs, "127.0.0.1:9"); ok {
		t.Error("無いポートは false")
	}
}
