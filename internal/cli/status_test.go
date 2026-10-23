package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

const statusYAML = `
tunnels:
  web: { host: app1, forwards: ["18080:localhost:80"] }
environments:
  dev:
    tunnels:
      db: { host: ap, forwards: ["15432:db:5432", "16379:redis:6379"] }
  prod:
    protected: true
    tunnels:
      db: { host: ap, forwards: ["25432:db:5432"] }
`

func loadCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Load(writeConfig(t, yaml), "linux")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStatusRowsCollapsesMultilineError(t *testing.T) {
	cfg := loadCfg(t, statusYAML)
	msg := "aws の準備に失敗:\n  line1\r\n\tline2\n"
	runs := []state.Run{{PID: 1, Slots: []state.Slot{
		{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}, State: state.Failed, LastError: msg},
		{Tunnel: "prod.db", Env: "prod", Ports: []string{"127.0.0.1:25432"}, State: state.Backoff, LastError: msg},
	}}}
	rows := statusRows(cfg, cfg.Tunnels(), runs, func(string) bool { return false }, time.Now())
	const want = "aws の準備に失敗: line1 line2"
	seen := 0
	for _, r := range rows {
		if strings.ContainsAny(r.State+r.LastError, "\r\n\t") {
			t.Errorf("改行や連続空白が残っている: %+v", r)
		}
		if r.LastError == want || r.State == "failed: "+want {
			seen++
		}
	}
	if seen < 2 {
		t.Errorf("LastError と failed の両方が 1 行になる: %+v", rows)
	}
}

func TestStatusRows(t *testing.T) {
	cfg := loadCfg(t, statusYAML)
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	next := now.Add(11500 * time.Millisecond)
	runs := []state.Run{{PID: 1, Slots: []state.Slot{
		{Tunnel: "dev.db", Env: "dev", Ports: []string{"127.0.0.1:15432", "127.0.0.1:16379"}, State: state.Up},
		{Tunnel: "prod.db", Env: "prod", Ports: []string{"127.0.0.1:25432"}, State: state.Backoff, NextRetryAt: &next, Reconnects: 3, LastError: "exit status 255"},
	}}}
	open := map[string]bool{"127.0.0.1:15432": true, "127.0.0.1:16379": true, "127.0.0.1:18080": true}
	rows := statusRows(cfg, cfg.Tunnels(), runs, func(a string) bool { return open[a] }, now)
	got := map[string]statusRow{}
	for _, r := range rows {
		got[r.Env+"/"+r.Tunnel] = r
	}
	if r := got["dev/db"]; r.State != "up" || r.Ports != "127.0.0.1:15432,127.0.0.1:16379" {
		t.Errorf("dev.db = %+v", r)
	}
	if r := got["prod/db"]; r.State != "backoff (次回 12s 後)" || r.Reconnects != 3 || !r.Protected || r.LastError != "exit status 255" {
		t.Errorf("prod.db = %+v", r)
	}
	if r := got["/web"]; r.State != "hashigo 外のプロセスが使用中" {
		t.Errorf("web = %+v", r)
	}

	failed := []state.Run{{PID: 1, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}, State: state.Failed, LastError: "AWS の認証が切れています"}}}}
	none := func(string) bool { return false }
	rows = statusRows(cfg, cfg.Tunnels(), failed, none, now)
	for _, r := range rows {
		switch r.Env + "/" + r.Tunnel {
		case "dev/db":
			if r.State != "failed: AWS の認証が切れています" {
				t.Errorf("failed = %+v", r)
			}
		case "/web":
			if r.State != "停止" {
				t.Errorf("状態ファイルも接続も無ければ停止: %+v", r)
			}
		}
	}

	up := []state.Run{{PID: 1, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}, State: state.Up}}}}
	if r := statusRows(cfg, []*config.Tunnel{cfg.Envs["dev"].Tunnels["db"]}, up, none, now)[0]; r.State != "unhealthy" {
		t.Errorf("状態ファイルが up でも接続できなければ unhealthy: %+v", r)
	}
}

func TestRenderTable(t *testing.T) {
	var out bytes.Buffer
	renderTable(&out, []string{"環境", "トンネル"}, [][]cell{
		{{Text: "prod", Color: "1;31"}, {Text: "db"}},
		{{Text: "-"}, {Text: "web"}},
	})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if lines[0] != "環境  トンネル" || lines[1] != "\x1b[1;31mprod\x1b[0m  db" || lines[2] != "-     web" {
		t.Errorf("表 =\n%q", lines)
	}
	if dispWidth("環境ab") != 6 {
		t.Errorf("全角は幅 2: %d", dispWidth("環境ab"))
	}
	c1, c2 := envColor("dev", false), envColor("dev", false)
	if c1 != c2 || envColor("x", true) != "1;31" {
		t.Error("色は名前で決まり、protected は赤の太字")
	}
}

func TestStatusCmd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	p := writeConfig(t, fmt.Sprintf("environments:\n  dev:\n    tunnels:\n      db: { host: ap, forwards: [\"%d:db:5432\"] }\n", port))
	h := testHost(t)

	code, out, _ := runWith(t, h, "status", "--config", p)
	if code != 1 || !strings.Contains(out, "hashigo 外のプロセスが使用中") || !strings.Contains(out, "環境") {
		t.Fatalf("状態ファイルが無ければ hashigo 外: code=%d\n%s", code, out)
	}

	st, err := platform.StartTime(os.Getpid())
	if err != nil {
		t.Skip("この OS ではプロセスの起動時刻を取れない")
	}
	dir, _ := h.StateDir()
	run := state.Run{PID: os.Getpid(), StartedAt: st, Slots: []state.Slot{{Tunnel: "dev.db", Env: "dev", Ports: []string{fmt.Sprintf("127.0.0.1:%d", port)}, State: state.Up}}}
	if err := state.New(dir, os.Getpid()).Write(run); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runWith(t, h, "status", "--config", p, "dev")
	if code != 0 || !strings.Contains(out, "up") || errOut != "" {
		t.Fatalf("全部 up なら 0: code=%d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("TTY でなければ色を付けない")
	}
}
