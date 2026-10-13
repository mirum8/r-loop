package hooktap

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"r-loop/internal/core"
)

const fieldCap = 512

type line struct {
	At        time.Time `json:"at"`
	Agent     string    `json:"agent"`
	Event     string    `json:"event"`
	Tool      string    `json:"tool,omitempty"`
	Target    string    `json:"target,omitempty"`
	Notice    string    `json:"notice,omitempty"`
	Type      string    `json:"type,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	PromptCut bool      `json:"prompt_cut,omitempty"`
}

func Tap(stdin io.Reader, out string, getenv func(string) string) {
	agent := getenv("R_LOOP_AGENT")
	if agent == "" {
		return
	}
	input, _ := io.ReadAll(stdin)
	var hook struct {
		Event     string `json:"hook_event_name"`
		Tool      string `json:"tool_name"`
		Message   string `json:"message"`
		Type      string `json:"notification_type"`
		Prompt    string `json:"prompt"`
		ToolInput struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
			Command      string `json:"command"`
			Pattern      string `json:"pattern"`
			Description  string `json:"description"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(input, &hook) != nil || hook.Event == "" {
		return
	}
	in := hook.ToolInput
	target := in.FilePath
	for _, v := range []string{in.NotebookPath, in.Command, in.Pattern, in.Description} {
		if target == "" {
			target = v
		}
	}
	data, err := json.Marshal(line{At: time.Now(), Agent: capped(agent), Event: capped(hook.Event), Tool: capped(hook.Tool), Target: capped(target), Notice: capped(hook.Message), Type: capped(hook.Type), Prompt: capped(hook.Prompt), PromptCut: len(hook.Prompt) > fieldCap})
	if err != nil {
		return
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

func capped(s string) string {
	if len(s) <= fieldCap {
		return s
	}
	s = s[:fieldCap]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

type Feed struct {
	path    string
	mu      sync.Mutex
	offset  int64
	partial []byte
}

func NewFeed(path string) *Feed {
	f := &Feed{path: path}
	if info, err := os.Stat(path); err == nil {
		f.offset = info.Size()
	}
	return f
}

func (f *Feed) Read() ([]core.HookEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := os.Open(f.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	f.offset += int64(len(data))
	data = append(f.partial, data...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		f.partial = data
		return nil, nil
	}
	f.partial = append([]byte(nil), data[end+1:]...)
	var events []core.HookEvent
	for _, raw := range bytes.Split(data[:end], []byte{'\n'}) {
		var l line
		if json.Unmarshal(raw, &l) != nil || l.Agent == "" || l.Event == "" {
			continue
		}
		events = append(events, core.HookEvent{At: l.At, Agent: l.Agent, Event: l.Event, Tool: l.Tool, Target: l.Target, Notice: l.Notice, Type: l.Type, Prompt: l.Prompt, PromptCut: l.PromptCut})
	}
	return events, nil
}
