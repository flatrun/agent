package docker

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMarshalComposeYAML_KeyOrder(t *testing.T) {
	compose := map[string]interface{}{
		"volumes": map[string]interface{}{
			"data": map[string]interface{}{},
		},
		"networks": map[string]interface{}{
			"web": map[string]interface{}{},
		},
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
		"name": "myproject",
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)

	nameIdx := strings.Index(output, "name:")
	servicesIdx := strings.Index(output, "services:")
	networksIdx := strings.Index(output, "networks:")
	volumesIdx := strings.Index(output, "volumes:")

	if nameIdx == -1 || servicesIdx == -1 || networksIdx == -1 || volumesIdx == -1 {
		t.Fatalf("missing expected keys in output:\n%s", output)
	}

	if nameIdx > servicesIdx {
		t.Errorf("name should come before services")
	}
	if servicesIdx > networksIdx {
		t.Errorf("services should come before networks")
	}
	if networksIdx > volumesIdx {
		t.Errorf("networks should come before volumes")
	}
}

func TestMarshalComposeYAML_ServiceKeyOrder(t *testing.T) {
	compose := map[string]interface{}{
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"networks":    []interface{}{"web"},
				"volumes":     []interface{}{"/data:/data"},
				"ports":       []interface{}{"80:80"},
				"environment": map[string]interface{}{"FOO": "bar"},
				"image":       "nginx",
				"restart":     "always",
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)

	imageIdx := strings.Index(output, "image:")
	envIdx := strings.Index(output, "environment:")
	portsIdx := strings.Index(output, "ports:")
	volumesIdx := strings.Index(output, "volumes:")
	networksIdx := strings.Index(output, "networks:")
	restartIdx := strings.Index(output, "restart:")

	if imageIdx > envIdx {
		t.Errorf("image should come before environment")
	}
	if envIdx > portsIdx {
		t.Errorf("environment should come before ports")
	}
	if portsIdx > volumesIdx {
		t.Errorf("ports should come before volumes")
	}
	if volumesIdx > networksIdx {
		t.Errorf("volumes should come before networks")
	}
	if networksIdx > restartIdx {
		t.Errorf("networks should come before restart")
	}
}

func TestMarshalComposeYAML_PreservesVersion(t *testing.T) {
	compose := map[string]interface{}{
		"version": "3.8",
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if !strings.Contains(output, "version:") {
		t.Errorf("version should be preserved if provided")
	}

	versionIdx := strings.Index(output, "version:")
	servicesIdx := strings.Index(output, "services:")
	if versionIdx > servicesIdx {
		t.Errorf("version should come before services")
	}
}

func TestAddNetworkToCompose_AddsNetwork(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "networks:") {
		t.Errorf("should add networks section")
	}
	if !strings.Contains(result, "proxy") {
		t.Errorf("should add proxy network")
	}
	if !strings.Contains(result, "external: true") {
		t.Errorf("should mark network as external")
	}
}

func TestAddNetworkToCompose_NoDuplicates(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
    networks:
      - proxy
networks:
  proxy:
    external: true
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count := strings.Count(result, "proxy")
	if count != 2 {
		t.Errorf("expected proxy to appear exactly 2 times (service + network definition), got %d", count)
	}
}

func TestAddNetworkToCompose_PreservesUserNetworks(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
    networks:
      - web
networks:
  web: {}
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "web") {
		t.Errorf("should preserve user's web network")
	}
	if !strings.Contains(result, "proxy") {
		t.Errorf("should add proxy network")
	}
}

func TestAddNetworkToCompose_MultipleServices(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
  db:
    image: mysql
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	proxyCount := strings.Count(result, "proxy")
	if proxyCount < 3 {
		t.Errorf("expected proxy to appear at least 3 times (2 services + 1 definition), got %d", proxyCount)
	}
}

func TestAddNetworkToCompose_EmptyNetworkName(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
`
	result, err := AddNetworkToCompose(input, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != input {
		t.Errorf("empty network name should return original content")
	}
}

func TestAddNetworkToCompose_DefinesReferencedNetworks(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
    networks:
      - custom
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "custom") {
		t.Errorf("should preserve custom network reference")
	}
	if !strings.Contains(result, "proxy") {
		t.Errorf("should add proxy network")
	}
}

func TestAddNetworkToCompose_KeyOrder(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nameIdx := strings.Index(result, "name:")
	servicesIdx := strings.Index(result, "services:")
	networksIdx := strings.Index(result, "networks:")

	if nameIdx == -1 || servicesIdx == -1 || networksIdx == -1 {
		t.Fatalf("missing expected keys in output:\n%s", result)
	}

	if nameIdx > servicesIdx {
		t.Errorf("name should come before services")
	}
	if servicesIdx > networksIdx {
		t.Errorf("services should come before networks")
	}
}

func TestAddNetworkToCompose_ComplexStructure(t *testing.T) {
	input := `name: wordpress
services:
  app:
    image: wordpress
    environment:
      DB_HOST: db
    ports:
      - "8080:80"
    volumes:
      - wp_data:/var/www/html
    depends_on:
      - db
  db:
    image: mysql:8
    environment:
      MYSQL_ROOT_PASSWORD: secret
    volumes:
      - db_data:/var/lib/mysql
volumes:
  wp_data: {}
  db_data: {}
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	requiredStrings := []string{
		"name:",
		"services:",
		"app:",
		"db:",
		"image:",
		"environment:",
		"ports:",
		"volumes:",
		"depends_on:",
		"networks:",
		"proxy",
		"wp_data:",
		"db_data:",
	}

	for _, s := range requiredStrings {
		if !strings.Contains(result, s) {
			t.Errorf("missing required string %q in output:\n%s", s, result)
		}
	}
}

func TestParseComposeYAML(t *testing.T) {
	input := `name: test
services:
  app:
    image: nginx
`
	compose, err := ParseComposeYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if compose["name"] != "test" {
		t.Errorf("expected name to be 'test', got %v", compose["name"])
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected services to be a map")
	}

	app, ok := services["app"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected app to be a map")
	}

	if app["image"] != "nginx" {
		t.Errorf("expected image to be 'nginx', got %v", app["image"])
	}
}

func TestMarshalComposeYAML_NoVersion(t *testing.T) {
	compose := map[string]interface{}{
		"name": "myapp",
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if strings.Contains(output, "version:") {
		t.Errorf("should not add version if not provided")
	}
}

func TestAddNetworkToCompose_MapStyleNetworks(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
    networks:
      web:
        aliases:
          - myalias
`
	result, err := AddNetworkToCompose(input, "proxy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "proxy") {
		t.Errorf("should add proxy network")
	}
	if !strings.Contains(result, "web") {
		t.Errorf("should preserve web network")
	}
}

func TestMarshalComposeYAML_WithSecrets(t *testing.T) {
	compose := map[string]interface{}{
		"name": "myapp",
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
		"secrets": map[string]interface{}{
			"db_password": map[string]interface{}{
				"file": "./secrets/db_password.txt",
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if !strings.Contains(output, "secrets:") {
		t.Errorf("should preserve secrets section")
	}

	servicesIdx := strings.Index(output, "services:")
	secretsIdx := strings.Index(output, "secrets:")
	if servicesIdx > secretsIdx {
		t.Errorf("services should come before secrets")
	}
}

func TestMarshalComposeYAML_WithConfigs(t *testing.T) {
	compose := map[string]interface{}{
		"name": "myapp",
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
		"configs": map[string]interface{}{
			"nginx_config": map[string]interface{}{
				"file": "./nginx.conf",
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if !strings.Contains(output, "configs:") {
		t.Errorf("should preserve configs section")
	}

	servicesIdx := strings.Index(output, "services:")
	configsIdx := strings.Index(output, "configs:")
	if servicesIdx > configsIdx {
		t.Errorf("services should come before configs")
	}
}

func TestMarshalComposeYAML_UnknownTopLevelKeys(t *testing.T) {
	compose := map[string]interface{}{
		"name": "myapp",
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image": "nginx",
			},
		},
		"x-custom": map[string]interface{}{
			"key": "value",
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if !strings.Contains(output, "x-custom:") {
		t.Errorf("should preserve custom extension fields")
	}
}

func TestMarshalComposeYAML_UnknownServiceKeys(t *testing.T) {
	compose := map[string]interface{}{
		"services": map[string]interface{}{
			"app": map[string]interface{}{
				"image":      "nginx",
				"x-priority": 10,
			},
		},
	}

	result, err := MarshalComposeYAML(compose)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := string(result)
	if !strings.Contains(output, "x-priority:") {
		t.Errorf("should preserve custom service extension fields")
	}
}

func TestEnsureContainerNames_AddsToAppService(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
`
	result, err := EnsureContainerNames(input, "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "container_name: my-deployment") {
		t.Errorf("should add container_name to app service, got:\n%s", result)
	}
}

func TestEnsureContainerNames_NamesAllServices(t *testing.T) {
	input := `name: myapp
services:
  web:
    image: nginx
  app:
    image: node
  db:
    image: postgres
`
	result, err := EnsureContainerNames(input, "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var compose map[string]interface{}
	if err := yaml.Unmarshal([]byte(result), &compose); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	services := compose["services"].(map[string]interface{})
	appService := services["app"].(map[string]interface{})
	webService := services["web"].(map[string]interface{})
	dbService := services["db"].(map[string]interface{})

	if appService["container_name"] != "my-deployment" {
		t.Errorf("app service should have container_name 'my-deployment', got: %v", appService["container_name"])
	}
	if webService["container_name"] != "my-deployment-web" {
		t.Errorf("web service should have container_name 'my-deployment-web', got: %v", webService["container_name"])
	}
	if dbService["container_name"] != "my-deployment-db" {
		t.Errorf("db service should have container_name 'my-deployment-db', got: %v", dbService["container_name"])
	}
}

func TestEnsureContainerNames_FallsBackToFirstService(t *testing.T) {
	input := `name: myapp
services:
  web:
    image: nginx
`
	result, err := EnsureContainerNames(input, "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "container_name: my-deployment") {
		t.Errorf("should add container_name to first service, got:\n%s", result)
	}
}

func TestEnsureContainerNames_PreservesExisting(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
    container_name: custom-name
  db:
    image: postgres
    container_name: custom-db
`
	result, err := EnsureContainerNames(input, "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "container_name: custom-name") {
		t.Errorf("should preserve existing container_name on app, got:\n%s", result)
	}
	if !strings.Contains(result, "container_name: custom-db") {
		t.Errorf("should preserve existing container_name on db, got:\n%s", result)
	}
}

func TestEnsureContainerNames_EmptyName(t *testing.T) {
	input := `name: myapp
services:
  app:
    image: nginx
`
	result, err := EnsureContainerNames(input, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != input {
		t.Errorf("empty deployment name should return unchanged content")
	}
}

func TestEnsureContainerNames_NoServices(t *testing.T) {
	input := `name: myapp
`
	result, err := EnsureContainerNames(input, "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != input {
		t.Errorf("no services should return unchanged content")
	}
}
