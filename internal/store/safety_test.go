package store

import (
	"os"
	"path/filepath"
	"testing"

	"r-loop/internal/core"
)

func TestCreateRecordsTheStartBranchAndLoadReadsIt(t *testing.T) {
	s, _ := newStore(t)
	id, err := s.Create(core.RunMeta{Todo: "todo.md", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load(id)
	if err != nil || st.Branch != "main" {
		t.Fatalf("Load = %+v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(id), "meta.json"), []byte(`{"todo":"todo.md"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = s.Load(id)
	if err != nil || st.Branch != "" {
		t.Fatalf("old Load = %+v, %v", st, err)
	}
}
