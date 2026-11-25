package main

import (
	"os"

	"procly/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
