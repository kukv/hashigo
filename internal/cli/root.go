// Package cli は hashigo のサブコマンドを定義する。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
)

// Deps はコマンドが使う外部とのつなぎ目。テストで差し替える。
type Deps struct {
	Host      *platform.Host
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	IsTTY     func() bool // stdin が端末か（protected の確認に使う）
	StdoutTTY func() bool // stdout が色を出せる端末か。nil なら端末ではない
}

type app struct {
	d          Deps
	configPath string
}

// Run はコマンドを実行し、終了コードを返す。
func Run(ctx context.Context, d Deps, args []string) int {
	a := &app{d: d}
	root := &cobra.Command{
		Use:           "hashigo",
		Short:         "多段 SSH のポートフォワードをまとめて起動・停止・監視する",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "設定ファイルのパス")
	root.SetArgs(args)
	root.SetIn(d.Stdin)
	root.SetOut(d.Stdout)
	root.SetErr(d.Stderr)
	root.AddCommand(newVersionCmd(), a.validateCmd(), a.cmdCmd(), a.upCmd(), a.statusCmd(), a.doctorCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, errQuiet) {
			printError(d.Stderr, err)
		}
		return 1
	}
	return 0
}

// errQuiet は、結果を出力済みで、終了コード 1 だけを返すときのエラー。
var errQuiet = errors.New("終了コード 1")

func printError(w io.Writer, err error) {
	var multi interface{ Lines() []string }
	if errors.As(err, &multi) {
		for _, l := range multi.Lines() {
			_, _ = fmt.Fprintln(w, l)
		}
		return
	}
	_, _ = fmt.Fprintln(w, "エラー:", err)
}

// load は設定ファイルを探して読み込む。
func (a *app) load(ctx context.Context) (*config.Config, string, error) {
	p, err := config.Find(ctx, a.configPath, a.d.Host)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(p, a.d.Host.GOOS)
	return cfg, p, err
}
