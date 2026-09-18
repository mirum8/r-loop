package core

import (
	"reflect"
	"testing"
)

func TestFakeStoreLoadKeepsLastRecordPerQuestion(t *testing.T) {
	store := &fakeStore{}
	id, _ := store.Create(RunMeta{Todo: "todo.md"})
	store.Append(id, Record{Kind: RecordQuestion, Question: &Question{ID: "q1", Text: "which?"}})
	store.Append(id, Record{Kind: RecordQuestion, Question: &Question{ID: "q2", Text: "other?"}})
	store.Append(id, Record{Kind: RecordQuestion, Question: &Question{ID: "q1", Text: "which?", Answer: "a"}})

	st, _ := store.Load(id)

	want := []Question{{ID: "q1", Text: "which?", Answer: "a"}, {ID: "q2", Text: "other?"}}
	if !reflect.DeepEqual(st.Questions, want) {
		t.Fatalf("questions = %+v", st.Questions)
	}
}

func TestFakesShareOneOrderedCallLog(t *testing.T) {
	shared := &callLog{}
	store := &fakeStore{callLog: callLog{Shared: shared}}
	host := &fakeSessionHost{callLog: callLog{Shared: shared}}

	store.Append("run-1", Record{Kind: RecordStep})
	host.Open(OpenSpec{CWD: "/wt", Label: "p1", Env: map[string]string{"R_LOOP_RUN": "run-1"}})

	want := []string{"Store.Append run-1 step", "SessionHost.Open /wt p1 map[R_LOOP_RUN:run-1]"}
	if !reflect.DeepEqual(shared.Calls(), want) {
		t.Fatalf("calls = %q", shared.Calls())
	}
	if host.Opened[0].Env["R_LOOP_RUN"] != "run-1" {
		t.Fatalf("opened = %+v", host.Opened)
	}
}
