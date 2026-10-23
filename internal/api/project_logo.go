package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

const (
	logoUploaded       = "upload"
	logoTemplatePrefix = "template:"
)

var logoIconID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*:[a-z0-9]+(-[a-z0-9]+)*$`)

func validateProjectLogo(logo string) error {
	switch {
	case logo == "" || logo == logoUploaded:
		return nil
	case strings.HasPrefix(logo, logoTemplatePrefix):
		if id := strings.TrimPrefix(logo, logoTemplatePrefix); id == "" || strings.Contains(id, "..") {
			return fmt.Errorf("logo %q names no template", logo)
		}
		return nil
	case strings.HasPrefix(logo, "https://") || strings.HasPrefix(logo, "http://"):
		if u, err := url.Parse(logo); err != nil || u.Host == "" {
			return fmt.Errorf("logo %q is not a valid link", logo)
		}
		return nil
	case logoIconID.MatchString(logo):
		return nil
	}
	return fmt.Errorf(`logo must be an icon id such as simple-icons:redis, an http(s) link, "upload", or "template:<id>"`)
}

// The logo sits in the project's own .flatrun directory, which a redeploy from source leaves
// alone, so it moves with the project and survives every deploy.
func (s *Server) projectLogoDir(name string) string {
	return filepath.Join(s.manager.BasePath(), name, ".flatrun")
}

func (s *Server) withProjectLogos(deployments []models.Deployment) {
	for i := range deployments {
		deployments[i].LogoVersion = logoVersion(s.projectLogoDir(deployments[i].Name))
	}
}

func (s *Server) setProjectLogo(c *gin.Context, name string, set func(*models.ServiceMetadata)) bool {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return false
	}
	meta := deployment.Metadata
	if meta == nil {
		meta = &models.ServiceMetadata{Name: name}
	}
	set(meta)
	if err := s.manager.SaveMetadata(name, meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func (s *Server) putProjectLogo(c *gin.Context) {
	name := c.Param("name")
	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	if !saveLogo(c, s.projectLogoDir(name)) {
		return
	}
	if !s.setProjectLogo(c, name, func(m *models.ServiceMetadata) { m.Logo = logoUploaded }) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"logo": logoUploaded, "logo_version": logoVersion(s.projectLogoDir(name))})
}

func (s *Server) getProjectLogo(c *gin.Context) {
	name := c.Param("name")
	if _, err := s.manager.GetDeployment(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	serveLogo(c, s.projectLogoDir(name), "this project has no uploaded logo")
}

func (s *Server) deleteProjectLogo(c *gin.Context) {
	name := c.Param("name")
	cleared := s.setProjectLogo(c, name, func(m *models.ServiceMetadata) {
		if m.Logo == logoUploaded {
			m.Logo = ""
		}
	})
	if cleared {
		removeLogo(c, s.projectLogoDir(name))
	}
}

// templateLogo turns a logo file shipped beside a template into the path that serves it.
// Links and icon ids pass through unchanged.
func templateLogo(id, logo string) string {
	if logo == "" || strings.Contains(logo, ":") {
		return logo
	}
	return "/api/templates/" + id + "/logo"
}

func (s *Server) templateDir(id string) (string, bool) {
	root := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")
	dir := filepath.Join(root, filepath.FromSlash(id))
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return dir, true
}

// getTemplateLogo answers without authentication so the logo can be used as an image source:
// templates and their logos come from the public catalog.
func (s *Server) getTemplateLogo(c *gin.Context) {
	dir, ok := s.templateDir(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	var metadata TemplateMetadata
	content, err := os.ReadFile(filepath.Join(dir, "metadata.yml"))
	if err == nil {
		err = yaml.Unmarshal(content, &metadata)
	}
	if err != nil || metadata.Logo == "" || strings.Contains(metadata.Logo, ":") {
		c.JSON(http.StatusNotFound, gin.H{"error": "this template has no logo file"})
		return
	}
	file := filepath.Join(dir, filepath.FromSlash(metadata.Logo))
	if rel, err := filepath.Rel(dir, file); err != nil || strings.HasPrefix(rel, "..") {
		c.JSON(http.StatusNotFound, gin.H{"error": "this template has no logo file"})
		return
	}
	serveLogoFile(c, file)
}
