package main

import (
	"fmt"
	"os"

	"github.com/amio/aria2s/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		if !cmd.IsReportedFailure(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}
