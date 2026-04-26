package main

import (
	"fmt"
	"os"

	"rhodium/internal/rhodium"
)

func main() {
	if err := rhodium.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
