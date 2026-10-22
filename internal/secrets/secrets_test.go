package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	k, err := LoadKeyring(filepath.Join(dir, "secrets.key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets.json")
	return NewStore(path, k), dir
}

func TestKeyringPersistsAndSeals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "secrets.key")
	k1, err := LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v, %v", info.Mode(), err)
	}
	sealed, err := k1.Seal("hunter2")
	if err != nil || !IsSealed(sealed) || strings.Contains(sealed, "hunter2") {
		t.Fatalf("sealed = %q, %v", sealed, err)
	}
	k2, err := LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := k2.Open(sealed); err != nil || plain != "hunter2" {
		t.Fatalf("reopened = %q, %v", plain, err)
	}
	other, _ := LoadKeyring(filepath.Join(dir, "other.key"))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a different key opened the value")
	}
	if _, err := k1.Open("plain"); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("plain value error = %v", err)
	}
}

func TestStoreKeepsValuesSealedOnDisk(t *testing.T) {
	s, dir := newTestStore(t)
	scope := Scope{Type: ScopeDeployment, ID: "shop"}
	sec, err := s.Set(scope, "DB_PASSWORD", "correct-horse")
	if err != nil || sec.Version != 1 {
		t.Fatalf("set = %+v, %v", sec, err)
	}
	if sec, _ = s.Set(scope, "DB_PASSWORD", "battery-staple"); sec.Version != 2 {
		t.Fatalf("version after update = %d", sec.Version)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if strings.Contains(string(raw), "battery-staple") || strings.Contains(string(raw), "correct-horse") {
		t.Fatalf("plain value on disk: %s", raw)
	}
	values, err := s.Values(scope)
	if err != nil || values["DB_PASSWORD"] != "battery-staple" {
		t.Fatalf("values = %v, %v", values, err)
	}
	listed, _ := json.Marshal(mustList(t, s, scope))
	if strings.Contains(string(listed), "battery-staple") || strings.Contains(string(listed), "enc:v1") {
		t.Fatalf("listing exposes the value: %s", listed)
	}
	if _, err := s.Set(scope, "9LIVES", "x"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid name error = %v", err)
	}
}

func mustList(t *testing.T, s *Store, scope Scope) []Secret {
	t.Helper()
	list, err := s.List(scope)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestRotationGeneratesANewValue(t *testing.T) {
	s, _ := newTestStore(t)
	scope := Scope{Type: ScopeWorkspace, ID: "team"}
	if _, err := s.Set(scope, "API_KEY", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRotation(scope, "API_KEY", &Rotation{Schedule: "@monthly", Generator: Generator{Length: 40, Charset: CharsetHex}}); err != nil {
		t.Fatal(err)
	}
	sec, err := s.Rotate(scope, "API_KEY")
	if err != nil || sec.Version != 2 || sec.LastRotatedAt == nil {
		t.Fatalf("rotate = %+v, %v", sec, err)
	}
	values, _ := s.Values(scope)
	if v := values["API_KEY"]; len(v) != 40 || strings.Trim(v, "0123456789abcdef") != "" {
		t.Fatalf("rotated value = %q", v)
	}
	if rotating, _ := s.Rotating(); len(rotating) != 1 {
		t.Fatalf("rotating = %v", rotating)
	}
	if _, err := s.SetRotation(scope, "API_KEY", &Rotation{Schedule: "@daily", Generator: Generator{Length: 4}}); err == nil {
		t.Fatal("a 4 character generator was accepted")
	}
}

func TestParseScope(t *testing.T) {
	for _, ok := range []string{"workspace:team", "deployment:shop", "service:shop/web"} {
		if _, err := ParseScope(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"shop", "deployment:", "service:shop", "planet:x", "deployment:a/b"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestDropScopeRemovesServiceSecretsOfADeployment(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Set(Scope{ScopeDeployment, "shop"}, "A", "1")
	_, _ = s.Set(Scope{ScopeService, "shop/web"}, "B", "2")
	_, _ = s.Set(Scope{ScopeDeployment, "blog"}, "C", "3")
	if err := s.DropScope(Scope{ScopeDeployment, "shop"}); err != nil {
		t.Fatal(err)
	}
	if len(mustList(t, s, Scope{ScopeService, "shop/web"})) != 0 || len(mustList(t, s, Scope{ScopeDeployment, "blog"})) != 1 {
		t.Fatal("drop removed the wrong secrets")
	}
}

// The fake servers below answer with the response shapes documented by each
// provider's API reference.

func TestInfisicalLogsInThenListsAFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/universal-auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || body["clientId"] != "cid" || body["clientSecret"] != "csecret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"tok","expiresIn":7200,"accessTokenMaxTTL":43200,"tokenType":"Bearer"}`))
		case "/api/v4/secrets":
			q := r.URL.Query()
			if r.Header.Get("Authorization") != "Bearer tok" || q.Get("projectId") != "p1" || q.Get("environment") != "prod" || q.Get("secretPath") != "/api" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"secrets":[{"id":"1","_id":"1","workspace":"p1","environment":"prod","version":3,"type":"shared","secretKey":"DB_URL","secretValue":"postgres://db","secretComment":"","secretValueHidden":false,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","isRotatedSecret":false}],"imports":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	got, err := Infisical{Client: srv.Client()}.Fetch(context.Background(),
		map[string]string{"site_url": srv.URL, "client_id": "cid", "client_secret": "csecret"},
		map[string]string{"project_id": "p1", "environment": "prod", "secret_path": "/api"})
	if err != nil || got["DB_URL"] != "postgres://db" || len(got) != 1 {
		t.Fatalf("infisical = %v, %v", got, err)
	}
}

func TestVaultReadsKVv2Data(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kv/data/apps/shop" || r.Header.Get("X-Vault-Token") != "vt" || r.Header.Get("X-Vault-Namespace") != "team" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"foo":"bar","port":5432},"metadata":{"created_time":"2018-03-22T02:24:06.945319214Z","deletion_time":"","destroyed":false,"version":2}}}`))
	}))
	defer srv.Close()
	got, err := Vault{Client: srv.Client()}.Fetch(context.Background(),
		map[string]string{"address": srv.URL + "/", "token": "vt", "namespace": "team"},
		map[string]string{"mount": "kv", "path": "/apps/shop"})
	if err != nil || got["foo"] != "bar" || got["port"] != "5432" {
		t.Fatalf("vault = %v, %v", got, err)
	}
}

func TestDopplerDownloadsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/configs/config/secrets/download" || r.URL.Query().Get("format") != "json" || r.Header.Get("Authorization") != "Bearer dp-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"ALGOLIA":"N9TOPUCTO","DATABASE":"brian@aws.dynamodb.com:9876","USER":"brian"}`))
	}))
	defer srv.Close()
	got, err := Doppler{Client: srv.Client()}.Fetch(context.Background(),
		map[string]string{"token": "dp-token", "api_url": srv.URL}, nil)
	if err != nil || got["USER"] != "brian" || len(got) != 3 {
		t.Fatalf("doppler = %v, %v", got, err)
	}
}

func TestOnePasswordUsesFieldLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/vaults/v1/items/i1" || r.Header.Get("Authorization") != "Bearer op" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"id":"i1","title":"Shop","vault":{"id":"v1"},"category":"LOGIN","fields":[{"id":"username","type":"STRING","purpose":"USERNAME","label":"username","value":"jane"},{"id":"password","type":"CONCEALED","purpose":"PASSWORD","label":"password","value":"pw"},{"id":"notesPlain","type":"STRING","purpose":"NOTES","label":"notesPlain"},{"id":"x","type":"URL","label":"web site","value":"https://example.org"}]}`))
	}))
	defer srv.Close()
	got, err := OnePassword{Client: srv.Client()}.Fetch(context.Background(),
		map[string]string{"connect_url": srv.URL, "token": "op"},
		map[string]string{"vault_id": "v1", "item_id": "i1"})
	if err != nil || got["username"] != "jane" || got["password"] != "pw" || len(got) != 2 {
		t.Fatalf("1password = %v, %v", got, err)
	}
}

func TestProviderErrorsNameTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Vault{Client: srv.Client()}.Fetch(context.Background(), map[string]string{"address": srv.URL}, map[string]string{"path": "x"})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolverLetsNarrowScopesWin(t *testing.T) {
	s, _ := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"SHARED":"from-doppler","REGION":"eu"}`))
	}))
	defer srv.Close()
	ws := Scope{ScopeWorkspace, "team"}
	dep := Scope{ScopeDeployment, "shop"}
	_, _ = s.AddBinding(Binding{Scope: ws, Provider: "doppler", ConnectionID: "c1"})
	_, _ = s.Set(ws, "SHARED", "from-workspace")
	_, _ = s.Set(ws, "TOKEN", "workspace-token")
	_, _ = s.Set(dep, "TOKEN", "shop-token")

	r := Resolver{
		Store:    s,
		Registry: NewRegistry(Doppler{Client: srv.Client()}),
		Connections: func(id string) (string, map[string]string, error) {
			return "doppler", map[string]string{"token": "t", "api_url": srv.URL}, nil
		},
	}
	got, err := r.Values(context.Background(), []Scope{ws, dep})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"SHARED": "from-workspace", "REGION": "eu", "TOKEN": "shop-token"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
