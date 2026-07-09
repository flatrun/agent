package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	name     string
	mu       sync.Mutex
	objects  map[string][]byte
	modtimes map[string]time.Time
	failList bool
}

func newFakeStore(name string) *fakeStore {
	return &fakeStore{name: name, objects: map[string][]byte{}, modtimes: map[string]time.Time{}}
}

func (f *fakeStore) Name() string { return f.name }

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	f.modtimes[key] = time.Now()
	return nil
}

func (f *fakeStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeStore) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	if f.failList {
		return nil, fmt.Errorf("list failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ObjectInfo
	for key, data := range f.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, ObjectInfo{Key: key, Size: int64(len(data)), ModTime: f.modtimes[key]})
	}
	return out, nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	delete(f.modtimes, key)
	return nil
}

func (f *fakeStore) Stat(_ context.Context, key string) (ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return ObjectInfo{Key: key, Size: int64(len(data)), ModTime: f.modtimes[key]}, nil
}

func (f *fakeStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func seedDeployment(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	compose := "services:\n  web:\n    image: nginx\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestCreateBackup_MirrorsToRemote(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	remote := newFakeStore("s3-test")
	m.SetRemotes([]Store{remote})
	seedDeployment(t, tmpDir, "app")

	b, err := m.CreateBackup(context.Background(), "app", nil)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	if !containsStr(b.Locations, locationLocal) || !containsStr(b.Locations, "s3-test") {
		t.Fatalf("expected local and remote locations, got %v", b.Locations)
	}
	if !remote.has(backupKey("app", b.ID)) {
		t.Fatalf("expected remote to hold %s", backupKey("app", b.ID))
	}
}

func TestListAndGetBackup_RemoteOnlyAfterLocalPruned(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	remote := newFakeStore("s3-test")
	m.SetRemotes([]Store{remote})
	seedDeployment(t, tmpDir, "app")

	b, err := m.CreateBackup(context.Background(), "app", nil)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	// Simulate local retention pruning the on-disk copy.
	if err := m.deleteLocalBackup(b.ID); err != nil {
		t.Fatalf("prune local: %v", err)
	}

	list, err := m.ListBackups(&BackupListFilter{DeploymentName: "app"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(list))
	}
	if list[0].Locations[0] != "s3-test" || containsStr(list[0].Locations, locationLocal) {
		t.Fatalf("expected remote-only location, got %v", list[0].Locations)
	}

	got, err := m.GetBackup(b.ID)
	if err != nil {
		t.Fatalf("get remote-only backup: %v", err)
	}
	if got.Size != b.Size {
		t.Fatalf("expected size %d, got %d", b.Size, got.Size)
	}
}

func TestDeleteBackup_RemovesFromLocalAndRemote(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	remote := newFakeStore("s3-test")
	m.SetRemotes([]Store{remote})
	seedDeployment(t, tmpDir, "app")

	b, err := m.CreateBackup(context.Background(), "app", nil)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	if err := m.DeleteBackup(b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if remote.has(backupKey("app", b.ID)) {
		t.Fatal("expected remote copy to be deleted")
	}
	if _, err := m.getLocalBackup(b.ID); err == nil {
		t.Fatal("expected local copy to be deleted")
	}
}

func TestRestoreBackup_FromRemoteWhenLocalMissing(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	remote := newFakeStore("s3-test")
	m.SetRemotes([]Store{remote})
	seedDeployment(t, tmpDir, "app")

	b, err := m.CreateBackup(context.Background(), "app", nil)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if err := m.deleteLocalBackup(b.ID); err != nil {
		t.Fatalf("prune local: %v", err)
	}

	// Wipe the deployment so restore must repopulate it from the remote copy.
	if err := os.RemoveAll(filepath.Join(tmpDir, "app")); err != nil {
		t.Fatalf("remove deployment: %v", err)
	}

	err = m.RestoreBackup(context.Background(), &RestoreBackupRequest{BackupID: b.ID})
	if err != nil {
		t.Fatalf("restore from remote: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "app", "docker-compose.yml")); err != nil {
		t.Fatalf("expected compose file restored from remote: %v", err)
	}
}

func TestListBackups_RemoteListFailureDoesNotBreakLocal(t *testing.T) {
	m, tmpDir := setupTestManager(t)
	defer os.RemoveAll(tmpDir)

	remote := newFakeStore("s3-test")
	remote.failList = true
	m.SetRemotes([]Store{remote})
	seedDeployment(t, tmpDir, "app")

	if _, err := m.CreateBackup(context.Background(), "app", nil); err != nil {
		t.Fatalf("create backup: %v", err)
	}

	list, err := m.ListBackups(&BackupListFilter{DeploymentName: "app"})
	if err != nil {
		t.Fatalf("list should tolerate remote failure: %v", err)
	}
	if len(list) != 1 || !containsStr(list[0].Locations, locationLocal) {
		t.Fatalf("expected local backup despite remote list failure, got %v", list)
	}
}

func TestParseBackupKey(t *testing.T) {
	dep, id, ok := parseBackupKey("app/app_20240101_120000.tar.gz")
	if !ok || dep != "app" || id != "app_20240101_120000" {
		t.Fatalf("parseBackupKey mismatch: dep=%q id=%q ok=%v", dep, id, ok)
	}
	if _, _, ok := parseBackupKey("no-slash.txt"); ok {
		t.Fatal("expected parse failure for malformed key")
	}
}

func TestS3StoreRequiresBucketAndCreds(t *testing.T) {
	if _, err := NewS3Store(S3Config{Name: "x"}); err == nil {
		t.Fatal("expected error without bucket")
	}
	if _, err := NewS3Store(S3Config{Name: "x", Bucket: "b"}); err == nil {
		t.Fatal("expected error without credentials")
	}
	if !errors.Is(ErrObjectNotFound, ErrObjectNotFound) {
		t.Fatal("sentinel identity")
	}
}
