// Package cli は hashigo のサブコマンドを定義する。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/platform"
)

// Deps はコマンドが使う外部とのつなぎ目。テストで差し替える。
type Deps struct {
	Host   *platform.Host
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	IsTTY  func() bool
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
	root.AddCommand(newVersionCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		printError(d.Stderr, err)
		return 1
	}
	return 0
}

func printError(w io.Writer, err error) {
	var multi interface{ Lines() []string }
	if errors.As(err, &multi) {
		for _, l := range multi.Lines() {
			fmt.Fprintln(w, l)
		}
		return
	}
	fmt.Fprintln(w, "エラー:", err)
}
