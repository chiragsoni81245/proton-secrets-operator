package proton

import (
	"errors"
	"strings"
	"testing"
)

const (
	subItem  = "item"
	subLogin = "login"
)

func TestClassify(t *testing.T) {
	boom := errors.New("exit status 1")
	cases := []struct {
		name, sub, stderr string
		want              error
	}{
		{"login rejected", subLogin, "Error: invalid token", ErrAuth},
		{"login unauthorized", subLogin, "401 Unauthorized", ErrAuth},
		{"login network error is not auth", subLogin, "dial tcp: lookup proton.me: no route to host", ErrTransient},
		{"login unknown wording is not auth", subLogin, "something unexpected", ErrTransient},
		{"login empty stderr is not auth", subLogin, "", ErrTransient},
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

func TestClassifyKeepsReasonVisible(t *testing.T) {
	err := classify(subLogin, "keyring unavailable", errors.New("exit status 1"))
	if !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatalf("the reason must be in the error, got %q", err)
	}
}

func TestSanitize(t *testing.T) {
	env := []string{"PROTON_PASS_PERSONAL_ACCESS_TOKEN=pst_abc123::SECRETKEY"}
	got := sanitize("bad token pst_abc123::SECRETKEY and\nanother pst_zzz999 line", env)
	for _, leak := range []string{"pst_abc123", "SECRETKEY", "pst_zzz999"} {
		if strings.Contains(got, leak) {
			t.Fatalf("leaked %q in %q", leak, got)
		}
	}
	if long := sanitize(strings.Repeat("x", 500), nil); len(long) > maxDetail+3 {
		t.Fatalf("not truncated: %d", len(long))
	}
}
