package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

func Banner(cfg LoopConfig) string {
	var b strings.Builder
	p := cfg.Provenance
	for _, name := range append(append([]string{}, cfg.Pipeline...), "milestone") {
		row := cfg.Steps[name]
		k := "steps." + name + "."
		fmt.Fprintf(&b, "%s  %s  %s  %s  %s  %s  ← %s\n", name, row.Provider, orDefault(row.Model), orDefault(row.Effort),
			Duration(row.Timeout), row.Check, sources(p[k+"provider"], p[k+"model"], p[k+"effort"]))
		if row.Rounds > 0 && len(row.Reviewers) > 0 {
			fmt.Fprintf(&b, "  review rounds %d %s  ← %s\n", row.Rounds, Duration(row.ReviewTimeout), p[k+"rounds"])
			for _, rv := range row.Reviewers {
				fmt.Fprintf(&b, "  reviewer %s  ← %s\n", roleLine(rv.Provider, rv.Model, rv.Effort), p[k+"reviewers"])
			}
		}
		if fb := row.Fallback; fb.Provider != "" {
			fmt.Fprintf(&b, "  fallback %s  ← %s\n", roleLine(fb.Provider, fb.Model, fb.Effort), p[k+"fallback"])
		}
	}
	fix := cfg.Land.Fix
	fmt.Fprintf(&b, "gatefix %s  ← %s\n", roleLine(fix.Provider, fix.Model, fix.Effort),
		sources(p["land.fix.provider"], p["land.fix.model"], p["land.fix.effort"]))
	for _, o := range cfg.overrides {
		fmt.Fprintf(&b, "override: %s %s %s (flag) replaces %s (%s)\n", o.Step, o.Key, o.Value, orDefault(o.old),
			strings.TrimSuffix(o.oldSource, ":steps."+o.Step+"."+o.Key))
	}
	for _, sw := range cfg.swaps {
		fmt.Fprintf(&b, "override: steps.%s.fallback swapped to %s (--provider) replaces %s (%s)\n", sw.step, sw.to.Provider,
			strings.Join(slices.DeleteFunc([]string{sw.old.Provider, sw.old.Model, sw.old.Effort}, func(s string) bool { return s == "" }), " "),
			strings.TrimSuffix(sw.oldSource, ":steps."+sw.step+".fallback"))
	}
	w := cfg.Watchdog
	fmt.Fprintf(&b, "watchdog: %s %s %s allow [%s]  ← %s\n", w.Provider, orDefault(w.Model), orDefault(w.Effort),
		strings.Join(w.Allow, ", "), sources(p["watchdog.provider"], p["watchdog.model"], p["watchdog.effort"]))
	return b.String()
}

func roleLine(provider, model, effort string) string {
	return provider + " " + orDefault(model) + " " + orDefault(effort)
}

func orDefault(v string) string {
	if v == "" {
		return "provider default"
	}
	return v
}

func sources(provider, model, effort string) string {
	if provider == model && model == effort {
		return provider
	}
	return "provider " + provider + " model " + model + " effort " + effort
}

func Duration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
