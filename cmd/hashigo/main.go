package main

import (
	"context"
	"os"

	"golang.org/x/term"

	"github.com/kukv/hashigo/internal/cli"
	"github.com/kukv/hashigo/internal/platform"
)

func main() {
	d := cli.Deps{
		Host:   &platform.Host{},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		IsTTY:  func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
	}
	os.Exit(cli.Run(context.Background(), d, os.Args[1:]))
}
