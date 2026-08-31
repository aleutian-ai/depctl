// Command ragctl is the entry point for the ragctl CLI and daemon.
package main

import (
	"fmt"
	"os"

	"aleutian-ai/ragctl/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
