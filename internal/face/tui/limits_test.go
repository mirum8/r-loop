package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"r-loop/internal/quota"
)

func limits(claude7d, codex7d float64) []quota.Limits {
	return []quota.Limits{
		{Provider: "claude", Windows: []quota.Window{{Label: "5h", Percent: 12}, {Label: "7d", Percent: claude7d}}},
		{Provider: "codex", Windows: []quota.Window{{Label: "7d", Percent: codex7d}}},
	}
}

func header(m Model, width int) string {
	m.Width = width
	return ansi.Strip(strings.Split(m.View(), "\n")[0])
}

func TestTheHeaderShowsEachProvidersLimitsBeforeTheWatchdog(t *testing.T) {
	m := newModel(recorded())
	m.Limits = limits(49, 3)

	got := header(m, 140)

	if !strings.Contains(got, "claude 49% · codex 3% · watchdog live · ~1h3m left · started") {
		t.Fatalf("header %q", got)
	}
}

func TestANarrowHeaderDropsTheLimitsBeforeAnythingElse(t *testing.T) {
	m := newModel(recorded())
	m.Limits = limits(49, 3)
	width := 200
	for strings.Contains(header(m, width), "claude") {
		width--
	}

	got := header(m, width)

	if !strings.Contains(got, "watchdog live · ~1h3m left · started") {
		t.Fatalf("header at %d without limits %q", width, got)
	}
}

func TestAHeaderTooShortForEveryProviderKeepsTheFirstThatFit(t *testing.T) {
	m := newModel(recorded())
	m.Limits = limits(49, 3)
	width := 200
	for strings.Contains(header(m, width), "codex") {
		width--
	}

	got := header(m, width)

	if !strings.Contains(got, "claude 49% · watchdog live · ~1h3m left · started") {
		t.Fatalf("header at %d %q", width, got)
	}
}

func TestALimitAtNinetyPercentIsInTheErrorColourAndBelowItIsDim(t *testing.T) {
	m := coloured(false)
	m.Width = 160
	m.Limits = limits(90, 89)

	head := strings.Split(m.View(), "\n")[0]

	if !regexp.MustCompile(`\x1b\[38;2;224;115;10[56][0-9;]*m90%`).MatchString(head) {
		t.Fatalf("90%% is not in the error colour: %q", head)
	}
	if regexp.MustCompile(`\x1b\[38;2;224;115;10[56][0-9;]*m89%`).MatchString(head) {
		t.Fatalf("89%% is in the error colour: %q", head)
	}
	if strings.Contains(head, "38;2;224;16") {
		t.Fatalf("limits use amber: %q", head)
	}
}

func TestAHotFiveHourWindowIsShownInTheErrorColour(t *testing.T) {
	m := coloured(false)
	m.Width = 160
	m.Limits = limits(42, 3)
	m.Limits[0].Windows[0].Percent = 93

	head := strings.Split(m.View(), "\n")[0]

	if !strings.Contains(ansi.Strip(head), "claude 5h 93% 42% · codex 3%") {
		t.Fatalf("header %q", ansi.Strip(head))
	}
	if !regexp.MustCompile(`\x1b\[38;2;224;115;10[56][0-9;]*m5h 93%`).MatchString(head) {
		t.Fatalf("hot 5h is not in the error colour: %q", head)
	}
}

func TestACoolFiveHourWindowIsLeftOut(t *testing.T) {
	m := newModel(recorded())
	m.Limits = limits(42, 3)

	if got := header(m, 160); strings.Contains(got, "5h") || strings.Contains(got, "12%") {
		t.Fatalf("header %q", got)
	}
}
