package core

import (
	"maps"
	"slices"
)

const EventProviderVersions = "provider-versions"

func startVersions(store Store, runID string) map[string]string {
	st, _ := store.Load(runID)
	for _, ev := range st.Events {
		if ev.Kind == EventProviderVersions {
			return ev.Fields
		}
	}
	return nil
}

func versionMoves(start, now map[string]string) []string {
	names := slices.Sorted(maps.Keys(now))
	for name := range start {
		if _, ok := now[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := make([]string, len(names))
	for i, name := range names {
		was, ok := start[name]
		is, live := now[name]
		switch {
		case !live:
			out[i] = name + " " + was
		case ok && was != is:
			out[i] = name + " " + was + " → " + is
		default:
			out[i] = name + " " + is
		}
	}
	return out
}
