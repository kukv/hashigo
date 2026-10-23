package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

// fakeRunner は「コマンド 引数...」をキーに出力を返す。値が ERR: で始まればその文言のエラーにする。
type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	if msg, isErr := strings.CutPrefix(out, "ERR:"); isErr {
		return nil, errors.New(msg)
	}
	return []byte(out), nil
}

func newHost(env, files, paths map[string]string, run fakeRunner) *platform.Host {
	return &platform.Host{
		GOOS:   "linux",
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			if p, ok := paths[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if _, ok := files[p]; ok {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run:     run,
	}
}

func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hashigo.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p, "linux")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runDoctor(h *platform.Host, cfg *config.Config, runs []state.Run, dial func(string) bool) []Result {
	return Run(context.Background(), Env{Host: h, Config: cfg, Tunnels: cfg.Tunnels(), Runs: runs, Dial: dial})
}

func find(t *testing.T, rs []Result, title string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Title == title {
			return r
		}
	}
	t.Fatalf("項目 %q が無い", title)
	return Result{}
}

func hasLine(r Result, sub string) bool {
	for _, l := range r.Lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func closed(string) bool { return false }

const sshYAML = "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n"

const wslRunBase = "wslinfo --networking-mode"

func TestPrint(t *testing.T) {
	var out bytes.Buffer
	rs := []Result{
		{Level: OK, Title: "a", Lines: []string{"x"}},
		{Level: Warn, Title: "b"},
		{Level: Fail, Title: "c"},
		{Level: NA, Title: "d"},
	}
	Print(&out, rs)
	want := "✓ a\n    x\n⚠ b\n✗ c\n- d: 対象外\n"
	if out.String() != want {
		t.Errorf("Print =\n%s\nwant\n%s", out.String(), want)
	}
	if !HasFail(rs) || HasFail(rs[:2]) {
		t.Error("HasFail は ✗ があるときだけ true")
	}
}

func TestLinuxSSHOnly(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V": "OpenSSH_9.6p1, OpenSSL 3.0.13\n",
	})
	rs := runDoctor(h, loadConfig(t, sshYAML), nil, closed)
	if r := find(t, rs, "実行環境"); r.Level != OK || !hasLine(r, "WSL: 対象外") {
		t.Errorf("実行環境 = %+v", r)
	}
	if r := find(t, rs, "ssh"); r.Level != OK || !hasLine(r, "/usr/bin/ssh: OpenSSH_9.6p1") {
		t.Errorf("ssh = %+v", r)
	}
	for _, title := range []string{"aws と session-manager-plugin", "socat（relay モード）", "ECS × relay"} {
		if r := find(t, rs, title); r.Level != NA {
			t.Errorf("%s は対象外: %+v", title, r)
		}
	}
	if r := find(t, rs, "ローカルポートの衝突"); r.Level != OK || !hasLine(r, "衝突なし") {
		t.Errorf("ポート = %+v", r)
	}
	if r := find(t, rs, "ssh / aws の config"); r.Level != OK || !hasLine(r, "ssh (/usr/bin/ssh, ssh_config: auto)") {
		t.Errorf("config = %+v", r)
	}
	if HasFail(rs) {
		t.Errorf("✗ は無い: %+v", rs)
	}
}

func TestWSLRelayWithECS(t *testing.T) {
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{"/mnt/c/Users/taro/.wslconfig": "[wsl2]\r\nnetworkingMode=nat\r\n"},
		map[string]string{"aws.exe": "/mnt/c/aws.exe"},
		fakeRunner{
			wslRunBase:                      "nat\n",
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"/mnt/c/aws.exe --version":      "aws-cli/2.17.0 Python/3.11 Windows/10\r\n",
		})
	cfg := loadConfig(t, "tunnels:\n  task: { type: ecs, cluster: c, service: s, container: app, forwards: [\"35432:db:5432\"] }\n")
	rs := runDoctor(h, cfg, nil, closed)
	if r := find(t, rs, "実行環境"); !hasLine(r, "hashigo のモード relay") || !hasLine(r, "networkingMode=nat") {
		t.Errorf("実行環境 = %+v", r)
	}
	if r := find(t, rs, "aws と session-manager-plugin"); r.Level != Fail || !hasLine(r, "aws-cli/2.17.0") || !hasLine(r, "session-manager-plugin が見つかりません") {
		t.Errorf("aws = %+v", r)
	}
	if r := find(t, rs, "socat（relay モード）"); r.Level != Fail {
		t.Errorf("socat = %+v", r)
	}
	if r := find(t, rs, "ECS × relay"); r.Level != Fail || !hasLine(r, "mirrored") {
		t.Errorf("ECS × relay = %+v", r)
	}
	if r := find(t, rs, "ssh"); r.Level != NA {
		t.Errorf("ssh トンネルが無ければ対象外: %+v", r)
	}
}

func TestPortsOwner(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{"/usr/bin/ssh -V": "OpenSSH_9\n"})
	cfg := loadConfig(t, "tunnels:\n  a: { host: x, forwards: [\"15432:db:5432\"] }\n  b: { host: y, forwards: [\"16379:r:6379\"] }\n")
	runs := []state.Run{{PID: 7, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}}}}}
	r := find(t, runDoctor(h, cfg, runs, func(string) bool { return true }), "ローカルポートの衝突")
	if r.Level != Warn || !hasLine(r, "dev.db（pid 7 の up）が使用中") || !hasLine(r, "127.0.0.1:16379（b）: hashigo 外のプロセスが使用中") {
		t.Errorf("ポート = %+v", r)
	}
}
