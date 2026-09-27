package api

import "testing"

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
