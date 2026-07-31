package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flatrun/agent/internal/credentials"
	"github.com/gin-gonic/gin"
)

func TestSourceCredentials_CreateThenList(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{credentialsManager: credentials.NewManager(t.TempDir())}
	router := gin.New()
	router.GET("/source-credentials", s.listSourceCredentials)
	router.POST("/source-credentials", s.createSourceCredential)

	body, _ := json.Marshal(map[string]string{"name": "gh", "token": "secret-pat"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/source-credentials", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/source-credentials", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Credentials []struct {
			Name string            `json:"name"`
			Data map[string]string `json:"data"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Credentials) != 1 || resp.Credentials[0].Name != "gh" {
		t.Fatalf("expected one credential named gh, got %+v", resp.Credentials)
	}
	// The token must be masked in the listing, never returned in the clear.
	if resp.Credentials[0].Data["token"] == "secret-pat" {
		t.Error("token was returned unmasked in the listing")
	}
}

func TestCreateSourceCredential_RequiresToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := &Server{credentialsManager: credentials.NewManager(t.TempDir())}
	router := gin.New()
	router.POST("/source-credentials", s.createSourceCredential)

	body, _ := json.Marshal(map[string]string{"name": "gh"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/source-credentials", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a credential with no token, got %d", w.Code)
	}
}
