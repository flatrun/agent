package templates

import (
	"bytes"
	"embed"
	"io/fs"
	"net/netip"
	"path/filepath"
	"strings"
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

// sanitizeTrustedProxies keeps only well-formed IP and CIDR entries in their
// canonical form. Anything else is dropped, which both rejects malformed
// config and guarantees the value cannot break out of the Lua string literal
// it is injected into.
func sanitizeTrustedProxies(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(e); err == nil {
			out = append(out, prefix.String())
			continue
		}
		// ParseAddr accepts IPv6 zone IDs that can carry arbitrary characters;
		// a zone is meaningless for proxy trust, so reject those entries
		if addr, err := netip.ParseAddr(e); err == nil && addr.Zone() == "" {
			out = append(out, addr.String())
		}
	}
	return out
}

// LuaTemplateData contains the data for Lua template processing
type LuaTemplateData struct {
	AgentIP          string
	AgentPort        int
	InternalAPIToken string
	TrustedProxies   string
	TrustCFHeader    bool
}

// GetNginxSecurityLuaWithConfig returns the security.lua template processed with agent config
func GetNginxSecurityLuaWithConfig(agentIP string, agentPort int, internalAPIToken string, trustedProxies []string, trustCFHeader bool) ([]byte, error) {
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
		TrustedProxies:   strings.Join(sanitizeTrustedProxies(trustedProxies), ","),
		TrustCFHeader:    trustCFHeader,
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
