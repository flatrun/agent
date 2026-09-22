package api

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/flatrun/agent/internal/access"
	"github.com/flatrun/agent/internal/notify"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

type accessEmailSender interface {
	SendEmailTo(string, string, notify.Notification) error
}

func (s *Server) checkApplicationAccess(c *gin.Context) {
	policy, ok := s.applicationAccessPolicy(c.GetHeader("X-Original-Host"), c.GetHeader("X-Original-URI"))
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}
	cookie, err := c.Cookie(access.CookieName)
	if err != nil || s.access == nil || !s.access.ValidateSession(cookie, c.GetHeader("X-Original-Host"), policy) {
		c.Status(http.StatusUnauthorized)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) applicationAccessLogin(c *gin.Context) {
	returnPath := safeAccessReturn(c.Query("return"))
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in</title><style>body{font-family:system-ui,sans-serif;background:#f6f7f9;color:#17202a;margin:0;display:grid;place-items:center;min-height:100vh}.card{background:#fff;border:1px solid #dfe3e8;border-radius:16px;padding:32px;width:min(380px,calc(100%% - 48px));box-shadow:0 12px 32px #17202a14}h1{font-size:24px;margin:0 0 8px}p{color:#59636e;margin:0 0 24px}label{display:block;font-weight:600;margin-bottom:8px}input{box-sizing:border-box;width:100%%;padding:12px;border:1px solid #b8c0c8;border-radius:8px;font:inherit}button{width:100%%;margin-top:16px;padding:12px;border:0;border-radius:8px;background:#2563eb;color:white;font:inherit;font-weight:600}</style></head>
<body><main class="card"><h1>Verify your email</h1><p>We will email you a secure sign-in link.</p><form method="post" action="/_flatrun/access/request"><input type="hidden" name="return" value="%s"><label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="email" required><button type="submit">Email me a sign-in link</button></form></main></body></html>`, html.EscapeString(returnPath))
}

func (s *Server) requestApplicationAccess(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	returnPath := safeAccessReturn(c.PostForm("return"))
	policy, ok := s.applicationAccessPolicy(c.Request.Host, returnPath)
	if ok && s.access != nil && s.accessEmailSender != nil && access.Allows(policy, email) && s.access.AllowEmailRequest(c.Request.Host, email) {
		token, err := s.access.MagicLink(email, c.Request.Host, returnPath)
		if err == nil {
			scheme := c.GetHeader("X-Forwarded-Proto")
			if scheme != "https" {
				scheme = "http"
			}
			link := fmt.Sprintf("%s://%s/_flatrun/access/verify?token=%s", scheme, c.Request.Host, url.QueryEscape(token))
			err = s.accessEmailSender.SendEmailTo(policy.EmailTargetID, email, notify.Notification{
				Title: "Your FlatRun access link", Message: "Open this link to continue: " + link,
			})
		}
		if err != nil {
			log.Printf("application access email failed for host %q: %v", c.Request.Host, err)
		}
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusAccepted, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Check your email</title></head><body><main><h1>Check your email</h1><p>If this address is allowed, a sign-in link is on its way.</p></main></body></html>`)
}

func (s *Server) getAccessEmailTargets(c *gin.Context) {
	options := make([]gin.H, 0)
	for _, target := range s.notify.Load().Targets {
		if target.Enabled && strings.HasPrefix(target.URL, "smtp://") {
			options = append(options, gin.H{"id": target.ID, "name": target.Name})
		}
	}
	c.JSON(http.StatusOK, gin.H{"targets": options})
}

func (s *Server) verifyApplicationAccess(c *gin.Context) {
	if s.access == nil {
		c.String(http.StatusServiceUnavailable, "Access service is unavailable")
		return
	}
	email, host, returnPath, err := s.access.VerifyMagicLink(c.Query("token"))
	policy, ok := s.applicationAccessPolicy(host, returnPath)
	if err != nil || !ok || !access.Allows(policy, email) || !strings.EqualFold(hostnameOnly(c.Request.Host), hostnameOnly(host)) {
		c.String(http.StatusUnauthorized, "This sign-in link is invalid or expired")
		return
	}
	session, err := s.access.Session(email, host, policy.SessionHours)
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not create an access session")
		return
	}
	https := c.GetHeader("X-Forwarded-Proto") == "https"
	hours := policy.SessionHours
	if hours <= 0 {
		hours = 24
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(access.CookieName, session, hours*3600, "/", "", https, true)
	c.Redirect(http.StatusFound, safeAccessReturn(returnPath))
}

func (s *Server) applicationAccessPolicy(host, requestPath string) (*models.DomainAccessConfig, bool) {
	if s.manager == nil {
		return nil, false
	}
	deployments, err := s.manager.FindDeployments()
	if err != nil {
		return nil, false
	}
	return access.Resolve(deployments, host, requestPath)
}

func safeAccessReturn(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return "/"
	}
	return value
}

func hostnameOnly(value string) string {
	if index := strings.IndexByte(value, ':'); index >= 0 {
		return value[:index]
	}
	return value
}
