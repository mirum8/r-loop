package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ask-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: ask-agent <step url>")
	}
	sentinel := os.Getenv("R_LOOP_SENTINEL")
	if sentinel == "" {
		return fmt.Errorf("R_LOOP_SENTINEL is not set")
	}
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "ask-agent", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: os.Args[1], MaxRetries: -1}, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ask_watchdog", Arguments: map[string]any{
		"question":    "Which database?",
		"options":     []string{"sqlite", "postgres"},
		"recommended": "sqlite",
	}})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("ask_watchdog failed: %v", res.Content)
	}
	out, _ := res.StructuredContent.(map[string]any)
	if out["id"] != "q1" || out["status"] != "asked" {
		return fmt.Errorf("ask_watchdog returned %v", out)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("no answer typed: %w", err)
	}
	_, answer, ok := strings.Cut(strings.TrimSpace(line), "r-loop: answer to q1 (")
	if _, answer, ok = strings.Cut(answer, "): "); !ok {
		return fmt.Errorf("typed %q", line)
	}
	if err := os.WriteFile("answer.txt", []byte(answer), 0o644); err != nil {
		return err
	}
	tmp := sentinel + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf(`{"outcome":"ok","reason":"","at":%q}`, time.Now().UTC().Format(time.RFC3339))), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, sentinel)
}
