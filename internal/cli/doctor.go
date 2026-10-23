package cli

import (
	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/doctor"
)

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [対象...]",
		Short: "実行環境と設定の前提条件を確かめる（✗ があれば終了コード 1）",
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, _, err := a.load(ctx)
			if err != nil {
				return err
			}
			tunnels := cfg.Tunnels()
			if len(args) > 0 {
				if tunnels, err = cfg.Select(args); err != nil {
					return err
				}
			}
			rs := doctor.Run(ctx, doctor.Env{Host: a.d.Host, Config: cfg, Tunnels: tunnels, Runs: a.liveRuns(), Dial: dialOK})
			doctor.Print(c.OutOrStdout(), rs)
			if doctor.HasFail(rs) {
				return errQuiet
			}
			return nil
		},
	}
}
