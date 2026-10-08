package core

import "fmt"

const (
	minPaneCols = 60
	minPaneRows = 15
)

func splitFits(info PaneInfo, direction string, ratio float64) bool {
	if ratio == 0 {
		ratio = 0.5
	}
	share := func(n int) float64 { return min(float64(n)*ratio, float64(n)*(1-ratio)) + 1e-9 }
	if direction == "right" {
		return share(info.Cols) >= minPaneCols && info.Rows >= minPaneRows
	}
	return info.Cols >= minPaneCols && share(info.Rows) >= minPaneRows
}

func paneFits(host SessionHost, pane, direction string, ratio float64) (PaneInfo, bool) {
	info, err := host.PaneInfo(pane)
	if err != nil {
		return PaneInfo{}, false
	}
	return info, splitFits(info, direction, ratio)
}

func openBeside(host SessionHost, pane string, spec OpenSpec) (string, string, string, error) {
	if pane != "" {
		info, ok := paneFits(host, pane, "right", 0)
		if ok {
			p, err := host.Split(pane, "right", spec.CWD, 0, nil)
			if err != nil {
				return "", "", "", fmt.Errorf("split: %w", err)
			}
			return p, "", "split", nil
		}
		if info.Workspace != "" {
			if p, err := host.OpenTab(info.Workspace, spec); err == nil {
				return p, "", "tab", nil
			}
		}
	}
	ws, err := host.Open(spec)
	if err != nil {
		return "", "", "", fmt.Errorf("open workspace: %w", err)
	}
	return ws.RootPane, ws.ID, "workspace", nil
}

func openTab(host SessionHost, pane string, spec OpenSpec) (string, string, error) {
	if pane != "" {
		if info, err := host.PaneInfo(pane); err == nil && info.Workspace != "" {
			if p, err := host.OpenTab(info.Workspace, spec); err == nil {
				return p, "", nil
			}
		}
	}
	ws, err := host.Open(spec)
	if err != nil {
		return "", "", fmt.Errorf("open workspace: %w", err)
	}
	return ws.RootPane, ws.ID, nil
}
