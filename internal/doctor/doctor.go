// Package doctor は、実行環境と設定が hashigo の前提を満たしているかを確かめる（仕様 9 の doctor）。
package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

// Level は確認の結果。
type Level int

// 結果の種類。NA は「対象外」。
const (
	NA Level = iota
	OK
	Warn
	Fail
)

// Result は確認項目1つの結果。
type Result struct {
	Level Level
	Title string
	Lines []string
}

// line は詳細を1行足し、l が今の結果より悪ければ結果を l にする。
func (r *Result) line(l Level, format string, args ...any) {
	r.Lines = append(r.Lines, fmt.Sprintf(format, args...))
	if l > r.Level {
		r.Level = l
	}
}

func ok(title string) Result { return Result{Level: OK, Title: title} }

func na(title string) Result { return Result{Level: NA, Title: title} }

// Env は確認に使う入力。
type Env struct {
	Host    *platform.Host
	Config  *config.Config
	Tunnels []*config.Tunnel // 確認の対象
	Runs    []state.Run      // 動いている up
	Dial    func(addr string) bool
}

type checker struct {
	ctx     context.Context
	e       Env
	mode    string // WSL のモード（WSL でなければ ""）
	modeErr error
}

// Run は確認項目を仕様の番号順に実行する。
func Run(ctx context.Context, e Env) []Result {
	c := &checker{ctx: ctx, e: e}
	c.mode, c.modeErr = e.Host.ResolveWSLMode(ctx, e.Config.WSLMode)
	return []Result{
		c.platform(),    // 1
		c.ssh(),         // 2
		c.aws(),         // 3
		c.socat(),       // 5
		c.ports(),       // 6
		c.configPaths(), // 7
		c.ecsRelay(),    // 11
	}
}

var marks = map[Level]string{OK: "✓", Warn: "⚠", Fail: "✗"}

// Print は結果を1項目ずつ表示する。詳細は4字下げで続ける。
func Print(w io.Writer, rs []Result) {
	for _, r := range rs {
		if r.Level == NA {
			_, _ = fmt.Fprintf(w, "- %s: 対象外\n", r.Title)
		} else {
			_, _ = fmt.Fprintf(w, "%s %s\n", marks[r.Level], r.Title)
		}
		for _, l := range r.Lines {
			_, _ = fmt.Fprintf(w, "    %s\n", l)
		}
	}
}

// HasFail は ✗ が1つでもあるかを返す。
func HasFail(rs []Result) bool {
	for _, r := range rs {
		if r.Level == Fail {
			return true
		}
	}
	return false
}

func (c *checker) tunnels(typ string) []*config.Tunnel {
	var out []*config.Tunnel
	for _, t := range c.e.Tunnels {
		if t.Type == typ {
			out = append(out, t)
		}
	}
	return out
}

// version は bin arg を実行し、出力の1行目を返す。ssh -V は stderr に出すので stderr も取り込む。
func (c *checker) version(bin, arg string) (Level, string) {
	out, err := c.e.Host.Runner(platform.RunOptions{Combined: true}).Output(c.ctx, bin, arg)
	if err != nil {
		return Fail, fmt.Sprintf("%s: %s を実行できません: %s", bin, arg, platform.ErrText(err))
	}
	return OK, fmt.Sprintf("%s: %s", bin, firstLine(out))
}

func firstLine(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r", "")
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
