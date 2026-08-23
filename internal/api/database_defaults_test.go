package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDatabaseListUsesRegisteredConnectionWhenBodyIsEmpty(t *testing.T) {
	server, _, httpServer := setupPlanTestServer(t)
	server.config.Infrastructure.Database.Enabled = true
	server.config.Infrastructure.Database.Type = "postgres"
	server.config.Infrastructure.Database.Host = "127.0.0.1"
	server.config.Infrastructure.Database.Port = 1
	server.config.Infrastructure.Database.RootUser = "postgres"
	server.config.Infrastructure.Database.RootPassword = "not-a-secret"

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/databases/list", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %#v", response.StatusCode, body)
	}
	message, _ := body["error"].(string)
	if !strings.Contains(message, "127.0.0.1") || strings.Contains(message, "[::1]:0") {
		t.Fatalf("error did not use the registered database: %q", message)
	}
}
