// Package platform は OS と WSL の差分を引き受ける。
package platform

import (
	"bytes"
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

// RunOptions は外部コマンドの実行方法。
type RunOptions struct {
	Env         []string // 追加する環境変数（KEY=VALUE）
	Interactive bool     // 端末の stdin と stderr をつなぐ（MFA の入力など）
	Combined    bool     // stderr も出力に含める（ssh -V など）
}

// ExecRunner は os/exec で実際に実行する Runner。ゼロ値は stdout だけを返す。
type ExecRunner struct{ Opts RunOptions }

func (r ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(r.Opts.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Opts.Env...)
	}
	if r.Opts.Interactive {
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	}
	if r.Opts.Combined {
		return cmd.CombinedOutput()
	}
	return cmd.Output()
}

// ErrText は外部コマンドのエラーを表示用の文字列にする。stderr が取れていればその中身を返す。
func ErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return strings.TrimSpace(strings.ReplaceAll(string(ee.Stderr), "\r", ""))
	}
	return err.Error()
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
	RunWith  func(RunOptions) Runner // nil なら Run を使う

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
		RunWith:  func(o RunOptions) Runner { return ExecRunner{Opts: o} },
	}
}

// Runner は o の実行方法で動く Runner を返す。RunWith が無ければ Run を返す（テスト用）。
func (h *Host) Runner(o RunOptions) Runner {
	if h.RunWith != nil {
		return h.RunWith(o)
	}
	return h.Run
}

var wslInterop = []string{"/proc/sys/fs/binfmt_misc/WSLInterop", "/proc/sys/fs/binfmt_misc/WSLInterop-late"}

// IsWSL は WSL 上で動いているかを返す。
// WSL 上の Docker コンテナでも /proc/version に microsoft が入るため、/proc/version だけでは判定しない。
func (h *Host) IsWSL() bool {
	if h.GOOS != "linux" {
		return false
	}
	if h.Getenv("WSL_DISTRO_NAME") != "" || h.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, err := h.ReadFile("/proc/version")
	if err != nil || !strings.Contains(strings.ToLower(string(b)), "microsoft") {
		return false
	}
	for _, p := range wslInterop {
		if _, err := h.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// CrossesToWindows は、WSL 上で Windows のバイナリ（.exe）を実行するかを返す。
func (h *Host) CrossesToWindows(bin string) bool {
	return h.IsWSL() && strings.HasSuffix(strings.ToLower(bin), ".exe")
}

// WindowsHome は Windows のユーザーホームを WSL 形式のパスで返す。
func (h *Host) WindowsHome(ctx context.Context) (string, error) {
	if h.winHome != "" {
		return h.winHome, nil
	}
	out, err := h.Run.Output(ctx, "cmd.exe", "/c", "echo %USERPROFILE%")
	if err != nil {
		return "", fmt.Errorf("cmd.exe で Windows のホームを取得できません: %w", err)
	}
	win := strings.TrimSpace(string(out))
	if win == "" || strings.Contains(win, "%") {
		return "", errors.New("cmd.exe で Windows のホームを取得できません: USERPROFILE が空です")
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

const (
	windowsSSH = "/mnt/c/Windows/System32/OpenSSH/ssh.exe"
	windowsAWS = "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"
)

// SSHBinary は ssh の設定値と WSL のモードから、実行する ssh のパスを決める。
func (h *Host) SSHBinary(setting, wslMode string) (string, error) {
	return h.binary("ssh", windowsSSH, setting, wslMode)
}

// AWSBinary は aws の設定値と WSL のモードから、実行する aws のパスを決める。
func (h *Host) AWSBinary(setting, wslMode string) (string, error) {
	return h.binary("aws", windowsAWS, setting, wslMode)
}

// binary は auto なら WSL（native 以外）で <kind>.exe、それ以外で <kind> を PATH から探す。
// PATH に無い .exe は、Windows の既定のインストール先 winDefault を使う。
func (h *Host) binary(kind, winDefault, setting, wslMode string) (string, error) {
	name := setting
	switch setting {
	case "", "auto":
		name = kind
		if h.IsWSL() && wslMode != "native" {
			name = kind + ".exe"
		}
	case kind, kind + ".exe":
	default:
		if _, err := h.Stat(setting); err != nil {
			return "", fmt.Errorf("%s に指定したファイルがありません: %s", kind, setting)
		}
		return setting, nil
	}
	if p, err := h.LookPath(name); err == nil {
		return p, nil
	}
	if name == kind+".exe" && h.IsWSL() {
		if _, err := h.Stat(winDefault); err == nil {
			return winDefault, nil
		}
	}
	return "", fmt.Errorf("%s が見つかりません", name)
}

// SSHConfigPath は、そのバイナリが既定で読む ssh config のパスを返す（表示用）。
func (h *Host) SSHConfigPath(ctx context.Context, bin string) (string, error) {
	if h.CrossesToWindows(bin) {
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

// StateDir は状態ファイルを置くディレクトリを返す。
func (h *Host) StateDir() (string, error) {
	if h.GOOS == "windows" {
		d := h.Getenv("LOCALAPPDATA")
		if d == "" {
			return "", errors.New("LOCALAPPDATA が設定されていないため、状態ファイルの場所を決められません")
		}
		return filepath.Join(d, "hashigo", "run"), nil
	}
	if d := h.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hashigo", "run"), nil
	}
	home, err := h.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "hashigo", "run"), nil
}
