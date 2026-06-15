package main

import (
	"bysir/creght-cli/internal/cli"
	"context"
	"fmt"
	"os"
)

func main() {
	err := cli.Run(context.Background(), os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
