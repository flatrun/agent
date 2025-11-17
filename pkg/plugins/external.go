package plugins

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/gin-gonic/gin"
)

type ExternalPlugin struct {
	info     PluginInfo
	basePath string
	config   map[string]interface{}
}

func (p *ExternalPlugin) Info() PluginInfo {
	return p.info
}

func (p *ExternalPlugin) Initialize(config map[string]interface{}) error {
	p.config = config
	return nil
}

func (p *ExternalPlugin) Start() error {
	return nil
}

func (p *ExternalPlugin) Stop() error {
	return nil
}

func (p *ExternalPlugin) GetCapabilities() []Capability {
	return []Capability{}
}

func (p *ExternalPlugin) RegisterRoutes(router *gin.RouterGroup) error {
	return nil
}

func (p *ExternalPlugin) GetWidgetData(deploymentName string) (interface{}, error) {
	return map[string]interface{}{
		"type":   p.info.Type,
		"plugin": p.info.Name,
		"name":   deploymentName,
	}, nil
}

func (p *ExternalPlugin) CreateDeployment(name string, config map[string]interface{}) (*DeploymentResult, error) {
	compose, err := p.GetDockerCompose(config)
	if err != nil {
		return nil, err
	}

	deployPath := filepath.Join(p.basePath, "..", "..", name)
	if err := os.MkdirAll(deployPath, 0755); err != nil {
		return nil, err
	}

	composePath := filepath.Join(deployPath, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(compose), 0644); err != nil {
		return nil, err
	}

	return &DeploymentResult{
		Name: name,
		Path: deployPath,
	}, nil
}

func (p *ExternalPlugin) ConfigureDeployment(name string, config map[string]interface{}) error {
	return nil
}

func (p *ExternalPlugin) GetDeploymentStatus(name string) (*DeploymentStatus, error) {
	return &DeploymentStatus{
		Name:   name,
		Status: "unknown",
		Health: "unknown",
	}, nil
}

func (p *ExternalPlugin) GetDockerCompose(config map[string]interface{}) (string, error) {
	templatePath := filepath.Join(p.basePath, "templates", "docker-compose.yml")

	data, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("compose").Parse(string(data))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (p *ExternalPlugin) GetNginxConfig(config map[string]interface{}) (string, error) {
	templatePath := filepath.Join(p.basePath, "templates", "nginx.conf")

	data, err := os.ReadFile(templatePath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("nginx").Parse(string(data))
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (p *ExternalPlugin) RunHook(hookName string, env map[string]string) error {
	hookPath := filepath.Join(p.basePath, "hooks", hookName+".sh")

	if _, err := os.Stat(hookPath); os.IsNotExist(err) {
		return nil
	}

	cmd := exec.Command("sh", hookPath)
	cmd.Dir = p.basePath

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	return cmd.Run()
}
