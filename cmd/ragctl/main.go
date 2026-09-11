// Command ragctl is the entry point for the ragctl CLI and daemon.
package main

import (
	"errors"
	"fmt"
	"os"

	"aleutian-ai/ragctl/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		var exitErr cli.ExitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
