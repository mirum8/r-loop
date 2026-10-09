package core

import (
	"strings"
	"testing"
)

func TestDesignAskedIsTrueOnlyWithADesignFileEntry(t *testing.T) {
	plain := []StepKind{{Name: "plan", Check: "plan-file"}, {Name: "implement", Check: "diff"}}
	designed := append([]StepKind{{Name: "design", Check: "design-file"}}, plain...)

	if DesignAsked(plain) {
		t.Errorf("a pipeline without a design-file entry asks for design")
	}
	if !DesignAsked(designed) {
		t.Errorf("a pipeline with a design-file entry does not ask for design")
	}
}

func TestPipelineRefusesADesignStepAfterThePlanStep(t *testing.T) {
	rows := map[string]StepRow{"plan": {}, "design": {}}
	prompts := map[string]string{"plan": "plan", "design": "design"}
	checks := map[string]string{"plan": "plan-file", "design": "design-file"}

	_, err := Pipeline([]string{"plan", "design"}, rows, prompts, checks)

	want := `pipeline entry "design": a design-file step must come before the plan-file step`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestPipelineRefusesASecondDesignStep(t *testing.T) {
	rows := map[string]StepRow{"design": {}, "sketch": {}, "plan": {}}
	prompts := map[string]string{"design": "design", "sketch": "design", "plan": "plan"}
	checks := map[string]string{"design": "design-file", "sketch": "design-file", "plan": "plan-file"}

	_, err := Pipeline([]string{"design", "sketch", "plan"}, rows, prompts, checks)

	if err == nil || !strings.Contains(err.Error(), `pipeline entry "sketch"`) || !strings.Contains(err.Error(), "a second design-file step") {
		t.Fatalf("err = %v", err)
	}
}
