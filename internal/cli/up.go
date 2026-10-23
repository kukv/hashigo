package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
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
	cfg, _, err := a.load(ctx)
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
	built, _, err := a.buildUnits(ctx, cfg, tunnels)
	if err != nil {
		return err
	}
	if err := a.confirmProtected(cfg, tunnels, yes); err != nil {
		return err
	}
	if err := checkPorts(tunnels); err != nil {
		return err
	}
	units := make([]supervisor.Unit, len(built))
	for i, b := range built {
		units[i] = b.unit()
	}
	sup := &supervisor.Supervisor{Out: c.ErrOrStderr()}
	return sup.Run(ctx, units)
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

// checkPorts はローカルポートに接続を試し、使用中のものがあればエラーにする。
func checkPorts(tunnels []*config.Tunnel) error {
	var busy []string
	for _, t := range tunnels {
		for _, f := range t.Forwards {
			addr := net.JoinHostPort(dialHost(f.Bind), strconv.Itoa(f.LocalPort))
			conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
			if err != nil {
				continue
			}
			_ = conn.Close()
			busy = append(busy, fmt.Sprintf("%s の %s", t.ID(), addr))
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("ローカルポートが使用中のため起動しません（別の hashigo up か、hashigo 外のプロセスが使っています）:\n  %s",
		strings.Join(busy, "\n  "))
}

func dialHost(bind string) string {
	switch bind {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	}
	return bind
}
