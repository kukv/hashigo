package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "設定ファイルの構文と参照を検証する",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, p, err := a.load(c.Context())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(c.OutOrStdout(), "OK:", p)
			return nil
		},
	}
}
