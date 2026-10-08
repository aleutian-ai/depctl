// Command depctl is the entry point for the depctl CLI and daemon.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/aleutian-ai/depctl/internal/cli"
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
