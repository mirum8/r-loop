package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "r-loop %s\n", version)
		return 0
	}
	fmt.Fprintln(stderr, "usage: r-loop <todo.md> [flags] | r-loop --version")
	return 2
}
