package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeDatabaseEnvironmentPreservesUnrelatedValues(t *testing.T) {
	got := mergeEnvVars(
		[]EnvVar{{Key: "APP_ENV", Value: "production"}, {Key: "DB_HOST", Value: "old"}},
		[]EnvVar{{Key: "DB_HOST", Value: "database"}, {Key: "DB_NAME", Value: "shop"}},
	)
	want := map[string]string{"APP_ENV": "production", "DB_HOST": "database", "DB_NAME": "shop"}
	for _, item := range got {
		if want[item.Key] != item.Value {
			t.Fatalf("unexpected environment value: %#v", got)
		}
		delete(want, item.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing environment values: %#v", want)
	}
}

func TestReadDeploymentEnvPreservesImportedValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_ENV=production\nDB_HOST=imported\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.flatrun"), []byte("DB_HOST=managed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readDeploymentEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"APP_ENV": "production", "DB_HOST": "managed"}
	for _, item := range got {
		if want[item.Key] != item.Value {
			t.Fatalf("unexpected environment value: %#v", got)
		}
		delete(want, item.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing environment values: %#v", want)
	}
}
