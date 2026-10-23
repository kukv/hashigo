package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/platform"
)

// testHost は WSL ではない Linux として振る舞う Host。ファイルの存在確認だけは本物を使う。
func testHost() *platform.Host {
	return &platform.Host{
		GOOS:     "linux",
		Getenv:   func(string) string { return "" },
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		LookPath: func(name string) (string, error) {
			if name == "ssh" {
				return "/usr/bin/ssh", nil
			}
			return "", exec.ErrNotFound
		},
		Stat:    os.Stat,
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run:     platform.ExecRunner{},
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hashigo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := Deps{Host: testHost(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, IsTTY: func() bool { return false }}
	code := Run(context.Background(), d, args)
	return code, out.String(), errOut.String()
}

const cmdYAML = `
hosts:
  bastion: { host: bastion.example.com, user: ec2-user }
  inner: { host: 10.0.1.10, user: admin, via: bastion }
tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432
      - 16379:redis.internal:6379
  web:
    host: app1
    keepalive: 45s
    forwards:
      - { local: "127.0.0.1:8080", remote: "localhost:80" }
  task:
    type: ecs
    cluster: c
    service: s
    container: app
    forwards: ["35432:db:5432"]
`

func TestValidateCmd(t *testing.T) {
	p := writeConfig(t, cmdYAML)
	code, out, _ := run(t, "validate", "--config", p)
	if code != 0 || !strings.Contains(out, "OK: "+p) {
		t.Fatalf("code=%d out=%q", code, out)
	}

	bad := writeConfig(t, "tunnels:\n  t1:\n    hots: x\n")
	code, _, errOut := run(t, "validate", "--config", bad)
	if code != 1 || !strings.Contains(errOut, bad+":3:") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestCmdCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	p := writeConfig(t, cmdYAML)
	code, out, errOut := run(t, "cmd", "--config", p, "prod-db", "web")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	want := `# 実行環境: linux
# prod-db  ssh=/usr/bin/ssh  ssh_config=/home/u/.ssh/config
/usr/bin/ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o BatchMode=yes -J ec2-user@bastion.example.com -L 127.0.0.1:15432:db.internal:5432 -L 127.0.0.1:16379:redis.internal:6379 -- admin@10.0.1.10
# web  ssh=/usr/bin/ssh  ssh_config=/home/u/.ssh/config
/usr/bin/ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=45 -o ServerAliveCountMax=3 -o BatchMode=yes -L 127.0.0.1:8080:localhost:80 -- app1
`
	if out != want {
		t.Errorf("cmd の出力 =\n%s\nwant\n%s", out, want)
	}
}

func TestCmdNotYet(t *testing.T) {
	p := writeConfig(t, cmdYAML)
	code, _, errOut := run(t, "cmd", "--config", p, "task")
	if code != 1 || !strings.Contains(errOut, "安定化フェーズで対応します") {
		t.Fatalf("ecs は未対応: code=%d stderr=%q", code, errOut)
	}
	p = writeConfig(t, "defaults:\n  ssh_config: wsl\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, _, errOut = run(t, "cmd", "--config", p, "t")
	if code != 1 || !strings.Contains(errOut, "ssh_config の切り替えは安定化フェーズで対応します") {
		t.Fatalf("ssh_config は auto のみ: code=%d stderr=%q", code, errOut)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"/usr/bin/ssh", "-o", "Foo=bar baz", "it's"})
	if got != `/usr/bin/ssh -o 'Foo=bar baz' 'it'\''s'` {
		t.Errorf("shellJoin = %s", got)
	}
	if got := shellJoin([]string{"[::1]:8080:host:80"}); got != `'[::1]:8080:host:80'` {
		t.Errorf("IPv6 は quote される: %s", got)
	}
}
