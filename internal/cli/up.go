package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
	"github.com/kukv/hashigo/internal/supervisor"
)

func (a *app) upCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "up <対象...>",
		Short: "トンネルを起動する（Ctrl-C で全停止）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			return a.up(ctx, c, args, yes)
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "protected 環境の確認を省略する")
	return c
}

func (a *app) up(ctx context.Context, c *cobra.Command, args []string, yes bool) error {
	cfg, path, err := a.load(ctx)
	if err != nil {
		return err
	}
	tunnels, err := cfg.Select(args)
	if err != nil {
		return err
	}
	if len(tunnels) == 0 {
		return errors.New("起動するトンネルがありません")
	}
	built, mode, err := a.buildUnits(ctx, cfg, tunnels)
	if err != nil {
		return err
	}
	if err := a.confirmProtected(cfg, tunnels, yes); err != nil {
		return err
	}
	if err := checkPorts(tunnels, a.liveRuns()); err != nil {
		return err
	}
	if err := a.preauth(ctx, built); err != nil {
		return err
	}
	units := make([]supervisor.Unit, len(built))
	for i, b := range built {
		units[i] = b.unit()
	}
	w := c.ErrOrStderr()
	sup := &supervisor.Supervisor{Out: w}
	a.attachState(w, sup, path)
	if mode == "relay" {
		go a.warnExposed(ctx, w, built, 5*time.Second)
	}
	runErr := sup.Run(ctx, units)
	a.cleanupWindows(w, built)
	return runErr
}

// confirmProtected は protected の環境を含むとき、起動してよいかを確認する。
func (a *app) confirmProtected(cfg *config.Config, tunnels []*config.Tunnel, yes bool) error {
	set := map[string]bool{}
	for _, t := range tunnels {
		if e := cfg.Envs[t.Env]; e != nil && e.Protected {
			set[t.Env] = true
		}
	}
	if len(set) == 0 || yes {
		return nil
	}
	var envs []string
	for e := range set {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	names := strings.Join(envs, ", ")
	if !a.d.IsTTY() {
		return fmt.Errorf("%s は protected です。端末から実行して確認に答えるか、--yes を付けてください", names)
	}
	_, _ = fmt.Fprintf(a.d.Stderr, "%s は protected です。起動しますか? [y/N] ", names)
	line, _ := bufio.NewReader(a.d.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("起動を取りやめました")
}

// runAlive は状態ファイルの up がまだ動いているかを返す。start_token があればそれで、無ければ started_at で比べる。
func runAlive(r state.Run) bool {
	if r.StartToken != "" {
		return platform.IsAliveToken(r.PID, r.StartToken)
	}
	return platform.IsAlive(r.PID, r.StartedAt)
}

// liveRuns は動いている up の状態ファイルを読む。残骸は削除される。読めなければ空として扱う。
func (a *app) liveRuns() []state.Run {
	dir, err := a.d.Host.StateDir()
	if err != nil {
		return nil
	}
	runs, err := state.List(dir, runAlive)
	if err != nil {
		return nil
	}
	return runs
}

// dialOK は addr に TCP 接続できるかを返す。
func dialOK(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// checkPorts はローカルポートに接続を試し、使用中のものがあればエラーにする。
// 使っている相手は状態ファイルから特定し、特定できなければ hashigo 外のプロセスとする。
func checkPorts(tunnels []*config.Tunnel, runs []state.Run) error {
	var busy []string
	for _, t := range tunnels {
		for _, f := range t.Forwards {
			l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
			if !dialOK(l.DialAddr()) {
				continue
			}
			owner := "hashigo 外のプロセス"
			if r, s, ok := state.FindPort(runs, l.String()); ok {
				owner = fmt.Sprintf("%s（pid %d の up）", s.Tunnel, r.PID)
			}
			busy = append(busy, fmt.Sprintf("%s の %s → %s", t.ID(), l.String(), owner))
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("ローカルポートが使用中のため起動しません:\n  %s", strings.Join(busy, "\n  "))
}

// preauth は、ECS トンネルが使うプロファイルごとに一度だけ sts get-caller-identity を実行する。
// 端末をつなぐので、AssumeRole の MFA コードをここで入力できる。失敗したら起動しない。
func (a *app) preauth(ctx context.Context, built []builtUnit) error {
	seen := map[string]bool{}
	for _, u := range built {
		if u.Type != "ecs" {
			continue
		}
		key := strings.Join(append([]string{u.Bin, u.Profile, u.Region}, u.AWSEnv...), "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		_, _ = fmt.Fprintf(a.d.Stderr, "AWS の認証を確認しています（profile: %s）\n", u.Profile)
		args := []string{"sts", "get-caller-identity", "--profile", u.Profile}
		if u.Region != "" {
			args = append(args, "--region", u.Region)
		}
		r := a.d.Host.Runner(platform.RunOptions{Env: u.AWSEnv, Interactive: true})
		if _, err := r.Output(ctx, u.Bin, args...); err != nil {
			msg := platform.ErrText(err)
			if ecs.IsAuthExpired(msg) {
				return errors.New(ecs.LoginHint(u.Bin, u.Profile))
			}
			return fmt.Errorf("profile %s の認証を確認できませんでした: %s", u.Profile, msg)
		}
	}
	return nil
}

// attachState は状態ファイルを用意する。started_at には、残骸の判定と同じ StartTime の値を書く（R6）。
func (a *app) attachState(w io.Writer, sup *supervisor.Supervisor, configPath string) {
	dir, err := a.d.Host.StateDir()
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: 状態ファイルを使えません: %v\n", err)
		return
	}
	pid := os.Getpid()
	started, err := platform.StartTime(pid)
	if err != nil {
		started = time.Now()
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	sup.State = state.New(dir, pid)
	token, _ := platform.StartToken(pid)
	sup.Base = state.Run{PID: pid, StartedAt: started, StartToken: token, Config: configPath}
}

// warnExposed は relay で起動したあと、Windows 側の待ち受けが LAN に公開されていないかを確かめる。
func (a *app) warnExposed(ctx context.Context, w io.Writer, built []builtUnit, delay time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	var ports []int
	for _, u := range built {
		if a.d.Host.CrossesToWindows(u.Bin) {
			ports = append(ports, u.Ports...)
		}
	}
	if len(ports) == 0 {
		return
	}
	ls, err := a.d.Host.WindowsListeners(ctx, ports)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: Windows 側の待ち受けアドレスを確認できません: %v\n", err)
		return
	}
	for _, l := range ls {
		if l.Exposed() {
			_, _ = fmt.Fprintf(w, "警告: Windows 側でポート %d が %s で待ち受けています（LAN に公開されています）\n", l.Port, l.Addr)
		}
	}
}

// cleanupWindows は、WSL から Windows のバイナリを使ったトンネルについて、
// Windows 側に残ったプロセスを片付ける（仕様 6.5）。
func (a *app) cleanupWindows(w io.Writer, built []builtUnit) {
	var ports []int
	for _, u := range built {
		if a.d.Host.CrossesToWindows(u.Bin) {
			ports = append(ports, u.Ports...)
		}
	}
	if len(ports) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	killed, err := a.d.Host.CleanupWindows(ctx, ports)
	for _, p := range killed {
		_, _ = fmt.Fprintf(w, "Windows 側に残っていた %s (pid %d) を終了しました\n", p.Name, p.PID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: Windows 側の後始末に失敗しました: %v\n", err)
	}
}
