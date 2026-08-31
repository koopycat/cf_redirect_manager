package main

import (
	"fmt"
	"os"

	"github.com/koopycat/cf-redirect/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
