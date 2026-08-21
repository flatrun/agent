package notify

import (
	"testing"
	"testing/fstest"
)

func TestLoadEmailThemeFromManifest(t *testing.T) {
	files := fstest.MapFS{
		"theme/theme.yml":         {Data: []byte("schema_version: 1\nid: test\ntemplates:\n  main: notification.html\n  partials: [header.html]\nassets:\n  logo:\n    path: logo.png\n    mime: image/png\n")},
		"theme/notification.html": {Data: []byte(`{{template "header" .}} {{.Title}}`)},
		"theme/header.html":       {Data: []byte(`{{define "header"}}header{{end}}`)},
		"theme/logo.png":          {Data: []byte("png")},
	}
	theme, err := loadEmailTheme(files, "theme")
	if err != nil {
		t.Fatal(err)
	}
	if theme.main != "notification.html" || len(theme.logo) == 0 {
		t.Fatal("theme manifest was not loaded")
	}
}

func TestLoadEmailThemeRejectsUnknownSchema(t *testing.T) {
	files := fstest.MapFS{
		"theme/theme.yml": {Data: []byte("schema_version: 2\nid: test\ntemplates:\n  main: notification.html\n")},
	}
	if _, err := loadEmailTheme(files, "theme"); err == nil {
		t.Fatal("expected unsupported schema to fail")
	}
}
