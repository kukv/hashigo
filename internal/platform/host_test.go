package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	if !newHost(nil, map[string]string{"/proc/version": "Linux version 6.18 (microsoft-standard-WSL2)"}, nil, nil).IsWSL() {
		t.Error("/proc/version に microsoft があれば WSL")
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
	_ = errors.Is(nil, os.ErrNotExist)
}
