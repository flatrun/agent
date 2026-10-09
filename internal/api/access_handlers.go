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

const accessPageStyles = `<style>
:root{color-scheme:light;font-family:Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8fafc;color:#1e293b}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at top,#eff6ff 0,#f8fafc 42%,#f1f5f9 100%)}
.shell{width:min(420px,100%)}.brand{display:flex;align-items:center;justify-content:center;gap:11px;margin-bottom:24px;color:#0f172a;font-size:22px;font-weight:750;letter-spacing:-.03em}
.brand-mark{width:38px;height:38px;display:grid;align-content:center;gap:4px;padding:8px;border-radius:10px;background:#2563eb;box-shadow:0 8px 20px rgba(37,99,235,.24);transform:rotate(-5deg)}
.brand-mark span{display:block;height:5px;border:2px solid #fff;border-radius:2px}.card{background:#fff;border:1px solid #e2e8f0;border-radius:18px;padding:32px;box-shadow:0 18px 45px rgba(15,23,42,.09)}
h1{font-size:25px;line-height:1.25;letter-spacing:-.025em;margin:0 0 10px;color:#0f172a}p{color:#64748b;line-height:1.6;margin:0 0 26px}label{display:block;font-size:14px;font-weight:650;margin-bottom:8px;color:#334155}
input{width:100%;padding:12px 13px;border:1px solid #cbd5e1;border-radius:9px;background:#fff;color:#0f172a;font:inherit;outline:0;transition:border-color .15s,box-shadow .15s}input:focus{border-color:#3b82f6;box-shadow:0 0 0 3px rgba(59,130,246,.16)}
button{width:100%;margin-top:16px;padding:12px 16px;border:0;border-radius:9px;background:#2563eb;color:#fff;font:inherit;font-weight:650;cursor:pointer;transition:background .15s,transform .15s}button:hover{background:#1d4ed8}button:active{transform:translateY(1px)}
.note{margin:22px 0 0;text-align:center;font-size:12px;color:#94a3b8}@media(max-width:480px){body{padding:16px}.card{padding:25px 22px}}
</style>`

const accessBrand = `<div class="brand" aria-label="FlatRun"><span class="brand-mark" aria-hidden="true"><span></span><span></span><span></span></span><span>FlatRun</span></div>`

func accessPage(title, content string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + title + ` | FlatRun</title>` + accessPageStyles + `</head><body><main class="shell">` + accessBrand + `<section class="card">` + content + `</section><p class="note">Protected by FlatRun</p></main></body></html>`
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
	returnPath := access.SafeReturn(c.Query("return"))
	c.Header("Content-Type", "text/html; charset=utf-8")
	content := fmt.Sprintf(`<h1>Verify your email</h1><p>Enter your email address to receive a secure sign-in link.</p><form method="post" action="/_flatrun/access/request"><input type="hidden" name="return" value="%s"><label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="email" placeholder="you@example.com" required autofocus><button type="submit">Email me a sign-in link</button></form>`, html.EscapeString(returnPath))
	c.String(http.StatusOK, accessPage("Sign in", content))
}

func (s *Server) requestApplicationAccess(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	returnPath := access.SafeReturn(c.PostForm("return"))
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
	c.String(http.StatusAccepted, accessPage("Check your email", `<h1>Check your email</h1><p>If this address is allowed, a secure sign-in link is on its way.</p>`))
}

func (s *Server) getAccessEmailTargets(c *gin.Context) {
	options := make([]gin.H, 0)
	if s.notify == nil {
		c.JSON(http.StatusOK, gin.H{"targets": options})
		return
	}
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
	if err != nil {
		c.String(http.StatusUnauthorized, "This sign-in link is invalid or expired")
		return
	}
	policy, ok := s.applicationAccessPolicy(host, returnPath)
	if !ok || !access.Allows(policy, email) || access.Hostname(c.Request.Host) != access.Hostname(host) {
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
	c.Redirect(http.StatusFound, access.SafeReturn(returnPath))
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
