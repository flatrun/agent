package models

import "testing"

func TestEffectiveLogSources_AlwaysIncludesStdout(t *testing.T) {
	m := &ServiceMetadata{Name: "app"}
	sources := m.EffectiveLogSources()
	if len(sources) != 1 || sources[0].Type != LogSourceStdout {
		t.Fatalf("expected only stdout, got %#v", sources)
	}
}

func TestEffectiveLogSources_IncludesKindDefaults(t *testing.T) {
	m := &ServiceMetadata{Name: "app", Kind: "laravel"}
	sources := m.EffectiveLogSources()

	var found bool
	for _, s := range sources {
		if s.ID == "laravel-app" {
			found = true
			if !s.Builtin || s.Type != LogSourceFile {
				t.Errorf("laravel default wrong: %#v", s)
			}
		}
	}
	if !found {
		t.Errorf("expected laravel built-in source, got %#v", sources)
	}
}

func TestEffectiveLogSources_UserOverridesBuiltin(t *testing.T) {
	m := &ServiceMetadata{
		Name: "app",
		Kind: "laravel",
		LogSources: []LogSource{
			{ID: "laravel-app", Name: "My Laravel", Type: LogSourceFile, Path: "custom/path.log"},
		},
	}
	sources := m.EffectiveLogSources()

	count := 0
	for _, s := range sources {
		if s.ID == "laravel-app" {
			count++
			if s.Path != "custom/path.log" || s.Builtin {
				t.Errorf("override not applied: %#v", s)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one laravel-app source, got %d", count)
	}
}

func TestFindLogSource(t *testing.T) {
	m := &ServiceMetadata{Name: "app", Kind: "nginx"}

	if s, ok := m.FindLogSource(""); !ok || s.Type != LogSourceStdout {
		t.Errorf("empty id should resolve to stdout, got %#v ok=%v", s, ok)
	}
	if s, ok := m.FindLogSource("nginx-error"); !ok || s.Path != "logs/error.log" {
		t.Errorf("nginx-error lookup wrong: %#v ok=%v", s, ok)
	}
	if _, ok := m.FindLogSource("does-not-exist"); ok {
		t.Error("unknown source should not resolve")
	}
}
