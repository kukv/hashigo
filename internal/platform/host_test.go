package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	return []byte(out), nil
}

func newHost(env map[string]string, files map[string]string, paths map[string]string, run fakeRunner) *Host {
	return &Host{
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

var wslRun = fakeRunner{
	"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
	"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
}

func TestIsWSL(t *testing.T) {
	if !newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil).IsWSL() {
		t.Error("WSL_DISTRO_NAME があれば WSL")
	}
	if !newHost(map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}, nil, nil, nil).IsWSL() {
		t.Error("WSL_INTEROP があれば WSL")
	}
	wslKernel := "Linux version 6.18 (microsoft-standard-WSL2)"
	if !newHost(nil, map[string]string{"/proc/version": wslKernel, "/proc/sys/fs/binfmt_misc/WSLInterop": ""}, nil, nil).IsWSL() {
		t.Error("/proc/version に microsoft があり WSLInterop があれば WSL")
	}
	if newHost(nil, map[string]string{"/proc/version": wslKernel}, nil, nil).IsWSL() {
		t.Error("WSL 上の Docker コンテナ（/proc/version だけが microsoft）は WSL ではない")
	}
	if newHost(nil, map[string]string{"/proc/version": "Linux version 6.8 (ubuntu)"}, nil, nil).IsWSL() {
		t.Error("ふつうの Linux は WSL ではない")
	}
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil)
	h.GOOS = "darwin"
	if h.IsWSL() {
		t.Error("linux 以外は WSL ではない")
	}
}

func TestWindowsHome(t *testing.T) {
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, wslRun)
	got, err := h.WindowsHome(context.Background())
	if err != nil || got != "/mnt/c/Users/taro" {
		t.Fatalf("WindowsHome = %q, %v", got, err)
	}
}

func TestResolveWSLMode(t *testing.T) {
	ctx := context.Background()
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}

	mirrored := fakeRunner{"wslinfo --networking-mode": "mirrored\n"}
	if got, _ := newHost(wslEnv, nil, nil, mirrored).ResolveWSLMode(ctx, "auto"); got != "mirrored" {
		t.Errorf("wslinfo が mirrored → %q", got)
	}

	// wslinfo が無いときは .wslconfig を読む
	files := map[string]string{"/mnt/c/Users/taro/.wslconfig": "[wsl2]\nnetworkingMode=mirrored\n"}
	if got, _ := newHost(wslEnv, files, nil, wslRun).ResolveWSLMode(ctx, ""); got != "mirrored" {
		t.Errorf(".wslconfig が mirrored → %q", got)
	}

	if got, _ := newHost(wslEnv, nil, nil, wslRun).ResolveWSLMode(ctx, "auto"); got != "relay" {
		t.Errorf("判定できなければ relay → %q", got)
	}
	if got, _ := newHost(wslEnv, nil, nil, nil).ResolveWSLMode(ctx, "native"); got != "native" {
		t.Errorf("明示した値はそのまま → %q", got)
	}
	if got, _ := newHost(nil, nil, nil, nil).ResolveWSLMode(ctx, "auto"); got != "" {
		t.Errorf("WSL でなければ空 → %q", got)
	}
}

func TestSSHBinary(t *testing.T) {
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}
	paths := map[string]string{"ssh": "/usr/bin/ssh", "ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"}

	cases := []struct {
		name    string
		env     map[string]string
		paths   map[string]string
		files   map[string]string
		setting string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "非WSLのauto", paths: paths, setting: "auto", want: "/usr/bin/ssh"},
		{name: "WSLのauto", env: wslEnv, paths: paths, setting: "auto", mode: "mirrored", want: "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"},
		{name: "WSLのnative", env: wslEnv, paths: paths, setting: "", mode: "native", want: "/usr/bin/ssh"},
		{name: "名前の指定", env: wslEnv, paths: paths, setting: "ssh", mode: "mirrored", want: "/usr/bin/ssh"},
		{name: "PATHに無いssh.exe", env: wslEnv, files: map[string]string{windowsSSH: ""}, setting: "auto", mode: "mirrored", want: windowsSSH},
		{name: "パスの指定", files: map[string]string{"/opt/ssh": ""}, setting: "/opt/ssh", want: "/opt/ssh"},
		{name: "存在しないパス", setting: "/nope/ssh", wantErr: true},
		{name: "見つからない", setting: "auto", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := newHost(c.env, c.files, c.paths, nil).SSHBinary(c.setting, c.mode)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("SSHBinary = %q, %v; want %q (err=%v)", got, err, c.want, c.wantErr)
			}
		})
	}
}

func TestSSHConfigPath(t *testing.T) {
	ctx := context.Background()
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, wslRun)
	if got, _ := wsl.SSHConfigPath(ctx, "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"); got != "/mnt/c/Users/taro/.ssh/config" {
		t.Errorf("ssh.exe → %q", got)
	}
	plain := newHost(nil, nil, nil, nil)
	if got, _ := plain.SSHConfigPath(ctx, "/usr/bin/ssh"); got != filepath.Join("/home/u", ".ssh", "config") {
		t.Errorf("ssh → %q", got)
	}
}

func TestDetect(t *testing.T) {
	h := Detect()
	if h.GOOS == "" || h.Getenv == nil || h.Run == nil {
		t.Fatal("Detect は全フィールドを埋める")
	}
	if h.RunWith == nil {
		t.Fatal("Detect は RunWith も埋める")
	}
	_ = errors.Is(nil, os.ErrNotExist)
}

func TestAWSBinary(t *testing.T) {
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}
	paths := map[string]string{"aws": "/usr/bin/aws", "aws.exe": "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"}
	cases := []struct {
		name    string
		env     map[string]string
		paths   map[string]string
		files   map[string]string
		setting string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "非WSLのauto", paths: paths, setting: "auto", want: "/usr/bin/aws"},
		{name: "WSLのauto", env: wslEnv, paths: paths, setting: "auto", mode: "mirrored", want: "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"},
		{name: "WSLのnative", env: wslEnv, paths: paths, setting: "", mode: "native", want: "/usr/bin/aws"},
		{name: "PATHに無いaws.exe", env: wslEnv, files: map[string]string{windowsAWS: ""}, setting: "auto", mode: "mirrored", want: windowsAWS},
		{name: "パスの指定", files: map[string]string{"/opt/aws": ""}, setting: "/opt/aws", want: "/opt/aws"},
		{name: "存在しないパス", setting: "/nope/aws", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := newHost(c.env, c.files, c.paths, nil).AWSBinary(c.setting, c.mode)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("AWSBinary = %q, %v; want %q (err=%v)", got, err, c.want, c.wantErr)
			}
		})
	}
	if _, err := newHost(nil, nil, nil, nil).AWSBinary("/nope/aws", ""); err == nil || !strings.Contains(err.Error(), "aws に指定したファイルがありません") {
		t.Errorf("メッセージ = %v", err)
	}
}

func TestCrossesToWindows(t *testing.T) {
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil)
	if !wsl.CrossesToWindows("/mnt/c/Windows/System32/OpenSSH/ssh.EXE") {
		t.Error("WSL で .exe なら true（大文字小文字は区別しない）")
	}
	if wsl.CrossesToWindows("/usr/bin/ssh") {
		t.Error("Linux 版なら false")
	}
	win := newHost(nil, nil, nil, nil)
	win.GOOS = "windows"
	if win.CrossesToWindows(`C:\Windows\System32\OpenSSH\ssh.exe`) {
		t.Error("Windows ネイティブでは false")
	}
}

func TestStateDir(t *testing.T) {
	h := newHost(nil, nil, nil, nil)
	if got, _ := h.StateDir(); got != filepath.Join("/home/u", ".local", "state", "hashigo", "run") {
		t.Errorf("既定 → %q", got)
	}
	h = newHost(map[string]string{"XDG_STATE_HOME": "/x/state"}, nil, nil, nil)
	if got, _ := h.StateDir(); got != filepath.Join("/x/state", "hashigo", "run") {
		t.Errorf("XDG_STATE_HOME → %q", got)
	}
	h = newHost(map[string]string{"LOCALAPPDATA": `C:\Users\taro\AppData\Local`}, nil, nil, nil)
	h.GOOS = "windows"
	if got, _ := h.StateDir(); got != filepath.Join(`C:\Users\taro\AppData\Local`, "hashigo", "run") {
		t.Errorf("Windows → %q", got)
	}
	h.Getenv = func(string) string { return "" }
	if _, err := h.StateDir(); err == nil {
		t.Error("Windows で LOCALAPPDATA が無ければエラー")
	}
}

func TestHostRunner(t *testing.T) {
	run := fakeRunner{}
	h := newHost(nil, nil, nil, run)
	if got := h.Runner(RunOptions{Interactive: true}); got == nil {
		t.Fatal("RunWith が無ければ Run を返す")
	}
	var seen RunOptions
	h.RunWith = func(o RunOptions) Runner { seen = o; return run }
	h.Runner(RunOptions{Env: []string{"A=1"}, Combined: true})
	if len(seen.Env) != 1 || !seen.Combined {
		t.Errorf("RunWith に渡した値 = %+v", seen)
	}
}

func TestExecRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh を使うため Unix 系でのみ確認する")
	}
	ctx := context.Background()
	out, err := ExecRunner{Opts: RunOptions{Env: []string{"HASHIGO_X=42"}}}.Output(ctx, "sh", "-c", "echo $HASHIGO_X")
	if err != nil || strings.TrimSpace(string(out)) != "42" {
		t.Errorf("Env = %q, %v", out, err)
	}
	out, err = ExecRunner{Opts: RunOptions{Combined: true}}.Output(ctx, "sh", "-c", "echo out; echo err >&2")
	if err != nil || !strings.Contains(string(out), "err") {
		t.Errorf("Combined = %q, %v", out, err)
	}
	_, err = ExecRunner{}.Output(ctx, "sh", "-c", "echo bad >&2; exit 3")
	if got := ErrText(err); got != "bad" {
		t.Errorf("ErrText = %q", got)
	}
	if got := ErrText(errors.New("x")); got != "x" {
		t.Errorf("ErrText(普通のエラー) = %q", got)
	}
}
