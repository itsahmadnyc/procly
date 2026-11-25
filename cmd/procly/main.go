package main

import (
	"os"

	"github.com/itsahmadnyc/procly/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
