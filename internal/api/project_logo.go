package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// An uploaded logo sits in the project's own .flatrun directory, which a redeploy from source
// leaves alone, so it moves with the project and survives every deploy.
const projectLogoDir = ".flatrun"

func (s *Server) projectDir(name string) string {
	return filepath.Join(s.manager.BasePath(), name)
}

// projectLogoFile returns the file the project's logo names, when it names one inside the
// project.
func (s *Server) projectLogoFile(name string, logo models.Logo) (string, bool) {
	file, ok := logo.File()
	if !ok || logo.Validate() != nil {
		return "", false
	}
	return containedFile(s.projectDir(name), file)
}

func (s *Server) projectLogoVersion(deployment *models.Deployment) string {
	if deployment.Metadata == nil {
		return ""
	}
	file, ok := s.projectLogoFile(deployment.Name, deployment.Metadata.Logo)
	if !ok {
		return ""
	}
	info, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(info.ModTime().UnixNano(), 36)
}

func (s *Server) withProjectLogos(deployments []models.Deployment) {
	for i := range deployments {
		deployments[i].LogoVersion = s.projectLogoVersion(&deployments[i])
	}
}

func (s *Server) setProjectLogo(c *gin.Context, name string, logo models.Logo) bool {
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return false
	}
	meta := deployment.Metadata
	if meta == nil {
		meta = &models.ServiceMetadata{Name: name}
	}
	meta.Logo = logo
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
	file, ok := saveLogo(c, filepath.Join(s.projectDir(name), projectLogoDir))
	if !ok {
		return
	}
	logo := models.Logo(path.Join(projectLogoDir, file))
	if !s.setProjectLogo(c, name, logo) {
		return
	}
	deployment := models.Deployment{Name: name, Metadata: &models.ServiceMetadata{Logo: logo}}
	c.JSON(http.StatusOK, gin.H{"logo": logo, "logo_version": s.projectLogoVersion(&deployment)})
}

func (s *Server) getProjectLogo(c *gin.Context) {
	name := c.Param("name")
	deployment, err := s.manager.GetDeployment(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Deployment not found"})
		return
	}
	var logo models.Logo
	if deployment.Metadata != nil {
		logo = deployment.Metadata.Logo
	}
	file, ok := s.projectLogoFile(name, logo)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "this project's logo is not a file"})
		return
	}
	serveLogoFile(c, file)
}

// deleteProjectLogo clears the project's logo, whatever its form, and removes an uploaded one.
func (s *Server) deleteProjectLogo(c *gin.Context) {
	name := c.Param("name")
	if s.setProjectLogo(c, name, "") {
		removeLogo(c, filepath.Join(s.projectDir(name), projectLogoDir))
	}
}

// copyTemplateLogo gives a project created from a template its own copy of the template's
// logo, so the project keeps it when the catalog changes or cannot be reached.
func (s *Server) copyTemplateLogo(name, templateID string) models.Logo {
	dir, ok := s.templateDir(templateID)
	if !ok {
		return ""
	}
	metadata, err := readTemplateMetadata(dir)
	if err != nil || metadata.Logo.Validate() != nil {
		return ""
	}
	file, isFile := metadata.Logo.File()
	if !isFile {
		return metadata.Logo
	}
	source, ok := containedFile(dir, file)
	if !ok {
		return ""
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLogoBytes {
		return ""
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return ""
	}
	ext, ok := logoExtensions[logoContentType(data)]
	if !ok || writeLogo(filepath.Join(s.projectDir(name), projectLogoDir), ext, data) != nil {
		return ""
	}
	return models.Logo(path.Join(projectLogoDir, "logo"+ext))
}

// templateLogo turns a logo file shipped beside a template into the path that serves it.
// Links and icon ids pass through unchanged.
func templateLogo(id string, logo models.Logo) string {
	if _, ok := logo.File(); ok {
		return "/api/templates/" + id + "/logo"
	}
	return string(logo)
}

func (s *Server) templateDir(id string) (string, bool) {
	root := filepath.Join(s.config.DeploymentsPath, ".flatrun", "templates")
	dir := filepath.Join(root, filepath.FromSlash(id))
	if rel, err := filepath.Rel(root, dir); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return dir, true
}

func readTemplateMetadata(dir string) (*TemplateMetadata, error) {
	content, err := os.ReadFile(filepath.Join(dir, "metadata.yml"))
	if err != nil {
		return nil, err
	}
	var metadata TemplateMetadata
	if err := yaml.Unmarshal(content, &metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

// getTemplateLogo answers without authentication so the logo can be used as an image source:
// templates and their logos come from the public catalog.
func (s *Server) getTemplateLogo(c *gin.Context) {
	dir, ok := s.templateDir(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "template not found"})
		return
	}
	metadata, err := readTemplateMetadata(dir)
	if err != nil || metadata.Logo.Validate() != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "this template has no logo file"})
		return
	}
	logoFile, isFile := metadata.Logo.File()
	if !isFile {
		c.JSON(http.StatusNotFound, gin.H{"error": "this template has no logo file"})
		return
	}
	file, ok := containedFile(dir, logoFile)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "this template has no logo file"})
		return
	}
	serveLogoFile(c, file)
}
