package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/driver/relay"
	"github.com/kukv/hashigo/internal/driver/ssh"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/supervisor"
)

// builtUnit は1トンネル分のドライバと、表示用の情報。
type builtUnit struct {
	ID        string
	Env       string
	Protected bool
	Reconnect bool
	Type      string   // ssh / ecs
	Bin       string   // 実行するバイナリ
	Config    string   // 実際に読む ssh / aws config のパス（表示用）
	Profile   string   // ECS の AWS プロファイル
	Region    string   // ECS のリージョン（空ならプロファイルの値）
	AWSEnv    []string // ECS の aws に渡す環境変数
	Ports     []int    // ローカルポート
	Driver    driver.Driver
}

func (b builtUnit) unit() supervisor.Unit {
	return supervisor.Unit{ID: b.ID, Env: b.Env, Protected: b.Protected, Reconnect: b.Reconnect, Driver: b.Driver}
}

// buildUnits はトンネルごとにドライバを組み立てる。2つ目の戻り値は WSL のモード（WSL でなければ ""）。
func (a *app) buildUnits(ctx context.Context, cfg *config.Config, tunnels []*config.Tunnel) ([]builtUnit, string, error) {
	mode, err := a.d.Host.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		return nil, "", err
	}
	b := &builder{a: a, cfg: cfg, mode: mode}
	units := make([]builtUnit, 0, len(tunnels))
	for _, t := range tunnels {
		eff := cfg.Effective(t)
		u := builtUnit{ID: t.ID(), Env: t.Env, Protected: eff.Protected, Reconnect: eff.Reconnect, Type: t.Type}
		for _, f := range t.Forwards {
			u.Ports = append(u.Ports, f.LocalPort)
		}
		if t.Type == "ecs" {
			err = b.ecs(ctx, &u, t, eff)
		} else {
			err = b.ssh(ctx, &u, t, eff)
		}
		if err != nil {
			return nil, "", err
		}
		units = append(units, u)
	}
	return units, mode, nil
}

type builder struct {
	a     *app
	cfg   *config.Config
	mode  string
	socat string // relay で使う socat のパス（最初に必要になったときに探す）
}

func (b *builder) ssh(ctx context.Context, u *builtUnit, t *config.Tunnel, eff config.Effective) error {
	h := b.a.d.Host
	bin, err := h.SSHBinary(eff.SSH, b.mode)
	if err != nil {
		return fmt.Errorf("%s: %w", t.ID(), err)
	}
	route, err := b.cfg.Route(t)
	if err != nil {
		return err
	}
	arg, display, err := h.SSHConfigFor(ctx, bin, eff.SSHConfig)
	if err != nil {
		if eff.SSHConfig != "auto" {
			return fmt.Errorf("%s: %w", t.ID(), err)
		}
		arg, display = "", "(不明)"
	}
	d := ssh.New(ssh.Options{
		Tunnel: t.ID(), Bin: bin, Route: route, Forwards: t.Forwards, Keepalive: eff.Keepalive, ConfigFile: arg,
	})
	u.Bin, u.Config, u.Driver = bin, display, d
	if b.mode != "relay" || !h.CrossesToWindows(bin) {
		return nil
	}
	if b.socat == "" {
		p, err := h.LookPath("socat")
		if err != nil {
			return errors.New("relay モードには socat が必要です（例: sudo apt install socat）。または .wslconfig で networkingMode=mirrored を有効にしてください")
		}
		b.socat = p
	}
	u.Driver = relay.Wrap(d, relay.Options{Socat: b.socat, Gateway: h.DefaultGateway})
	return nil
}

func (b *builder) ecs(ctx context.Context, u *builtUnit, t *config.Tunnel, eff config.Effective) error {
	h := b.a.d.Host
	if b.mode == "relay" {
		return fmt.Errorf("%s: ECS トンネルは relay モードでは使えません（session-manager-plugin は Windows の 127.0.0.1 でしか待ち受けないため）。.wslconfig で networkingMode=mirrored を有効にしてください", t.ID())
	}
	bin, err := h.AWSBinary(eff.AWS, b.mode)
	if err != nil {
		return fmt.Errorf("%s: %w", t.ID(), err)
	}
	env, display, err := h.AWSConfigFor(ctx, bin, eff.AWSConfig)
	if err != nil {
		if eff.AWSConfig != "auto" {
			return fmt.Errorf("%s: %w", t.ID(), err)
		}
		env, display = nil, "(不明)"
	}
	profile := ecs.ResolveProfile(eff.Profile, h.Getenv)
	u.Bin, u.Config, u.Profile, u.Region, u.AWSEnv = bin, display, profile, eff.Region, env
	u.Driver = ecs.New(ecs.Options{
		Tunnel: t.ID(), Bin: bin, Run: h.Runner(platform.RunOptions{Env: env}), Env: env,
		Profile: profile, Region: eff.Region,
		Cluster: t.Cluster, Service: t.Service, Container: t.Container,
		Forwards: t.Forwards,
	})
	return nil
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
