package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicesReportMountsReplicasAndDomains(t *testing.T) {
	_, base, httpServer := setupPlanTestServer(t)
	dir := filepath.Join(base, "shop")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  web:
    image: ghcr.io/acme/shop:1.4
    depends_on: [worker, db]
    volumes:
      - ./uploads:/app/uploads
      - ./config.yml:/app/config.yml:ro
  worker:
    image: ghcr.io/acme/shop:1.4
    depends_on:
      db:
        condition: service_healthy
    deploy:
      replicas: 3
  db:
    image: postgres:16
    scale: 1
    volumes:
      - type: bind
        source: ./data
        target: /var/lib/postgresql/data
      - cache:/cache
`
	metadata := `name: shop
type: web
primary_service: web
networking:
  expose: true
  container_port: 80
domains:
  - id: main
    service: ""
    container_port: 80
    domain: shop.example.com
  - id: admin
    service: worker
    container_port: 8080
    domain: jobs.example.com
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "service.yml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(httpServer.URL + "/api/deployments/shop/services")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Services []struct {
			Name      string   `json:"name"`
			IsPrimary bool     `json:"is_primary"`
			Replicas  int      `json:"replicas"`
			Domains   []string `json:"domains"`
			DependsOn []string `json:"depends_on"`
			Mounts    []struct {
				Type     string `json:"type"`
				Source   string `json:"source"`
				Target   string `json:"target"`
				ReadOnly bool   `json:"read_only"`
			} `json:"mounts"`
		} `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, err = %v", resp.StatusCode, err)
	}
	byName := map[string]int{}
	for i, s := range body.Services {
		byName[s.Name] = i
	}
	web := body.Services[byName["web"]]
	if !web.IsPrimary || web.Replicas != 1 || len(web.Domains) != 1 || web.Domains[0] != "shop.example.com" {
		t.Fatalf("web = %+v", web)
	}
	if len(web.Mounts) != 2 || web.Mounts[0].Type != "bind" || web.Mounts[0].Target != "/app/uploads" || !web.Mounts[1].ReadOnly {
		t.Fatalf("web mounts = %+v", web.Mounts)
	}
	if strings.Join(web.DependsOn, ",") != "db,worker" {
		t.Fatalf("web depends on %v", web.DependsOn)
	}
	worker := body.Services[byName["worker"]]
	if strings.Join(worker.DependsOn, ",") != "db" {
		t.Fatalf("worker depends on %v", worker.DependsOn)
	}
	if worker.Replicas != 3 || len(worker.Domains) != 1 || worker.Domains[0] != "jobs.example.com" {
		t.Fatalf("worker = %+v", worker)
	}
	db := body.Services[byName["db"]]
	if len(db.Mounts) != 2 || db.Mounts[0].Source != "./data" || db.Mounts[1].Type != "volume" || db.Mounts[1].Source != "cache" {
		t.Fatalf("db mounts = %+v", db.Mounts)
	}
}
