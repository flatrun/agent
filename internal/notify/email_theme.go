package notify

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"path"

	"gopkg.in/yaml.v3"
)

type emailThemeManifest struct {
	SchemaVersion int    `yaml:"schema_version"`
	ID            string `yaml:"id"`
	Templates     struct {
		Main     string   `yaml:"main"`
		Partials []string `yaml:"partials"`
	} `yaml:"templates"`
	Assets struct {
		Logo struct {
			Path string `yaml:"path"`
			MIME string `yaml:"mime"`
		} `yaml:"logo"`
	} `yaml:"assets"`
}

type emailTheme struct {
	template *template.Template
	main     string
	logo     template.URL
}

//go:embed templates/default
var emailThemeFiles embed.FS

var defaultEmailTheme = mustLoadEmailTheme(emailThemeFiles, "templates/default")

func mustLoadEmailTheme(fsys fs.FS, root string) *emailTheme {
	theme, err := loadEmailTheme(fsys, root)
	if err != nil {
		panic(err)
	}
	return theme
}

func loadEmailTheme(fsys fs.FS, root string) (*emailTheme, error) {
	manifestData, err := fs.ReadFile(fsys, path.Join(root, "theme.yml"))
	if err != nil {
		return nil, fmt.Errorf("read email theme manifest: %w", err)
	}
	var manifest emailThemeManifest
	decoder := yaml.NewDecoder(bytes.NewReader(manifestData))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse email theme manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.ID == "" || manifest.Templates.Main == "" {
		return nil, fmt.Errorf("invalid email theme manifest")
	}
	files := append([]string{path.Join(root, manifest.Templates.Main)}, manifest.Templates.Partials...)
	for i := 1; i < len(files); i++ {
		files[i] = path.Join(root, files[i])
	}
	parsed, err := template.ParseFS(fsys, files...)
	if err != nil {
		return nil, fmt.Errorf("parse email theme templates: %w", err)
	}
	logoData, err := fs.ReadFile(fsys, path.Join(root, manifest.Assets.Logo.Path))
	if err != nil {
		return nil, fmt.Errorf("read email theme logo: %w", err)
	}
	if manifest.Assets.Logo.MIME != "image/png" {
		return nil, fmt.Errorf("unsupported email theme logo type %q", manifest.Assets.Logo.MIME)
	}
	logo := "data:" + manifest.Assets.Logo.MIME + ";base64," + base64.StdEncoding.EncodeToString(logoData)
	return &emailTheme{template: parsed, main: path.Base(manifest.Templates.Main), logo: template.URL(logo)}, nil
}
