package templatesource

import (
	"context"
	"errors"
	"testing"
)

type stubSource struct {
	name      string
	available bool
	templates []Template
	err       error
}

func (s stubSource) Name() string                   { return s.name }
func (s stubSource) Available(context.Context) bool { return s.available }
func (s stubSource) List(context.Context) ([]Template, error) {
	return s.templates, s.err
}

func TestResolverPrefersFirstAvailable(t *testing.T) {
	first := stubSource{name: "marketplace", available: true, templates: []Template{{ID: "a", Compose: []byte("x")}}}
	second := stubSource{name: "github", available: true, templates: []Template{{ID: "b", Compose: []byte("y")}}}

	got, src, err := NewResolver(first, second).Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "marketplace" {
		t.Fatalf("source = %q, want marketplace", src)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %+v, want the first source's catalog", got)
	}
}

func TestResolverSkipsUnavailable(t *testing.T) {
	first := stubSource{name: "marketplace", available: false}
	second := stubSource{name: "github", available: true, templates: []Template{{ID: "b", Compose: []byte("y")}}}

	got, src, err := NewResolver(first, second).Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "github" || len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("got src=%q %+v, want github catalog", src, got)
	}
}

func TestResolverFallsThroughOnError(t *testing.T) {
	first := stubSource{name: "marketplace", available: true, err: errors.New("boom")}
	second := stubSource{name: "github", available: true, templates: []Template{{ID: "b", Compose: []byte("y")}}}

	got, src, err := NewResolver(first, second).Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src != "github" || len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("expected fallback to github, got src=%q %+v", src, got)
	}
}

func TestResolverReturnsErrorWhenAllAvailableFail(t *testing.T) {
	first := stubSource{name: "marketplace", available: true, err: errors.New("boom")}
	second := stubSource{name: "github", available: true, err: errors.New("bang")}

	_, _, err := NewResolver(first, second).Resolve(context.Background())
	if err == nil {
		t.Fatal("expected error when every available source fails")
	}
}

func TestResolverEmptyWhenNoneAvailable(t *testing.T) {
	got, src, err := NewResolver(stubSource{name: "github", available: false}).Resolve(context.Background())
	if err != nil || got != nil || src != "" {
		t.Fatalf("got (%v, %q, %v), want (nil, \"\", nil)", got, src, err)
	}
}
