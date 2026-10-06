package analyze

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"r-loop/internal/core"
)

const maxFindings = 50

type Hit struct {
	Tool, Rule, Level, Message, Path string
	Line                             int
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
	Index     *int   `json:"index"`
}

type sarifRule struct {
	ID                   string `json:"id"`
	DefaultConfiguration struct {
		Level string `json:"level"`
	} `json:"defaultConfiguration"`
	MessageStrings map[string]sarifText `json:"messageStrings"`
}

type sarifResult struct {
	RuleID    string `json:"ruleId"`
	RuleIndex *int   `json:"ruleIndex"`
	Rule      struct {
		ID    string `json:"id"`
		Index *int   `json:"index"`
	} `json:"rule"`
	Level   string `json:"level"`
	Message struct {
		Text      string   `json:"text"`
		Markdown  string   `json:"markdown"`
		ID        string   `json:"id"`
		Arguments []string `json:"arguments"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
			Region           struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
}

type sarifRun struct {
	BaseIDs map[string]struct {
		URI string `json:"uri"`
	} `json:"originalUriBaseIds"`
	Artifacts []struct {
		Location sarifArtifactLocation `json:"location"`
	} `json:"artifacts"`
	Tool struct {
		Driver struct {
			Rules                []sarifRule          `json:"rules"`
			GlobalMessageStrings map[string]sarifText `json:"globalMessageStrings"`
		} `json:"driver"`
	} `json:"tool"`
	Results []sarifResult `json:"results"`
}

func ParseSARIF(tool, base string, data []byte) ([]Hit, error) {
	var doc struct {
		Runs []sarifRun `json:"runs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: sarif: %w", tool, err)
	}
	var hits []Hit
	for _, run := range doc.Runs {
		for _, res := range run.Results {
			hits = append(hits, run.hit(tool, base, res))
		}
	}
	return hits, nil
}

func (run sarifRun) hit(tool, base string, res sarifResult) Hit {
	rules := run.Tool.Driver.Rules
	index := res.RuleIndex
	if index == nil {
		index = res.Rule.Index
	}
	var rule *sarifRule
	if index != nil && *index >= 0 && *index < len(rules) {
		rule = &rules[*index]
	}
	h := Hit{Tool: tool, Rule: firstSet(res.RuleID, res.Rule.ID)}
	if h.Rule == "" && rule != nil {
		h.Rule = rule.ID
	}
	if h.Rule == "" {
		h.Rule = "unknown"
	}
	if rule == nil {
		for i := range rules {
			if rules[i].ID == h.Rule {
				rule = &rules[i]
				break
			}
		}
	}
	h.Level = res.Level
	if h.Level == "" && rule != nil {
		h.Level = rule.DefaultConfiguration.Level
	}
	if h.Level == "" {
		h.Level = "warning"
	}
	h.Message = firstSet(res.Message.Text, res.Message.Markdown, run.messageString(rule, res.Message.ID, res.Message.Arguments), "(no message)")
	if len(res.Locations) > 0 {
		loc := res.Locations[0].PhysicalLocation
		h.Path = run.path(base, loc.ArtifactLocation)
		h.Line = loc.Region.StartLine
	}
	return h
}

func (run sarifRun) messageString(rule *sarifRule, id string, args []string) string {
	if id == "" {
		return ""
	}
	text, ok := "", false
	if rule != nil {
		var s sarifText
		if s, ok = rule.MessageStrings[id]; ok {
			text = s.Text
		}
	}
	if !ok {
		text = run.Tool.Driver.GlobalMessageStrings[id].Text
	}
	if text == "" {
		return ""
	}
	pairs := []string{"{{", "{", "}}", "}"}
	for i, a := range args {
		pairs = append(pairs, "{"+strconv.Itoa(i)+"}", a)
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func (run sarifRun) path(base string, loc sarifArtifactLocation) string {
	if loc.URI == "" && loc.Index != nil && *loc.Index >= 0 && *loc.Index < len(run.Artifacts) {
		loc = run.Artifacts[*loc.Index].Location
	}
	if loc.URI == "" {
		return ""
	}
	if strings.HasPrefix(loc.URI, "file:") {
		if u, err := url.Parse(loc.URI); err == nil {
			return u.Path
		}
	}
	p, err := url.PathUnescape(loc.URI)
	if err != nil {
		p = loc.URI
	}
	if b, ok := run.BaseIDs[loc.URIBaseID]; ok && loc.URIBaseID != "" && strings.HasPrefix(b.URI, "file:") {
		if u, err := url.Parse(b.URI); err == nil {
			return filepath.Join(u.Path, filepath.FromSlash(p))
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, filepath.FromSlash(p))
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var severity = map[string]int{"error": 0, "warning": 1, "note": 2}

func rank(level string) int {
	if r, ok := severity[level]; ok {
		return r
	}
	return 3
}

type kept struct {
	Hit
	rel string
}

func Findings(dir string, ch Changes, hits []Hit) []core.Finding {
	var keep []kept
	for _, h := range hits {
		rel := filepath.ToSlash(h.Path)
		if h.Path != "" {
			if r, err := filepath.Rel(dir, h.Path); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		if h.Tool == "govulncheck" || ch.Has(rel, h.Line) {
			keep = append(keep, kept{Hit: h, rel: rel})
		}
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i], keep[j]
		switch {
		case rank(a.Level) != rank(b.Level):
			return rank(a.Level) < rank(b.Level)
		case a.rel != b.rel:
			return a.rel < b.rel
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Tool != b.Tool:
			return a.Tool < b.Tool
		case a.Rule != b.Rule:
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
	findings := []core.Finding{}
	for i, k := range keep[:min(len(keep), maxFindings)] {
		findings = append(findings, finding(i+1, k))
	}
	if len(keep) > maxFindings {
		findings = append(findings, overflow(keep[maxFindings:]))
	}
	return findings
}

func finding(n int, k kept) core.Finding {
	first, _, _ := strings.Cut(k.Message, "\n")
	detail := k.Level + "\n" + k.Message
	files := []string{}
	if k.rel != "" {
		files = append(files, k.rel)
		detail += "\n" + k.rel
		if k.Line > 0 {
			detail += ":" + strconv.Itoa(k.Line)
		}
	}
	return core.Finding{
		ID:     "s" + strconv.Itoa(n),
		Title:  k.Tool + "/" + k.Rule + ": " + strings.TrimSpace(strings.TrimRight(first, "\r")),
		Detail: detail,
		Files:  files,
	}
}

func overflow(rest []kept) core.Finding {
	counts := map[string]int{}
	paths := map[string]bool{}
	for _, k := range rest {
		counts[k.Tool+"/"+k.Rule]++
		if k.rel != "" {
			paths[k.rel] = true
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = name + ": " + strconv.Itoa(counts[name])
	}
	files := make([]string, 0, len(paths))
	for p := range paths {
		files = append(files, p)
	}
	sort.Strings(files)
	return core.Finding{
		ID:     "s" + strconv.Itoa(maxFindings+1),
		Title:  "static: " + strconv.Itoa(len(rest)) + " more findings",
		Detail: strings.Join(lines, "\n"),
		Files:  files,
	}
}
