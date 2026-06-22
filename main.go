package main

import (
	"fmt"
	"os"

	"github.com/amio/aria2s/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
