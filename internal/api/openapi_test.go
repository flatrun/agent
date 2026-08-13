package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal(openAPISpec, &spec); err != nil {
		t.Fatalf("the embedded spec must be valid JSON: %v", err)
	}
	return spec
}

func TestOpenAPISpecIsServed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{}
	router := gin.New()
	router.GET("/openapi.json", server.getOpenAPISpec)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var spec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("the served spec must be valid JSON: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v", spec["openapi"])
	}
}

// A spec that describes an endpoint the agent does not serve, or misses one it does, is worse
// than no spec, since a caller trusts it.
func TestOpenAPISpecMatchesTheRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("reruns the generator")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	regenerated := filepath.Join(t.TempDir(), "openapi.json")
	cmd := exec.Command("go", "run", "./tools/genspec", "-o", regenerated)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("regenerating the spec failed: %v: %s", err, out)
	}

	fresh, err := os.ReadFile(regenerated)
	if err != nil {
		t.Fatal(err)
	}
	if string(fresh) != string(openAPISpec) {
		t.Error("the committed spec is out of date; run: go run ./tools/genspec -o internal/api/openapi.json")
	}
}

func TestOpenAPISpecCarriesWhatACallerNeeds(t *testing.T) {
	spec := loadSpec(t)
	paths, ok := spec["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("the spec describes no paths")
	}

	// A request body, so a caller knows what to send rather than guessing field names.
	backups, ok := paths["/api/backups"].(map[string]any)
	if !ok {
		t.Fatal("/api/backups is missing")
	}
	post, ok := backups["post"].(map[string]any)
	if !ok {
		t.Fatal("POST /api/backups is missing")
	}
	body, ok := post["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("POST /api/backups has no request body")
	}
	ref := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
	if ref == nil {
		t.Fatal("the request body has no schema")
	}

	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	name := ref.(string)[len("#/components/schemas/"):]
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("%s is referenced but not described", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["deployment_name"] == nil {
		t.Fatalf("the schema should carry the fields the handler binds, got %v", schema)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) == 0 {
		t.Error("a field tagged binding:required should be required in the spec")
	}

	// The permission, so a caller can tell what a key needs before it is refused.
	if post["x-permission"] == nil {
		t.Error("the operation should carry the permission it is gated on")
	}
}
