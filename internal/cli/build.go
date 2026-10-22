package cli

import (
	"context"
	"fmt"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ssh"
)

// builtUnit は1トンネル分のドライバと、表示用の情報。
type builtUnit struct {
	ID        string
	Bin       string
	SSHConfig string
	Driver    driver.Driver
}

// buildUnits はトンネルごとにドライバを組み立てる。MVP で未対応のものはここでエラーにする。
func (a *app) buildUnits(ctx context.Context, cfg *config.Config, tunnels []*config.Tunnel) ([]builtUnit, error) {
	h := a.d.Host
	mode, err := h.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		return nil, err
	}
	if mode == "relay" {
		return nil, fmt.Errorf("relay モードは安定化フェーズで対応します。.wslconfig で networkingMode=mirrored を有効にするか、wsl.mode: native を指定してください")
	}
	var units []builtUnit
	for _, t := range tunnels {
		if t.Type == "ecs" {
			return nil, fmt.Errorf("%s: type: ecs は安定化フェーズで対応します", t.ID())
		}
		eff := cfg.Effective(t)
		if eff.SSHConfig != "auto" {
			return nil, fmt.Errorf("%s: ssh_config の切り替えは安定化フェーズで対応します（現在は auto のみ）", t.ID())
		}
		bin, err := h.SSHBinary(eff.SSH, mode)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.ID(), err)
		}
		route, err := cfg.Route(t)
		if err != nil {
			return nil, err
		}
		sshConfig, err := h.SSHConfigPath(ctx, bin)
		if err != nil {
			sshConfig = "(不明)"
		}
		units = append(units, builtUnit{
			ID:        t.ID(),
			Bin:       bin,
			SSHConfig: sshConfig,
			Driver: ssh.New(ssh.Options{
				Tunnel: t.ID(), Bin: bin, Route: route, Forwards: t.Forwards, Keepalive: eff.Keepalive,
			}),
		})
	}
	return units, nil
}

func (a *app) describeEnv(ctx context.Context, cfg *config.Config) string {
	h := a.d.Host
	if !h.IsWSL() {
		return h.GOOS
	}
	mode, err := h.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		mode = "不明"
	}
	return fmt.Sprintf("%s (WSL, %s)", h.GOOS, mode)
}
