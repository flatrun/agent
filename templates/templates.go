package templates

import (
	"embed"
	"io/fs"
	"path/filepath"
)

//go:embed */metadata.yml */docker-compose.yml
var FS embed.FS

func List() ([]string, error) {
	var templates []string
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			templates = append(templates, entry.Name())
		}
	}
	return templates, nil
}

func GetMetadata(name string) ([]byte, error) {
	return FS.ReadFile(filepath.Join(name, "metadata.yml"))
}

func GetCompose(name string) ([]byte, error) {
	return FS.ReadFile(filepath.Join(name, "docker-compose.yml"))
}
