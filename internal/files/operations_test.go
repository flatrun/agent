package files

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerCopyAndMovePaths(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	deployment := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(deployment, "source", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployment, "source", "nested", "file.txt"), []byte("content"), 0640); err != nil {
		t.Fatal(err)
	}

	if err := manager.Copy("demo", "/source", "/copied"); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(deployment, "copied", "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "content" {
		t.Fatalf("copied content = %q", copied)
	}

	if err := manager.Rename("demo", "/copied", "/moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deployment, "copied")); !os.IsNotExist(err) {
		t.Fatalf("copied path still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deployment, "moved", "nested", "file.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestManagerKeepsActiveComposeFileDiscoverable(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	deployment := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(deployment, "test"), 0755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(deployment, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := manager.Rename("demo", "/docker-compose.yml", "/test/docker-compose.yml"); err == nil {
		t.Fatal("expected moving the active compose file out of the root to fail")
	}
	if _, err := os.Stat(compose); err != nil {
		t.Fatalf("active compose file was moved: %v", err)
	}
	if err := manager.DeleteFile("demo", "/docker-compose.yml"); err == nil {
		t.Fatal("expected deleting the active compose file to fail")
	}
	if err := manager.Rename("demo", "/docker-compose.yml", "/compose.yaml"); err != nil {
		t.Fatalf("renaming to another root compose filename failed: %v", err)
	}
}

func TestManagerListsAndExtractsZip(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	deployment := filepath.Join(root, "demo")
	if err := os.MkdirAll(deployment, 0755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(deployment, "bundle.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	entry, err := writer.Create("config/app.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("setting")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := manager.ListArchive("demo", "/bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "config/app.txt" || entries[0].Size != 7 {
		t.Fatalf("entries = %#v", entries)
	}

	if err := manager.ExtractArchive("demo", "/bundle.zip", "/bundle"); err != nil {
		t.Fatal(err)
	}
	extracted, err := os.ReadFile(filepath.Join(deployment, "bundle", "config", "app.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(extracted) != "setting" {
		t.Fatalf("extracted content = %q", extracted)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "unsafe.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	entry, err := writer.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("outside")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	err = extractArchive(archivePath, filepath.Join(root, "output"))
	if err == nil {
		t.Fatal("expected traversal archive to fail")
	}
	if _, err := os.Stat(filepath.Join(root, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside destination: %v", err)
	}
}

func TestManagerRejectsSiblingDeploymentTraversal(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.resolvePath("demo", "../demo-copy/file.txt"); err == nil {
		t.Fatal("expected sibling deployment traversal to fail")
	}
}

func TestManagerPushArchiveMergesOrDeletesMissingContent(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	deployment := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(deployment, "sites"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployment, "sites", "stale.txt"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(root, "sites.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	entry, err := writer.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("new site")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	count, err := manager.PushArchive("demo", archivePath, "/sites", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("files pushed = %d", count)
	}
	if _, err := os.Stat(filepath.Join(deployment, "sites", "stale.txt")); err != nil {
		t.Fatal("merge removed an existing file")
	}

	if _, err := manager.PushArchive("demo", archivePath, "/sites", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deployment, "sites", "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete sync kept stale file: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(deployment, "sites", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new site" {
		t.Fatalf("pushed content = %q", content)
	}
}
