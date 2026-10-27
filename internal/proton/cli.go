package proton

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// PAT sessions last 2h; refresh well before that.
	sessionMaxAge  = 100 * time.Minute
	defaultTimeout = 30 * time.Second
	healthCacheTTL = 30 * time.Second
)

// CLIConfig configures the pass-cli backed client.
type CLIConfig struct {
	// Binary is the path to pass-cli.
	Binary string
	// TokenFile holds the personal access token. It is re-read on every login
	// so rotating the Secret needs no restart.
	TokenFile string
	// SessionDir is PROTON_PASS_SESSION_DIR (an emptyDir in the pod).
	SessionDir string
	// KeyProvider is PROTON_PASS_KEY_PROVIDER: fs (default), env or keyring.
	KeyProvider string
	// Timeout bounds each CLI invocation.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous pass-cli processes.
	MaxConcurrent int
}

// CLI is a Client backed by the pass-cli binary.
type CLI struct {
	cfg CLIConfig
	sem chan struct{}

	mu       sync.Mutex
	loggedAt time.Time

	healthMu  sync.Mutex
	healthAt  time.Time
	healthErr error
}

// NewCLI validates cfg and returns a client. It does not log in yet.
func NewCLI(cfg CLIConfig) (*CLI, error) {
	if cfg.Binary == "" {
		cfg.Binary = "pass-cli"
	}
	if cfg.TokenFile == "" {
		return nil, errors.New("proton: token file is required")
	}
	if cfg.KeyProvider == "" {
		cfg.KeyProvider = "fs"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	return &CLI{cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrent)}, nil
}

// Resolve reads a single reference. On an auth failure it logs in again once.
func (c *CLI) Resolve(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, "pass://") {
		return "", fmt.Errorf("%w: %q is not a pass:// reference", ErrNotFound, ref)
	}
	if err := c.ensureSession(ctx, false); err != nil {
		return "", err
	}
	out, err := c.run(ctx, nil, "item", "view", ref)
	if errors.Is(err, ErrAuth) {
		if lerr := c.ensureSession(ctx, true); lerr != nil {
			return "", lerr
		}
		out, err = c.run(ctx, nil, "item", "view", ref)
	}
	if err != nil {
		return "", err
	}
	// pass-cli prints the value followed by a single newline.
	return strings.TrimSuffix(out, "\n"), nil
}

// Healthy verifies the session with `pass-cli info`.
// The result is cached briefly so frequent probes do not spawn pass-cli each time.
func (c *CLI) Healthy(ctx context.Context) error {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	if !c.healthAt.IsZero() && time.Since(c.healthAt) < healthCacheTTL {
		return c.healthErr
	}
	err := c.ensureSession(ctx, false)
	if err == nil {
		_, err = c.run(ctx, nil, "info")
	}
	c.healthAt, c.healthErr = time.Now(), err
	return err
}

func (c *CLI) ensureSession(ctx context.Context, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && !c.loggedAt.IsZero() && time.Since(c.loggedAt) < sessionMaxAge {
		return nil
	}
	token, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return fmt.Errorf("%w: reading token file: %v", ErrAuth, err)
	}
	env := []string{"PROTON_PASS_PERSONAL_ACCESS_TOKEN=" + strings.TrimSpace(string(token))}
	if _, err := c.run(ctx, env, "login"); err != nil {
		if errors.Is(err, ErrAuth) || errors.Is(err, ErrTransient) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrAuth, err)
	}
	c.loggedAt = time.Now()
	return nil
}

func (c *CLI) run(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %v", ErrTransient, ctx.Err())
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...) // #nosec G204 -- fixed binary, no shell
	// Minimal environment: do not leak the operator's own env to the CLI.
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + c.cfg.SessionDir,
		"PROTON_PASS_KEY_PROVIDER=" + c.cfg.KeyProvider,
		"PROTON_PASS_SESSION_DIR=" + c.cfg.SessionDir,
		"PASS_LOG_LEVEL=error",
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: pass-cli %s: %v", ErrTransient, args[0], ctx.Err())
		}
		// Only the subcommand and a classification are returned: stderr can
		// echo references or tokens, so it is never propagated verbatim.
		return "", classify(args[0], stderr.String(), err)
	}
	return stdout.String(), nil
}

// classify maps CLI failures to sentinel errors using stderr text. The exact
// wording is undocumented; unknown failures are treated as transient so they
// are retried with backoff rather than marked terminal.
func classify(sub, stderr string, err error) error {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "not found"), strings.Contains(s, "no such"), strings.Contains(s, "does not exist"):
		return fmt.Errorf("%w: pass-cli %s", ErrNotFound, sub)
	case strings.Contains(s, "unauthor"), strings.Contains(s, "not logged in"),
		strings.Contains(s, "session"), strings.Contains(s, "token"), strings.Contains(s, "expired"):
		return fmt.Errorf("%w: pass-cli %s", ErrAuth, sub)
	default:
		return fmt.Errorf("%w: pass-cli %s failed: %v", ErrTransient, sub, err)
	}
}

// CLIVersion returns the output of `pass-cli --version` (first line) or
// "unknown". It is informational (metric label); a missing binary is not fatal
// here because readiness reports an unusable session separately.
func CLIVersion(ctx context.Context, binary string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output() // #nosec G204 -- operator-configured path
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" {
		return "unknown"
	}
	return line
}
