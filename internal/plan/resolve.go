package plan

import (
	"regexp"
	"slices"
	"strings"

	"r-loop/internal/core"
)

var (
	resolveHeadingRe = regexp.MustCompile(`(?i)^##[ \t]+Resolve[ \t]+first\b`)
	entryStartRe     = regexp.MustCompile(`^ ?[-*][ \t]`)
	entryBoxRe       = regexp.MustCompile(`^( ?[-*][ \t]+)\[([ xX])\]`)
	boldRe           = regexp.MustCompile(`\*\*(.+?)\*\*`)
	labelRe          = regexp.MustCompile(`\b([A-Z][A-Za-z]{2,}):`)
	segmentStartRe   = regexp.MustCompile(`(^ ?[-*][ \t]+(\[[ xX]\][ \t]+)?|[.?!][ \t]+)$`)
)

var knownLabels = map[string]bool{
	"owner": true, "blocks": true, "timebox": true, "output": true,
	"resolved": true, "alternative": true, "outstanding": true,
}

var (
	personOwnerRe = regexp.MustCompile(`(?i)\b(legal|finance|procurement|hr|people|compliance|security council)\b`)
	personRes     = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bhir(e|ing)\b|\bstaff(ing)?\b|\brota\b|on-call cover`),
		regexp.MustCompile(`(?i)\bprocure|\bpurchase\b|\blicen[cs]e agreement\b|\bcontract\b|\bsign(ing)? (a|the)\b`),
		regexp.MustCompile(`(?i)\btrain(ing)? the team\b|\bworkshop\b|\bonboard the\b`),
		regexp.MustCompile(`(?i)\bapprovals?\b|\bapprove\b|\bbudget\b|\blegal\b|\bDPA\b|\bNDA\b|\bprocurement\b`),
	}
	decisionRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bspike\b`),
		regexp.MustCompile(`(?i)\binvestigat|\bresearch\b|\bevaluate\b|\bexplore whether\b|\bbenchmark\b`),
		regexp.MustCompile(`(?i)\bdecide whether\b|\bchoose between\b|\bpick between\b`),
		regexp.MustCompile(`(?i)\bcan (?:we|it|they|the)\b|\bdoes (?:it|the)\b|\bis (?:it|the)\b|\bwhether\b|\bwhich\b`),
	}
)

func classify(subject, owner string) string {
	if personOwnerRe.MatchString(owner) {
		return core.EntryPerson
	}
	for _, re := range personRes {
		if re.MatchString(subject) {
			return core.EntryPerson
		}
	}
	for _, re := range decisionRes {
		if re.MatchString(subject) {
			return core.EntryDecision
		}
	}
	return core.EntryUnclassified
}

func OnlyResolveFirstChanged(before, after []byte) bool {
	b, a := outsideResolveFirst(before), outsideResolveFirst(after)
	return slices.Equal(b, a)
}

func outsideResolveFirst(data []byte) []string {
	lines := strings.SplitAfter(string(data), "\n")
	for i, l := range lines {
		if !resolveHeadingRe.MatchString(l) {
			continue
		}
		end := i + 1
		for end < len(lines) && !anyHeadingRe.MatchString(lines[end]) {
			end++
		}
		return append(slices.Clone(lines[:i+1]), lines[end:]...)
	}
	return lines
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
	seenKnown := false
	labels := marks[:0]
	for _, m := range marks {
		known := knownLabels[strings.ToLower(flat[m[2]:m[3]])]
		seenKnown = seenKnown || known
		if seenKnown || segmentStartRe.MatchString(flat[:m[0]]) {
			labels = append(labels, m)
		}
	}
	marks = labels
	fields := map[string]string{}
	for i := range marks {
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
	e.Alternative, e.Outstanding = fields["alternative"], fields["outstanding"]
	head := flat
	for _, m := range marks {
		if knownLabels[strings.ToLower(flat[m[2]:m[3]])] {
			head = flat[:m[0]]
			break
		}
	}
	e.Kind = classify(head, e.Owner)

	if idx := strings.Index(e.Blocks, "Phase"); idx >= 0 {
		e.BlocksPhases = phaseRefs(e.Blocks[idx:])
	}
	e.BlocksAll = len(e.BlocksPhases) == 0
	if e.Ticked && e.Resolved == "" {
		e.Malformed = append(e.Malformed, "ticked without Resolved")
	}
	return e
}
