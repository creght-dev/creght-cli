package main

import (
	"context"
	"fmt"
	"github.com/creght-dev/creght-cli/internal/cli"
	"os"
)

func main() {
	err := cli.Run(context.Background(), os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
