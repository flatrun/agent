package templates

import (
	"bytes"
	"embed"
	"io/fs"
	"path/filepath"
	"text/template"
)

//go:embed */metadata.yml */docker-compose.yml
//go:embed infra/*/metadata.yml infra/*/docker-compose.yml
//go:embed infra/nginx/nginx.conf infra/nginx/nginx.lua.conf infra/nginx/default.conf infra/nginx/lua/*
//go:embed welcome/index.html
var FS embed.FS

var Categories = []Category{
	{ID: "application", Name: "Applications", Icon: "pi pi-desktop", Priority: 100},
	{ID: "framework", Name: "Frameworks", Icon: "pi pi-code", Priority: 90},
	{ID: "runtime", Name: "Runtimes", Icon: "pi pi-cog", Priority: 80},
	{ID: "infrastructure", Name: "Infrastructure", Icon: "pi pi-server", Priority: 70},
	{ID: "database", Name: "Databases", Icon: "pi pi-database", Priority: 60},
	{ID: "basic", Name: "Basic", Icon: "pi pi-file", Priority: 50},
}

type Category struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Priority int    `json:"priority"`
}

func List() ([]string, error) {
	var templates []string

	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if entry.Name() == "infra" {
				infraTemplates, err := listInfraTemplates()
				if err == nil {
					templates = append(templates, infraTemplates...)
				}
			} else {
				templates = append(templates, entry.Name())
			}
		}
	}
	return templates, nil
}

func listInfraTemplates() ([]string, error) {
	var templates []string
	entries, err := fs.ReadDir(FS, "infra")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			templates = append(templates, "infra/"+entry.Name())
		}
	}
	return templates, nil
}

func GetMetadata(name string) ([]byte, error) {
	return FS.ReadFile(filepath.Join(name, "metadata.yml"))
}

func GetCompose(name string) ([]byte, error) {
	return FS.ReadFile(filepath.Join(name, "docker-compose.yml"))
}

func GetFile(templateID, filename string) ([]byte, error) {
	return FS.ReadFile(filepath.Join(templateID, filename))
}

func GetWelcomePage() ([]byte, error) {
	return FS.ReadFile("welcome/index.html")
}

func GetCategories() []Category {
	return Categories
}

type NginxConfigData struct {
	RejectUnknownDomains bool
}

func GetNginxConfig(luaEnabled bool) ([]byte, error) {
	if luaEnabled {
		return FS.ReadFile("infra/nginx/nginx.lua.conf")
	}
	return FS.ReadFile("infra/nginx/nginx.conf")
}

func GetNginxConfigWithData(luaEnabled bool, data NginxConfigData) ([]byte, error) {
	var content []byte
	var err error

	if luaEnabled {
		content, err = FS.ReadFile("infra/nginx/nginx.lua.conf")
	} else {
		content, err = FS.ReadFile("infra/nginx/nginx.conf")
	}
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("nginx.conf").Parse(string(content))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func GetNginxSecurityLua() ([]byte, error) {
	return FS.ReadFile("infra/nginx/lua/security.lua")
}

// LuaTemplateData contains the data for Lua template processing
type LuaTemplateData struct {
	AgentIP          string
	AgentPort        int
	InternalAPIToken string
}

// GetNginxSecurityLuaWithConfig returns the security.lua template processed with agent config
func GetNginxSecurityLuaWithConfig(agentIP string, agentPort int, internalAPIToken string) ([]byte, error) {
	content, err := FS.ReadFile("infra/nginx/lua/security.lua")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("security.lua").Parse(string(content))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	data := LuaTemplateData{
		AgentIP:          agentIP,
		AgentPort:        agentPort,
		InternalAPIToken: internalAPIToken,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// GetNginxTrafficLuaWithConfig returns the traffic.lua template processed with agent config
func GetNginxTrafficLuaWithConfig(agentIP string, agentPort int) ([]byte, error) {
	content, err := FS.ReadFile("infra/nginx/lua/traffic.lua")
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("traffic.lua").Parse(string(content))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	data := LuaTemplateData{
		AgentIP:   agentIP,
		AgentPort: agentPort,
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
