package webhook

import (
	"net/http"
	"os"
	"testing"
)

func TestWorkflowEvent(t *testing.T) {
	body, err := os.ReadFile("testdata/queued.payload.json")
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"X-Github-Event": []string{"workflow_job"}, "X-Github-Delivery": []string{"published-delivery"}}
	e, _, err := Normalize(GitHub{}, "ci", headers, body)
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != "github.workflow_job.queued" || e.Attribute("subject") != "Codertocat/Hello-World" || e.Attributes["installation_id"] != int64(3456996) || e.Attributes["branch"] != "main" || e.Attributes["job_id"] != "289782451" || e.ID != "published-delivery" {
		t.Fatalf("event: %+v", e)
	}
	if !e.Matches("github/ci", e.Type, map[string]string{"branch": "main"}, []string{"ubuntu-latest"}) {
		t.Fatal("matching filters rejected")
	}
	for _, filter := range [][]string{{"self-hosted"}, {"ubuntu-latest", "flatrun"}} {
		if e.Matches("github/ci", e.Type, nil, filter) {
			t.Fatal("missing label matched")
		}
	}
	if e.Matches("github/elsewhere", e.Type, nil, nil) || e.Matches("", "github.workflow_job.completed", nil, nil) || e.Matches("", "", map[string]string{"branch": "other"}, nil) {
		t.Fatal("incorrect filter matched")
	}
}

func TestGenericRepositoryPreserved(t *testing.T) {
	e, _, err := Normalize(Generic{}, "ci", http.Header{}, []byte(`{"subject":"subject","attributes":{"repository":"explicit-repository"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Attribute("repository") != "explicit-repository" {
		t.Fatalf("repository: %v", e.Attributes["repository"])
	}
}
