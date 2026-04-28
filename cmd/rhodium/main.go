package main

import (
	"fmt"
	"os"

	"rhodium/internal/cli"
	"rhodium/internal/rhodium"
)

func main() {
	args := os.Args[1:]
	var err error
	if len(args) > 0 {
		err = cli.Run(args)
	} else {
		err = rhodium.Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
