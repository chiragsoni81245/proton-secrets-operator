package proton

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakePassCLI mimics pass-cli's session behaviour: `login` is refused while a session exists, `logout --force`
// removes it, `info` needs it. Every call is appended to calls.log.
const fakePassCLI = `#!/bin/sh
dir="$PROTON_PASS_SESSION_DIR"
echo "$*" >> "$dir/calls.log"
case "$1" in
  login)
    if [ -f "$dir/session" ]; then echo "Error: Already authenticated" >&2; exit 1; fi
    touch "$dir/session" ;;
  logout)
    rm -f "$dir/session" ;;
  info)
    [ -f "$dir/session" ] || { echo "Error: not logged in" >&2; exit 1; } ;;
esac
`

func newFakeCLI(t *testing.T) (*CLI, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "pass-cli")
	if err := os.WriteFile(bin, []byte(fakePassCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("pst_test"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewCLI(CLIConfig{Binary: bin, TokenFile: token, SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func calls(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestClassifyAlreadyAuthenticated(t *testing.T) {
	err := classify(subLogin, "Error: Already authenticated", errors.New("exit status 1"))
	if !errors.Is(err, errAlreadyLoggedIn) || !errors.Is(err, ErrTransient) || errors.Is(err, ErrAuth) {
		t.Fatalf("got %v, want errAlreadyLoggedIn (transient, not auth)", err)
	}
}

// The reported bug: a session left from before (container restart with the session emptyDir kept, or the periodic
// refresh) made every login fail with "Already authenticated", forever.
func TestEnsureSessionReplacesExistingSession(t *testing.T) {
	c, dir := newFakeCLI(t)
	if err := os.WriteFile(filepath.Join(dir, "session"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Healthy(context.Background()); err != nil {
		t.Fatalf("Healthy with a leftover session: %v", err)
	}
	if got, want := calls(t, dir), "login\nlogout --force\nlogin\ninfo"; got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnsureSessionRefreshAfterMaxAge(t *testing.T) {
	c, dir := newFakeCLI(t)
	ctx := context.Background()
	if err := c.ensureSession(ctx, false); err != nil {
		t.Fatalf("first login: %v", err)
	}
	c.loggedAt = time.Now().Add(-sessionMaxAge - time.Minute) // the session is due for a refresh
	if err := c.ensureSession(ctx, false); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := c.ensureSession(ctx, true); err != nil {
		t.Fatalf("forced re-login: %v", err)
	}
	if got, want := calls(t, dir), "login\nlogin\nlogout --force\nlogin\nlogin\nlogout --force\nlogin"; got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
}
