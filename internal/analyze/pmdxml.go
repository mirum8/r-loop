package analyze

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

func ParsePMD(base string, data []byte) ([]Hit, error) {
	var report struct {
		XMLName xml.Name `xml:"pmd"`
		Files   []struct {
			Name       string `xml:"name,attr"`
			Violations []struct {
				BeginLine int    `xml:"beginline,attr"`
				Rule      string `xml:"rule,attr"`
				Priority  int    `xml:"priority,attr"`
				Text      string `xml:",chardata"`
			} `xml:"violation"`
		} `xml:"file"`
	}
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("pmd: xml: %w", err)
	}
	var hits []Hit
	for _, f := range report.Files {
		path := f.Name
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		for _, v := range f.Violations {
			hits = append(hits, Hit{Tool: "pmd", Rule: v.Rule, Level: pmdLevel(v.Priority), Message: strings.TrimSpace(v.Text), Path: path, Line: v.BeginLine})
		}
	}
	return hits, nil
}

func pmdLevel(priority int) string {
	switch priority {
	case 1, 2:
		return "error"
	case 4, 5:
		return "note"
	}
	return "warning"
}
