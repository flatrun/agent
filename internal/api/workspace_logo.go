package api

import (
	"net/http"
	"path/filepath"

	"github.com/flatrun/agent/internal/workspace"
	"github.com/gin-gonic/gin"
)

func (s *Server) workspaceLogoDir(slug string) string {
	return filepath.Join(s.config.DeploymentsPath, ".flatrun", "workspaces", slug)
}

func (s *Server) withLogo(m workspace.Membership) workspace.Membership {
	if v := logoVersion(s.workspaceLogoDir(m.Slug)); v != "" {
		m.Logo = v
	}
	return m
}

func (s *Server) putWorkspaceLogo(c *gin.Context) {
	w, role, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	if _, ok := saveLogo(c, s.workspaceLogoDir(w.Slug)); !ok {
		return
	}
	c.JSON(http.StatusOK, s.withLogo(workspace.Membership{Workspace: *w, Role: role}))
}

func (s *Server) getWorkspaceLogo(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleMember)
	if !ok {
		return
	}
	serveLogo(c, s.workspaceLogoDir(w.Slug), "this workspace has no logo")
}

func (s *Server) deleteWorkspaceLogo(c *gin.Context) {
	w, _, ok := s.loadWorkspace(c, workspace.RoleAdmin)
	if !ok {
		return
	}
	removeLogo(c, s.workspaceLogoDir(w.Slug))
}
