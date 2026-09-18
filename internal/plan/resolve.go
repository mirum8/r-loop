package plan

import (
	"regexp"
	"strconv"
	"strings"

	"r-loop/internal/core"
)

var (
	resolveHeadingRe = regexp.MustCompile(`(?i)^##[ \t]+Resolve[ \t]+first\b`)
	entryStartRe     = regexp.MustCompile(`^ ?[-*][ \t]`)
	entryBoxRe       = regexp.MustCompile(`^( ?[-*][ \t]+)\[([ xX])\]`)
	entryMarkerRe    = regexp.MustCompile(`^ ?[-*][ \t]+`)
	boldRe           = regexp.MustCompile(`\*\*(.+?)\*\*`)
	labelRe          = regexp.MustCompile(`\b([A-Z][A-Za-z]{2,}):`)
)

var knownLabels = map[string]bool{
	"owner": true, "blocks": true, "timebox": true, "output": true,
	"resolved": true, "alternative": true, "outstanding": true,
}

type entrySpan struct {
	entry      core.Entry
	start, end int
}

func resolveFirst(lines []string) (bool, []entrySpan) {
	begin := -1
	for i, l := range lines {
		if resolveHeadingRe.MatchString(l) {
			begin = i + 1
			break
		}
	}
	if begin < 0 {
		return false, nil
	}
	stop := begin
	for stop < len(lines) && !anyHeadingRe.MatchString(lines[stop]) {
		stop++
	}

	var spans []entrySpan
	for i := begin; i < stop; i++ {
		if !entryStartRe.MatchString(lines[i]) {
			continue
		}
		next := i + 1
		for next < stop && !entryStartRe.MatchString(lines[next]) {
			next++
		}
		end := next
		for end > i+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		spans = append(spans, entrySpan{entry: parseEntry(lines[i:end]), start: i, end: end})
		i = next - 1
	}
	return true, spans
}

func parseEntry(lines []string) core.Entry {
	e := core.Entry{Body: strings.TrimRight(strings.Join(lines, ""), " \t\r\n")}
	if m := entryBoxRe.FindStringSubmatch(lines[0]); m != nil {
		e.HasBox = true
		e.Ticked = m[2] != " "
	}
	flat := strings.Join(strings.Fields(e.Body), " ")
	if m := boldRe.FindStringSubmatch(flat); m != nil {
		e.Name = strings.TrimSpace(m[1])
	}

	marks := labelRe.FindAllStringSubmatchIndex(flat, -1)
	first := len(marks)
	for i, m := range marks {
		if knownLabels[strings.ToLower(flat[m[2]:m[3]])] {
			first = i
			break
		}
	}
	fields := map[string]string{}
	for i := first; i < len(marks); i++ {
		m := marks[i]
		label := flat[m[2]:m[3]]
		end := len(flat)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		key := strings.ToLower(label)
		if !knownLabels[key] {
			e.Malformed = append(e.Malformed, "unknown label "+label+":")
			continue
		}
		fields[key] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(flat[m[1]:end]), "."))
	}
	e.Owner, e.Blocks, e.Timebox, e.Output, e.Resolved = fields["owner"], fields["blocks"], fields["timebox"], fields["output"], fields["resolved"]

	if idx := strings.Index(e.Blocks, "Phase"); idx >= 0 {
		for _, s := range numberRe.FindAllString(e.Blocks[idx:], -1) {
			n, _ := strconv.Atoi(s)
			e.BlocksPhases = append(e.BlocksPhases, n)
		}
	}
	e.BlocksAll = len(e.BlocksPhases) == 0
	if e.Ticked && e.Resolved == "" {
		e.Malformed = append(e.Malformed, "ticked without Resolved")
	}
	return e
}
