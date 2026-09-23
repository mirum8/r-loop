package calc

import "testing"

func TestAdd(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"4", 4},
		{"1,2,3", 6},
	}
	for _, c := range cases {
		got, err := Add(c.input)
		if err != nil {
			t.Fatalf("Add(%q) returned error %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("Add(%q) = %d, want %d", c.input, got, c.want)
		}
	}
}

func TestAddRejectsGarbage(t *testing.T) {
	if _, err := Add("1,x"); err == nil {
		t.Error("Add(\"1,x\") returned no error")
	}
}
