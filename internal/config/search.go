package config

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/kukv/hashigo/internal/platform"
)

// Find は設定ファイルを探す。順番は --config → カレントの hashigo.yaml →
// $XDG_CONFIG_HOME/hashigo/config.yaml（無ければ ~/.config/hashigo/config.yaml）→
// WSL なら Windows ホームの .config/hashigo/config.yaml。
func Find(ctx context.Context, explicit string, h *platform.Host) (string, error) {
	if explicit != "" {
		if _, err := h.Stat(explicit); err != nil {
			return "", fmt.Errorf("設定ファイルがありません: %s", explicit)
		}
		return explicit, nil
	}
	cands := []string{"hashigo.yaml"}
	if x := h.Getenv("XDG_CONFIG_HOME"); x != "" && h.GOOS != "windows" {
		cands = append(cands, filepath.Join(x, "hashigo", "config.yaml"))
	} else if home, err := h.HomeDir(); err == nil {
		cands = append(cands, filepath.Join(home, ".config", "hashigo", "config.yaml"))
	}
	if h.IsWSL() {
		if wh, err := h.WindowsHome(ctx); err == nil {
			cands = append(cands, path.Join(wh, ".config", "hashigo", "config.yaml"))
		}
	}
	for _, c := range cands {
		if _, err := h.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("設定ファイルが見つかりません（探した場所: %s）", strings.Join(cands, ", "))
}
