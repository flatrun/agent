package source

import (
	"context"
	"testing"
)

type stubProvider struct{ typ string }

func (s stubProvider) Type() string { return s.typ }
func (s stubProvider) Fetch(context.Context, Descriptor, string, func(string)) (*Result, error) {
	return &Result{}, nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry(stubProvider{"git"}, stubProvider{"upload"})

	if _, ok := r.Get("git"); !ok {
		t.Error("expected git provider to be registered")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("unregistered type should not resolve")
	}
	if len(r.Types()) != 2 {
		t.Errorf("Types() = %v, want 2 entries", r.Types())
	}
}
