package quota

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Window struct {
	Label   string
	Percent float64
	Resets  time.Time
}

type Limits struct {
	Provider string
	Windows  []Window
}

const scannedRollouts = 20

func label(minutes int) string {
	switch {
	case minutes > 0 && minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + "d"
	case minutes > 0 && minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "h"
	default:
		return strconv.Itoa(minutes) + "m"
	}
}

func window(minutes int, percent float64, resets int64, now time.Time) Window {
	w := Window{Label: label(minutes), Percent: percent}
	if resets > 0 {
		w.Resets = time.Unix(resets, 0)
		if !w.Resets.After(now) {
			w.Percent = 0
		}
	}
	return w
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

func Codex(root string, now time.Time) (Limits, bool) {
	files, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	slices.SortFunc(files, func(a, b string) int { return strings.Compare(filepath.Base(b), filepath.Base(a)) })
	files = files[:min(len(files), scannedRollouts)]
	modified := map[string]time.Time{}
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			modified[f] = info.ModTime()
		}
	}
	slices.SortStableFunc(files, func(a, b string) int { return modified[b].Compare(modified[a]) })
	for _, f := range files {
		if l, ok := codexFile(f, now); ok {
			return l, true
		}
	}
	return Limits{}, false
}

func codexFile(path string, now time.Time) (Limits, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Limits{}, false
	}
	defer f.Close()
	var last []*codexWindow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), []byte(`"rate_limits"`)) {
			continue
		}
		var line codexLine
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Payload.Type != "token_count" || line.Payload.RateLimits == nil {
			continue
		}
		last = []*codexWindow{line.Payload.RateLimits.Primary, line.Payload.RateLimits.Secondary}
	}
	var ws []codexWindow
	for _, w := range last {
		if w != nil {
			ws = append(ws, *w)
		}
	}
	if len(ws) == 0 {
		return Limits{}, false
	}
	slices.SortFunc(ws, func(a, b codexWindow) int { return a.WindowMinutes - b.WindowMinutes })
	l := Limits{Provider: "codex"}
	for _, w := range ws {
		l.Windows = append(l.Windows, window(w.WindowMinutes, w.UsedPercent, w.ResetsAt, now))
	}
	return l, true
}

type claudeWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       int64    `json:"resets_at"`
}

type claudeLimits struct {
	FiveHour *claudeWindow `json:"five_hour"`
	SevenDay *claudeWindow `json:"seven_day"`
}

func Claude(file string, now time.Time) (Limits, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Limits{}, false
	}
	var rl claudeLimits
	if json.Unmarshal(data, &rl) != nil {
		return Limits{}, false
	}
	l := Limits{Provider: "claude"}
	for _, w := range []struct {
		minutes int
		w       *claudeWindow
	}{{300, rl.FiveHour}, {10080, rl.SevenDay}} {
		if w.w != nil && w.w.UsedPercentage != nil {
			l.Windows = append(l.Windows, window(w.minutes, *w.w.UsedPercentage, w.w.ResetsAt, now))
		}
	}
	return l, len(l.Windows) > 0
}

func Tap(stdin io.Reader, out, userSettings string, stdout io.Writer) {
	input, _ := io.ReadAll(stdin)
	var status struct {
		RateLimits json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(input, &status) == nil && len(status.RateLimits) > 0 && string(status.RateLimits) != "null" {
		save(out, status.RateLimits)
	}
	var settings struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	data, err := os.ReadFile(userSettings)
	if err != nil || json.Unmarshal(data, &settings) != nil || settings.StatusLine.Command == "" {
		return
	}
	cmd := exec.Command("sh", "-c", settings.StatusLine.Command)
	cmd.Stdin, cmd.Stdout = bytes.NewReader(input), stdout
	_ = cmd.Run()
}

func save(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-*.json")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}
