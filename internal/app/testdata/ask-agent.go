package main

import (
	"context"
	"fmt"
	"os"
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ask_user", Arguments: map[string]any{
		"question":    "Which database?",
		"options":     []string{"sqlite", "postgres"},
		"recommended": "sqlite",
	}})
	if err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("ask_user failed: %v", res.Content)
	}
	out, _ := res.StructuredContent.(map[string]any)
	answer, _ := out["answer"].(string)
	if err := os.WriteFile("answer.txt", []byte(answer), 0o644); err != nil {
		return err
	}
	tmp := sentinel + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf(`{"outcome":"ok","reason":"","at":%q}`, time.Now().UTC().Format(time.RFC3339))), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, sentinel)
}
