package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) cmdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cmd <対象...>",
		Short: "生成される ssh / aws コマンドを表示する（接続はしない）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, _, err := a.load(ctx)
			if err != nil {
				return err
			}
			tunnels, err := cfg.Select(args)
			if err != nil {
				return err
			}
			units, _, err := a.buildUnits(ctx, cfg, tunnels)
			if err != nil {
				return err
			}
			w := c.OutOrStdout()
			_, _ = fmt.Fprintf(w, "# 実行環境: %s\n", a.describeEnv(ctx, cfg))
			for _, u := range units {
				if u.Type == "ecs" {
					_, _ = fmt.Fprintf(w, "# %s  aws=%s  aws_config=%s  profile=%s\n", u.ID, u.Bin, u.Config, u.Profile)
					if len(u.AWSEnv) > 0 {
						_, _ = fmt.Fprintf(w, "# 環境変数: %s\n", strings.Join(u.AWSEnv, " "))
					}
					if r, ok := u.Driver.(interface{ Resolvers() [][]string }); ok {
						_, _ = fmt.Fprintln(w, "# <task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します")
						for _, args := range r.Resolvers() {
							_, _ = fmt.Fprintln(w, shellJoin(args))
						}
					}
				} else {
					_, _ = fmt.Fprintf(w, "# %s  ssh=%s  ssh_config=%s\n", u.ID, u.Bin, u.Config)
				}
				for _, sp := range u.Driver.Preview() {
					_, _ = fmt.Fprintln(w, shellJoin(append([]string{sp.Path}, sp.Args...)))
				}
			}
			return nil
		},
	}
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellJoin は引数をシェルに貼り付けられる形でつなぐ。
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, s := range args {
		if safeArg.MatchString(s) {
			quoted[i] = s
		} else {
			quoted[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
