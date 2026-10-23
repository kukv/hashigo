package cli

import (
	"bytes"
	"context"
	"fmt"
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
// 状態ファイルはテストごとの一時ディレクトリに書く。
func testHost(t testing.TB) *platform.Host {
	stateHome := t.TempDir()
	return &platform.Host{
		GOOS: "linux",
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		LookPath: func(name string) (string, error) {
			switch name {
			case "ssh":
				return "/usr/bin/ssh", nil
			case "aws":
				return "/usr/bin/aws", nil
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
	d := Deps{Host: testHost(t), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, IsTTY: func() bool { return false }}
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

// mapRunner は「コマンド 引数...」をキーに出力を返す Runner。
type mapRunner map[string]string

func (m mapRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	return []byte(out), nil
}

// funcRunner は呼び出しを関数で受ける Runner。
type funcRunner func(name string, args []string) ([]byte, error)

func (f funcRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return f(name, args)
}

// wslHost は NAT モードの WSL として振る舞う Host。
func wslHost(t testing.TB, paths map[string]string) *platform.Host {
	h := testHost(t)
	base := h.Getenv
	h.Getenv = func(k string) string {
		if k == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return base(k)
	}
	h.LookPath = func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	h.Run = mapRunner{
		"wslinfo --networking-mode":     "nat\n",
		"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
	}
	return h
}

func runWith(t *testing.T, h *platform.Host, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := Deps{Host: h, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, IsTTY: func() bool { return false }}
	code := Run(context.Background(), d, args)
	return code, out.String(), errOut.String()
}

func TestCmdECS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	p := writeConfig(t, cmdYAML)
	code, out, errOut := run(t, "cmd", "--config", p, "task")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	want := `# 実行環境: linux
# task  aws=/usr/bin/aws  aws_config=/home/u/.aws/config  profile=default
# <task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します
/usr/bin/aws ecs list-tasks --cluster c --service-name s --desired-status RUNNING --query 'taskArns[0]' --output text --profile default
/usr/bin/aws ecs describe-tasks --cluster c --tasks '<task-arn>' --query 'tasks[0].containers[?name=='\''app'\''].runtimeId | [0]' --output text --profile default
/usr/bin/aws ssm start-session --profile default --target 'ecs:c_<task-id>_<runtime-id>' --document-name AWS-StartPortForwardingSessionToRemoteHost --parameters host=db,portNumber=5432,localPortNumber=35432
`
	if out != want {
		t.Errorf("cmd の出力 =\n%s\nwant\n%s", out, want)
	}
}

func TestCmdSSHConfig(t *testing.T) {
	p := writeConfig(t, "defaults:\n  ssh_config: /home/u/work/ssh_config\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, out, errOut := run(t, "cmd", "--config", p, "t")
	if code != 0 || !strings.Contains(out, "ssh_config=/home/u/work/ssh_config") || !strings.Contains(out, "-o BatchMode=yes -F /home/u/work/ssh_config -L") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errOut)
	}
	p = writeConfig(t, "defaults:\n  ssh_config: wsl\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, _, errOut = run(t, "cmd", "--config", p, "t")
	if code != 1 || !strings.Contains(errOut, "ssh_config: wsl は WSL でだけ使えます") {
		t.Fatalf("WSL でなければ wsl はエラー: code=%d stderr=%s", code, errOut)
	}
}

func TestCmdRelay(t *testing.T) {
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	h := wslHost(t, map[string]string{"ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe", "socat": "/usr/bin/socat"})
	code, out, errOut := runWith(t, h, "cmd", "--config", p, "db")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"# 実行環境: linux (WSL, relay)",
		"ssh_config=/mnt/c/Users/taro/.ssh/config",
		"-L '<ゲートウェイIP>:15432:db:5432'",
		"/usr/bin/socat TCP-LISTEN:15432,bind=127.0.0.1,reuseaddr,fork 'TCP:<ゲートウェイIP>:15432'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

func TestCmdRelayErrors(t *testing.T) {
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	h := wslHost(t, map[string]string{"ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"})
	if code, _, errOut := runWith(t, h, "cmd", "--config", p, "db"); code != 1 || !strings.Contains(errOut, "socat が必要です") {
		t.Errorf("socat が無ければエラー: code=%d %s", code, errOut)
	}

	p = writeConfig(t, cmdYAML)
	h = wslHost(t, map[string]string{"aws.exe": "/mnt/c/aws.exe", "socat": "/usr/bin/socat"})
	if code, _, errOut := runWith(t, h, "cmd", "--config", p, "task"); code != 1 || !strings.Contains(errOut, "relay モードでは使えません") {
		t.Errorf("ECS × relay はエラー: code=%d %s", code, errOut)
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
