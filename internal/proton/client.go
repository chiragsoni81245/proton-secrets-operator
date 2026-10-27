// Package proton wraps access to Proton Pass. The only supported interface is
// the pass-cli binary, so everything here treats it as an unreliable external
// dependency: bounded runtime, no shell, token only via environment.
package proton

import (
	"context"
	"errors"
)

// Client resolves pass:// references to secret values.
type Client interface {
	// Resolve returns the value referenced by ref (pass://VAULT/ITEM/FIELD).
	Resolve(ctx context.Context, ref string) (string, error)
	// Healthy reports whether a valid session exists (used by readiness).
	Healthy(ctx context.Context) error
}

var (
	// ErrNotFound means the vault, item or field does not exist or is not
	// visible to the token. It is terminal: retrying will not help until the
	// ProtonSecret or the Proton vault changes.
	ErrNotFound = errors.New("proton: reference not found")
	// ErrAuth means login failed or the token is invalid/expired.
	ErrAuth = errors.New("proton: authentication failed")
	// ErrTransient wraps timeouts and other retryable failures.
	ErrTransient = errors.New("proton: transient error")
)
