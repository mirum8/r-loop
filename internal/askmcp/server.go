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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

const maxBody = 4 << 20

type Server struct {
	RunDir    string
	RunID     string
	Store     core.Store
	Seq       int
	KeepAlive time.Duration

	mu        sync.Mutex
	ctx       context.Context
	base      string
	prefix    string
	wdPath    string
	wdURL     string
	handlers  WatchdogHandlers
	steps     map[string]core.StepKey
	questions chan core.Question
	pending   map[string]*pending
}

type pending struct {
	q         core.Question
	seq       int
	delivered bool
	answered  bool
	handed    bool
	waiters   int
	done      chan struct{}
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
	s.pending = map[string]*pending{}
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
	p, ok := s.pending[id]
	switch {
	case !ok:
		return fmt.Errorf("no question %s", id)
	case p.answered:
		return fmt.Errorf("question %s is already answered", id)
	}
	p.answered = true
	p.q.Answer, p.q.AnsweredBy, p.q.Citation, p.q.AnsweredAt = answer, by, citation, time.Now()
	close(p.done)
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
		Description: "Ask the run's watchdog a real choice the repository cannot answer, with the options and your recommendation. Blocks until answered.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, askOutput, error) {
		_, answer, err := s.ask(ctx, key, in, keepAlive(req))
		return nil, askOutput{Answer: answer}, err
	})
	return srv
}

func keepAlive(req *mcp.CallToolRequest) func(context.Context, float64) error {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	return func(ctx context.Context, n float64) error {
		return req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: n, Message: "waiting for the watchdog's answer"})
	}
}

func (s *Server) ask(ctx context.Context, key core.StepKey, in askInput, notify func(context.Context, float64) error) (string, string, error) {
	s.mu.Lock()
	p, serverCtx := s.open(key, in), s.ctx
	if p != nil {
		p.waiters++
		s.mu.Unlock()
		answer, err := s.await(ctx, serverCtx, p, notify)
		return p.q.ID, answer, err
	}
	s.Seq++
	p = &pending{
		q:       core.Question{ID: "q" + strconv.Itoa(s.Seq), Step: key, Text: in.Question, Options: in.Options, Recommended: in.Recommended, AskedAt: time.Now()},
		seq:     s.Seq,
		waiters: 1,
		done:    make(chan struct{}),
	}
	s.pending[p.q.ID] = p
	out := s.questions
	s.mu.Unlock()

	select {
	case out <- p.q:
	case <-serverCtx.Done():
		s.leave(p)
		return p.q.ID, "", errors.New("r-loop stopped before the question was delivered")
	case <-ctx.Done():
		s.leave(p)
		return p.q.ID, "", ctx.Err()
	}
	s.mu.Lock()
	p.delivered = true
	s.mu.Unlock()
	answer, err := s.await(ctx, serverCtx, p, notify)
	return p.q.ID, answer, err
}

func (s *Server) open(key core.StepKey, in askInput) *pending {
	var orphan *pending
	for _, p := range s.pending {
		if !p.delivered || p.q.Step != key {
			continue
		}
		if !p.answered && p.q.Text == in.Question && slices.Equal(p.q.Options, in.Options) {
			return p
		}
		if p.waiters == 0 && !p.handed && (orphan == nil || p.seq < orphan.seq) {
			orphan = p
		}
	}
	return orphan
}

func (s *Server) leave(p *pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.waiters--
}

func (s *Server) await(ctx, serverCtx context.Context, p *pending, notify func(context.Context, float64) error) (string, error) {
	defer s.leave(p)
	var tick <-chan time.Time
	if notify != nil {
		every := s.KeepAlive
		if every <= 0 {
			every = 30 * time.Second
		}
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		tick = ticker.C
	}
	for n := 1; ; n++ {
		select {
		case <-p.done:
			s.mu.Lock()
			defer s.mu.Unlock()
			p.handed = true
			return p.q.Answer, nil
		case <-serverCtx.Done():
			return "", errors.New("r-loop stopped before the question was answered")
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick:
			if err := notify(ctx, float64(n)); err != nil {
				return "", fmt.Errorf("the caller is gone: %w", err)
			}
		}
	}
}
