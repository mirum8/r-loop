package askmcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

type Server struct {
	RunDir string
	Seq    int

	mu        sync.Mutex
	ctx       context.Context
	base      string
	prefix    string
	steps     map[string]core.StepKey
	questions chan core.Question
	pending   map[string]*pending
}

type pending struct {
	q        core.Question
	answered bool
	reply    chan string
}

type askInput struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

type askOutput struct {
	Answer string `json:"answer"`
}

func (s *Server) Serve(ctx context.Context) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(filepath.Join(s.RunDir, "token"), []byte(token), 0o600); err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.ctx = ctx
	s.prefix = "/mcp/" + token
	s.base = "http://" + ln.Addr().String() + s.prefix
	s.questions = make(chan core.Question)
	s.pending = map[string]*pending{}
	s.steps = map[string]core.StepKey{}
	s.mu.Unlock()

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		key, _ := s.stepKey(r.URL.Path)
		return s.mcpServer(key)
	}, nil)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.stepKey(r.URL.Path); !ok {
			http.NotFound(w, r)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(shutdown) != nil {
			srv.Close()
		}
	}()
	return s.base, nil
}

func (s *Server) StepURL(key core.StepKey) string {
	path := fmt.Sprintf("/%d/%s/%d", key.Phase, key.Kind, key.Attempt)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps[path] = key
	return s.base + path
}

func (s *Server) Questions() <-chan core.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.questions
}

func (s *Server) Answer(id, answer, by, citation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	switch {
	case !ok:
		return fmt.Errorf("no question %s", id)
	case p.answered:
		return fmt.Errorf("question %s is already answered", id)
	}
	p.answered = true
	p.q.Answer, p.q.AnsweredBy, p.q.Citation, p.q.AnsweredAt = answer, by, citation, time.Now()
	p.reply <- answer
	return nil
}

func (s *Server) stepKey(path string) (core.StepKey, bool) {
	rest, ok := strings.CutPrefix(path, s.prefix)
	if !ok {
		return core.StepKey{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.steps[rest]
	return key, ok
}

func (s *Server) mcpServer(key core.StepKey) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "r-loop", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ask_user",
		Description: "Ask the person running r-loop a question you cannot answer from the repository. Blocks until answered.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, askOutput, error) {
		answer, err := s.ask(ctx, key, in)
		return nil, askOutput{Answer: answer}, err
	})
	return srv
}

func (s *Server) ask(ctx context.Context, key core.StepKey, in askInput) (string, error) {
	s.mu.Lock()
	s.Seq++
	p := &pending{
		q:     core.Question{ID: "q" + strconv.Itoa(s.Seq), Step: key, Text: in.Question, Options: in.Options, Recommended: in.Recommended, AskedAt: time.Now()},
		reply: make(chan string, 1),
	}
	s.pending[p.q.ID] = p
	out, serverCtx := s.questions, s.ctx
	s.mu.Unlock()

	select {
	case out <- p.q:
	case <-serverCtx.Done():
		return "", errors.New("r-loop stopped before the question was delivered")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case answer := <-p.reply:
		return answer, nil
	case <-serverCtx.Done():
		return "", errors.New("r-loop stopped before the question was answered")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
