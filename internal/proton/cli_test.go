package proton

import (
	"errors"
	"testing"
)

const subItem = "item"

func TestClassify(t *testing.T) {
	boom := errors.New("exit status 1")
	cases := []struct {
		name, sub, stderr string
		want              error
	}{
		{"login fails with unknown wording", "login", "something unexpected", ErrAuth},
		{"login fails with empty stderr", "login", "", ErrAuth},
		{"item missing", subItem, "Error: item not found", ErrNotFound},
		{"not logged in", subItem, "you are not logged in", ErrAuth},
		{"unknown failure is retried", subItem, "kaboom", ErrTransient},
	}
	for _, c := range cases {
		if got := classify(c.sub, c.stderr, boom); !errors.Is(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
