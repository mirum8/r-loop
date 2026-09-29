package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"r-loop/internal/app"
	"r-loop/internal/quota"
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
	if len(args) > 0 && args[0] == "statusline-tap" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, "r-loop: usage: r-loop statusline-tap <file>")
			return 2
		}
		quota.Tap(os.Stdin, args[1], claudeSettings(), stdout)
		return 0
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "r-loop: %v\n", err)
		return 2
	}
	home, _ := os.UserHomeDir()
	return app.Main(args, app.Env{Dir: dir, Home: home, Herdr: "herdr", Git: "git", Pane: os.Getenv("HERDR_PANE_ID"), PID: os.Getpid(), Stdin: os.Stdin, Stdout: stdout, Stderr: stderr, Now: time.Now})
}

func claudeSettings() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}
