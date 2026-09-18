package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"r-loop/internal/app"
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
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "r-loop: %v\n", err)
		return 2
	}
	home, _ := os.UserHomeDir()
	return app.Main(args, app.Env{Dir: dir, Home: home, Herdr: "herdr", Git: "git", PID: os.Getpid(), Stdin: os.Stdin, Stdout: stdout, Stderr: stderr, Now: time.Now})
}
