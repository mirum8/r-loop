package askmcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

const maxBody = 4 << 20

type Server struct {
	RunDir string
	RunID  string
	Store  core.Store
	Seq    int

	mu        sync.Mutex
	ctx       context.Context
	base      string
	prefix    string
	wdPath    string
	wdURL     string
	handlers  WatchdogHandlers
	steps     map[string]core.StepKey
	questions chan core.Question
	open      map[string]core.StepKey
}

type askInput struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

type askOutput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

const askedText = "Question %s is asked: the answer will arrive as your next message; end your turn now and do nothing else until it arrives."

func (s *Server) Serve(ctx context.Context) (string, error) {
	token, err := s.token("token")
	if err != nil {
		return "", err
	}
	wdToken, err := s.token("wd-token")
	if err != nil {
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
	s.wdPath = "/mcp/watchdog/" + wdToken
	s.wdURL = "http://" + ln.Addr().String() + s.wdPath
	s.questions = make(chan core.Question)
	s.open = map[string]core.StepKey{}
	s.steps = map[string]core.StepKey{}
	s.mu.Unlock()

	watchdogServer := s.watchdogServer()
	watchdogHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return watchdogServer }, nil)
	stepHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		key, _ := s.stepKey(r.URL.Path)
		return s.mcpServer(key)
	}, nil)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == s.wdPath {
			watchdogHandler.ServeHTTP(w, r)
			return
		}
		if _, ok := s.stepKey(r.URL.Path); !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if callsWatchdogTool(body) {
				http.NotFound(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		stepHandler.ServeHTTP(w, r)
	})}
	serveHTTP(ctx, srv, ln)
	return s.base, nil
}

func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener) {
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(shutdown) != nil {
			srv.Close()
		}
	}()
}

func newToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Server) token(name string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.RunDir, name), []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) WatchdogURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wdURL
}

func (s *Server) StepURL(key core.StepKey) string {
	path := fmt.Sprintf("/%s/%s/%d", key.Phase, key.Kind, key.Attempt)
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
	if _, ok := s.open[id]; !ok {
		return fmt.Errorf("question %s is not open", id)
	}
	delete(s.open, id)
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
		Name:        "ask_watchdog",
		Description: "Ask the run's watchdog a real choice the repository cannot answer, with the options and your recommendation. Returns at once with the question's id: end your turn then, and the answer arrives as your next message. One open question at a time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, askOutput, error) {
		id, err := s.ask(ctx, key, in)
		if err != nil {
			return nil, askOutput{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(askedText, id)}}}, askOutput{ID: id, Status: "asked"}, nil
	})
	return srv
}

func (s *Server) ask(ctx context.Context, key core.StepKey, in askInput) (string, error) {
	s.mu.Lock()
	for id, k := range s.open {
		if k == key {
			s.mu.Unlock()
			return "", fmt.Errorf("question %s is still open: end your turn now and wait for its answer as your next message", id)
		}
	}
	s.Seq++
	q := core.Question{ID: "q" + strconv.Itoa(s.Seq), Step: key, Text: in.Question, Options: in.Options, Recommended: in.Recommended, AskedAt: time.Now()}
	s.open[q.ID] = key
	out, serverCtx := s.questions, s.ctx
	s.mu.Unlock()

	select {
	case out <- q:
		return q.ID, nil
	case <-serverCtx.Done():
		s.drop(q.ID)
		return "", errors.New("r-loop stopped before the question was delivered")
	case <-ctx.Done():
		s.drop(q.ID)
		return "", ctx.Err()
	}
}

func (s *Server) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.open, id)
}
