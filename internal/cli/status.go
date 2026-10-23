package cli

import (
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/state"
)

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [対象...]",
		Short: "トンネルの状態を表示する（全部 up なら終了コード 0）",
		RunE: func(c *cobra.Command, args []string) error {
			cfg, _, err := a.load(c.Context())
			if err != nil {
				return err
			}
			tunnels := cfg.Tunnels()
			if len(args) > 0 {
				if tunnels, err = cfg.Select(args); err != nil {
					return err
				}
			}
			rows := statusRows(cfg, tunnels, a.liveRuns(), dialOK, time.Now())
			color := a.d.StdoutTTY != nil && a.d.StdoutTTY() && a.d.Host.Getenv("NO_COLOR") == ""
			cells := make([][]cell, len(rows))
			allUp := true
			for i, r := range rows {
				env := r.Env
				if env == "" {
					env = "-"
				}
				envCell := cell{Text: env}
				if color && r.Env != "" {
					envCell.Color = envColor(r.Env, r.Protected)
				}
				cells[i] = []cell{envCell, {Text: r.Tunnel}, {Text: r.Ports}, {Text: r.State}, {Text: strconv.Itoa(r.Reconnects)}, {Text: r.LastError}}
				allUp = allUp && r.State == "up"
			}
			renderTable(c.OutOrStdout(), []string{"環境", "トンネル", "ポート", "状態", "再接続", "最終エラー"}, cells)
			if !allUp {
				return errQuiet
			}
			return nil
		},
	}
}

// statusRow は status の1行。
type statusRow struct {
	Env        string
	Protected  bool
	Tunnel     string
	Ports      string
	State      string
	Reconnects int
	LastError  string
}

// statusRows は、状態ファイルがあるトンネルはスロットごと、無いトンネルはトンネルごとに1行を作る（R8）。
func statusRows(cfg *config.Config, tunnels []*config.Tunnel, runs []state.Run, dial func(string) bool, now time.Time) []statusRow {
	var rows []statusRow
	for _, t := range tunnels {
		e := cfg.Envs[t.Env]
		base := statusRow{Env: t.Env, Protected: e != nil && e.Protected, Tunnel: t.Name}
		slots := slotsOf(runs, t.ID())
		if len(slots) == 0 {
			var ports []string
			open := false
			for _, f := range t.Forwards {
				l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
				ports = append(ports, l.String())
				open = open || dial(l.DialAddr())
			}
			r := base
			r.Ports, r.State = strings.Join(ports, ","), "停止"
			if open {
				r.State = "hashigo 外のプロセスが使用中"
			}
			rows = append(rows, r)
			continue
		}
		for _, s := range slots {
			r := base
			r.Ports, r.State, r.Reconnects, r.LastError = strings.Join(s.Ports, ","), slotState(s, dial, now), s.Reconnects, oneLine(s.LastError)
			rows = append(rows, r)
		}
	}
	return rows
}

// slotsOf は id のトンネルのスロットを返す。ローカルポートは一意なので、見つかる up は1つだけ。
func slotsOf(runs []state.Run, id string) []state.Slot {
	for _, r := range runs {
		var out []state.Slot
		for _, s := range r.Slots {
			if s.Tunnel == id {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func slotState(s state.Slot, dial func(string) bool, now time.Time) string {
	ok := len(s.Ports) > 0
	for _, p := range s.Ports {
		ok = ok && dial(dialAddrOf(p))
	}
	if ok {
		return "up"
	}
	switch s.State {
	case state.Backoff:
		if s.NextRetryAt == nil {
			return "backoff"
		}
		secs := max(0, int(math.Ceil(s.NextRetryAt.Sub(now).Seconds())))
		return fmt.Sprintf("backoff (次回 %ds 後)", secs)
	case state.Failed:
		return "failed: " + oneLine(s.LastError)
	case state.Starting:
		return "starting"
	case state.Stopped:
		return "停止"
	}
	return "unhealthy"
}

// dialAddrOf は状態ファイルのポート（127.0.0.1:15432 の形）を、接続の確認に使うアドレスにする。
func dialAddrOf(p string) string {
	host, port, err := net.SplitHostPort(p)
	if err != nil {
		return p
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return p
	}
	return driver.Listen{Addr: host, Port: n}.DialAddr()
}

// cell は表の1マス。Color は ANSI の SGR（"1;31" など）。空なら色なし。
type cell struct {
	Text  string
	Color string
}

// renderTable は列を表示幅でそろえて書く。最後の列は詰めない。
func renderTable(w io.Writer, header []string, rows [][]cell) {
	all := make([][]cell, 0, len(rows)+1)
	hc := make([]cell, len(header))
	for i, h := range header {
		hc[i] = cell{Text: h}
	}
	all = append(all, hc)
	all = append(all, rows...)
	widths := make([]int, len(header))
	for _, r := range all {
		for i, c := range r {
			widths[i] = max(widths[i], dispWidth(c.Text))
		}
	}
	for _, r := range all {
		var b strings.Builder
		for i, c := range r {
			text := c.Text
			if c.Color != "" {
				text = "\x1b[" + c.Color + "m" + text + "\x1b[0m"
			}
			b.WriteString(text)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-dispWidth(c.Text)+2))
			}
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// dispWidth は端末での表示幅を返す。全角の文字は 2 と数える。
func dispWidth(s string) int {
	n := 0
	for _, r := range s {
		if isWide(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || r >= 0x20000
}

var envPalette = []string{"32", "33", "34", "35", "36", "92", "93", "94", "95", "96"}

// envColor は環境名から色を決める。protected の環境は赤の太字にする。
func envColor(name string, protected bool) string {
	if protected {
		return "1;31"
	}
	sum := 0
	for _, b := range []byte(name) {
		sum = (sum*31 + int(b)) % 1000003
	}
	return envPalette[sum%len(envPalette)]
}

// oneLine は空白の連続（改行を含む）を 1 つの空白にまとめ、表の 1 行に収める。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
