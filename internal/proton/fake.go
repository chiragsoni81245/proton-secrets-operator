package proton

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Client for tests.
type Fake struct {
	mu     sync.Mutex
	values map[string]string
	// Err, when set, is returned by every call (simulates Proton being down).
	Err error
	// Calls counts Resolve invocations.
	Calls int
}

// NewFake returns a Fake seeded with values keyed by full pass:// reference.
func NewFake(values map[string]string) *Fake {
	f := &Fake{values: map[string]string{}}
	for k, v := range values {
		f.values[k] = v
	}
	return f
}

// Set updates a value.
func (f *Fake) Set(ref, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[ref] = value
}

func (f *Fake) Resolve(_ context.Context, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Err != nil {
		return "", f.Err
	}
	v, ok := f.values[ref]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return v, nil
}

func (f *Fake) Healthy(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Err
}
