// Package platform は OS と WSL の差分を引き受ける。
package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Runner は外部コマンドを実行して標準出力を返す。
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner は os/exec で実際に実行する Runner。
type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Host は実行環境への窓口。テストでは各関数を差し替える。
type Host struct {
	GOOS     string
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	LookPath func(string) (string, error)
	Stat     func(string) (fs.FileInfo, error)
	HomeDir  func() (string, error)
	Run      Runner

	winHome string // WindowsHome の結果を覚えておく
}

// Detect は実際の実行環境の Host を返す。
func Detect() *Host {
	return &Host{
		GOOS:     runtime.GOOS,
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
		LookPath: exec.LookPath,
		Stat:     os.Stat,
		HomeDir:  os.UserHomeDir,
		Run:      ExecRunner{},
	}
}

// IsWSL は WSL 上で動いているかを返す。
func (h *Host) IsWSL() bool {
	if h.GOOS != "linux" {
		return false
	}
	if h.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := h.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// WindowsHome は Windows のユーザーホームを WSL 形式のパスで返す。
func (h *Host) WindowsHome(ctx context.Context) (string, error) {
	if h.winHome != "" {
		return h.winHome, nil
	}
	out, err := h.Run.Output(ctx, "cmd.exe", "/c", "echo %USERPROFILE%")
	if err != nil {
		return "", fmt.Errorf("Windows のホームを取得できません: %w", err)
	}
	win := strings.TrimSpace(string(out))
	if win == "" || strings.Contains(win, "%") {
		return "", errors.New("Windows のホームを取得できません: USERPROFILE が空です")
	}
	out, err = h.Run.Output(ctx, "wslpath", "-u", win)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", win, err)
	}
	h.winHome = strings.TrimSpace(string(out))
	return h.winHome, nil
}

var wslconfigMode = regexp.MustCompile(`(?im)^\s*networkingMode\s*=\s*(\w+)`)

// NetworkMode は WSL のネットワークモードを返す。判定できなければ "nat"。
func (h *Host) NetworkMode(ctx context.Context) string {
	if out, err := h.Run.Output(ctx, "wslinfo", "--networking-mode"); err == nil {
		if m := strings.ToLower(strings.TrimSpace(string(out))); m != "" {
			return m
		}
	}
	if home, err := h.WindowsHome(ctx); err == nil {
		if b, err := h.ReadFile(path.Join(home, ".wslconfig")); err == nil {
			if m := wslconfigMode.FindSubmatch(b); m != nil {
				return strings.ToLower(string(m[1]))
			}
		}
	}
	return "nat"
}

// ResolveWSLMode は wsl.mode の設定値から実際のモードを決める。WSL でなければ "" を返す。
func (h *Host) ResolveWSLMode(ctx context.Context, setting string) (string, error) {
	if !h.IsWSL() {
		return "", nil
	}
	switch setting {
	case "", "auto":
		if h.NetworkMode(ctx) == "mirrored" {
			return "mirrored", nil
		}
		return "relay", nil
	case "mirrored", "relay", "native":
		return setting, nil
	}
	return "", fmt.Errorf("wsl.mode は auto / mirrored / relay / native のいずれかです: %q", setting)
}

const windowsSSH = "/mnt/c/Windows/System32/OpenSSH/ssh.exe"

// SSHBinary は ssh の設定値と WSL のモードから、実行する ssh のパスを決める。
func (h *Host) SSHBinary(setting, wslMode string) (string, error) {
	name := setting
	switch setting {
	case "", "auto":
		name = "ssh"
		if h.IsWSL() && wslMode != "native" {
			name = "ssh.exe"
		}
	case "ssh", "ssh.exe":
	default:
		if _, err := h.Stat(setting); err != nil {
			return "", fmt.Errorf("ssh に指定したファイルがありません: %s", setting)
		}
		return setting, nil
	}
	if p, err := h.LookPath(name); err == nil {
		return p, nil
	}
	if name == "ssh.exe" && h.IsWSL() {
		if _, err := h.Stat(windowsSSH); err == nil {
			return windowsSSH, nil
		}
	}
	return "", fmt.Errorf("%s が見つかりません", name)
}

// SSHConfigPath は、そのバイナリが既定で読む ssh config のパスを返す（表示用）。
func (h *Host) SSHConfigPath(ctx context.Context, bin string) (string, error) {
	if h.IsWSL() && strings.HasSuffix(strings.ToLower(bin), ".exe") {
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return "", err
		}
		return path.Join(home, ".ssh", "config"), nil
	}
	home, err := h.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}
