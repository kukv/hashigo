package platform

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ToWindowsPath は WSL のパスを Windows のパス（UNC を含む）に変換する。
func (h *Host) ToWindowsPath(ctx context.Context, p string) (string, error) {
	out, err := h.Run.Output(ctx, "wslpath", "-w", p)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", p, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ToWSLPath は Windows のパスを WSL のパスに変換する。
func (h *Host) ToWSLPath(ctx context.Context, p string) (string, error) {
	out, err := h.Run.Output(ctx, "wslpath", "-u", p)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", p, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SSHConfigFor は ssh_config の設定値から、-F に渡す値（空なら付けない）と表示用のパスを返す。
// 対応は仕様 6.3 の表のとおり。auto は実行バイナリと同じ側の config を使う。
func (h *Host) SSHConfigFor(ctx context.Context, bin, setting string) (arg, display string, err error) {
	switch setting {
	case "", "auto":
		p, err := h.SSHConfigPath(ctx, bin)
		return "", p, err
	case "windows":
		if h.GOOS == "windows" {
			p, err := h.SSHConfigPath(ctx, bin)
			return "", p, err
		}
		if !h.IsWSL() {
			return "", "", errors.New("ssh_config: windows は WSL か Windows でだけ使えます")
		}
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return "", "", err
		}
		p := path.Join(home, ".ssh", "config")
		if h.CrossesToWindows(bin) {
			return "", p, nil
		}
		return p, p, nil
	case "wsl":
		if !h.IsWSL() {
			return "", "", errors.New("ssh_config: wsl は WSL でだけ使えます")
		}
		home, err := h.HomeDir()
		if err != nil {
			return "", "", err
		}
		p := path.Join(home, ".ssh", "config") // WSL 側は Linux なので / 区切り
		if !h.CrossesToWindows(bin) {
			return "", p, nil
		}
		w, err := h.ToWindowsPath(ctx, p)
		return w, w, err
	}
	if h.CrossesToWindows(bin) {
		w, err := h.ToWindowsPath(ctx, setting)
		return w, w, err
	}
	return setting, setting, nil
}

// AWSConfigFor は aws_config の設定値から、aws に渡す環境変数と表示用のパスを返す。
// 対応は仕様 6.3 の表のとおり。aws.exe に WSL のパスを渡すときは WSLENV の /p で変換させる。
func (h *Host) AWSConfigFor(ctx context.Context, bin, setting string) (env []string, display string, err error) {
	switch setting {
	case "", "auto":
		if h.CrossesToWindows(bin) {
			home, err := h.WindowsHome(ctx)
			return nil, path.Join(home, ".aws", "config"), err
		}
		if p := h.Getenv("AWS_CONFIG_FILE"); p != "" {
			return nil, p, nil
		}
		home, err := h.HomeDir()
		return nil, filepath.Join(home, ".aws", "config"), err
	case "windows":
		if h.GOOS == "windows" {
			home, err := h.HomeDir()
			return nil, filepath.Join(home, ".aws", "config"), err
		}
		if !h.IsWSL() {
			return nil, "", errors.New("aws_config: windows は WSL か Windows でだけ使えます")
		}
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return nil, "", err
		}
		cfg := path.Join(home, ".aws", "config")
		if h.CrossesToWindows(bin) {
			return nil, cfg, nil
		}
		return []string{
			"AWS_CONFIG_FILE=" + cfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + path.Join(home, ".aws", "credentials"),
		}, cfg, nil
	case "wsl":
		if !h.IsWSL() {
			return nil, "", errors.New("aws_config: wsl は WSL でだけ使えます")
		}
		home, err := h.HomeDir()
		if err != nil {
			return nil, "", err
		}
		cfg := path.Join(home, ".aws", "config") // WSL 側は Linux なので / 区切り
		if !h.CrossesToWindows(bin) {
			return nil, cfg, nil
		}
		return []string{
			"AWS_CONFIG_FILE=" + cfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + path.Join(home, ".aws", "credentials"),
			"WSLENV=" + appendWSLENV(h.Getenv("WSLENV"), "AWS_CONFIG_FILE/p", "AWS_SHARED_CREDENTIALS_FILE/p"),
		}, cfg, nil
	}
	if h.CrossesToWindows(bin) {
		w, err := h.ToWindowsPath(ctx, setting)
		if err != nil {
			return nil, "", err
		}
		return []string{"AWS_CONFIG_FILE=" + w, "WSLENV=" + appendWSLENV(h.Getenv("WSLENV"), "AWS_CONFIG_FILE")}, w, nil
	}
	return []string{"AWS_CONFIG_FILE=" + setting}, setting, nil
}

// appendWSLENV は既存の WSLENV の後ろに項目を足す。
func appendWSLENV(cur string, items ...string) string {
	if cur == "" {
		return strings.Join(items, ":")
	}
	return cur + ":" + strings.Join(items, ":")
}
