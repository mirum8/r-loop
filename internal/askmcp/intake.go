package askmcp

import (
	"context"
	"net"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Intake struct {
	Submit func(argv []string) (bool, string)
}

type submitInput struct {
	Argv []string `json:"argv"`
}

func (in *Intake) Serve(ctx context.Context) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	path := "/mcp/intake/" + token
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "r-loop-intake", Version: "1"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "submit_args",
		Description: "Submit the r-loop command line the maintainer confirmed, as argv without the program name: the plan path first, then the flags. A refusal carries the reason; fix the argv and submit again.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args submitInput) (*mcp.CallToolResult, acceptedOutput, error) {
		ok, reason := in.Submit(args.Argv)
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	serveHTTP(ctx, srv, ln)
	return "http://" + ln.Addr().String() + path, nil
}
