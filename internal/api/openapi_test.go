package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

// A description a client cannot resolve is worse than none, since the client trusts it. These
// are the faults a generator can introduce that still produce parseable JSON.
func TestOpenAPISpecIsStructurallySound(t *testing.T) {
	spec := loadSpec(t)

	schemas, ok := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the spec describes no schemas")
	}

	var refs []string
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				refs = append(refs, ref)
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(spec)

	if len(refs) == 0 {
		t.Fatal("no schema is referenced, so nothing describes a body")
	}
	for _, ref := range refs {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if name == ref {
			t.Errorf("%s does not point into the schemas", ref)
			continue
		}
		if _, ok := schemas[name]; !ok {
			t.Errorf("%s is referenced but not described", ref)
		}
	}

	seen := map[string]string{}
	for path, methods := range spec["paths"].(map[string]any) {
		if !strings.HasPrefix(path, "/api/") {
			t.Errorf("%s is not reachable: every route is served under /api", path)
		}
		for method, raw := range methods.(map[string]any) {
			op, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s %s is not an operation", method, path)
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s %s has no operationId, which client generators key on", method, path)
				continue
			}
			if previous, clash := seen[id]; clash {
				t.Errorf("operationId %s is used by both %s and %s %s", id, previous, method, path)
			}
			seen[id] = method + " " + path
			if op["responses"] == nil {
				t.Errorf("%s %s describes no response", method, path)
			}
		}
	}
}

// A permission is a string the agent checks, not a rewording of a constant's name, and a path
// OpenAPI cannot express is a path no client can call.
func TestOpenAPISpecNamesPermissionsAndPathsExactly(t *testing.T) {
	spec := loadSpec(t)

	for path, methods := range spec["paths"].(map[string]any) {
		if strings.ContainsAny(path, "*:") {
			t.Errorf("%s is not a path a client can build", path)
		}
		for _, raw := range methods.(map[string]any) {
			op := raw.(map[string]any)
			permission, ok := op["x-permission"].(string)
			if !ok {
				continue
			}
			resource, _, found := strings.Cut(permission, ":")
			if !found || len(resource) < 3 {
				t.Errorf("%s reads as a mangled permission on %s", permission, path)
			}
		}
	}

	// The wildcard segment carries the rest of the path, and a caller has to be told about it.
	files, ok := spec["paths"].(map[string]any)["/api/deployments/{name}/files/{path}"].(map[string]any)
	if !ok {
		t.Fatal("the file endpoint is missing, so wildcards are not being translated")
	}
	var named []string
	for _, param := range files["get"].(map[string]any)["parameters"].([]any) {
		named = append(named, param.(map[string]any)["name"].(string))
	}
	if !slices.Contains(named, "path") {
		t.Errorf("the wildcard is not described as a parameter, got %v", named)
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
