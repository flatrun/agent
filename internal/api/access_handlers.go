package api

import (
	_ "embed"
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
:root{--radius-sm:3px;--radius-md:4px;--accent:#3b82f6;--accent-hover:#2563eb;--accent-contrast:#fff;color-scheme:light;font-family:Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8fafc;color:#1e293b}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at top,#eff6ff 0,#f8fafc 42%,#f1f5f9 100%)}
.shell{width:min(420px,100%)}.brand{display:flex;justify-content:center;margin-bottom:24px}.brand a{display:block;border-radius:var(--radius-sm)}.brand a:focus-visible{outline:2px solid var(--accent);outline-offset:6px}.brand svg{display:block;width:auto;height:52px;max-width:100%}
.card{background:#fff;border:1px solid #e2e8f0;border-radius:var(--radius-md);padding:32px;box-shadow:0 18px 45px rgba(15,23,42,.09)}
h1{font-size:25px;line-height:1.25;letter-spacing:-.025em;margin:0 0 10px;color:#0f172a}p{color:#64748b;line-height:1.6;margin:0 0 26px}label{display:block;font-size:14px;font-weight:650;margin-bottom:8px;color:#334155}
input{width:100%;padding:12px 13px;border:1px solid #cbd5e1;border-radius:var(--radius-sm);background:#fff;color:#0f172a;font:inherit;outline:0;transition:border-color .15s,box-shadow .15s}input:focus{border-color:#3b82f6;box-shadow:0 0 0 3px rgba(59,130,246,.16)}
button{width:100%;margin-top:16px;padding:8px 16px;border:0;border-radius:var(--radius-sm);background:var(--accent);color:var(--accent-contrast);font:inherit;font-size:14px;font-weight:500;cursor:pointer;transition:background .15s,transform .15s}button:hover{background:var(--accent-hover)}button:active{transform:translateY(1px)}
.spinner{width:28px;height:28px;margin:0 0 20px;border:3px solid #dbeafe;border-top-color:var(--accent);border-radius:50%;animation:spin .9s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(prefers-reduced-motion:reduce){.spinner{animation-duration:3s}}
.action{display:block;margin-top:16px;padding:8px 16px;border-radius:var(--radius-sm);background:var(--accent);color:var(--accent-contrast);font-size:14px;font-weight:500;text-align:center;text-decoration:none}.action:hover{background:var(--accent-hover)}.hint a{color:var(--accent)}
.code{font-size:24px;letter-spacing:.4em;text-align:center;font-variant-numeric:tabular-nums}.error{color:#b91c1c}.hint{margin:16px 0 0;font-size:13px}
.note{margin:22px 0 0;text-align:center;font-size:12px;color:#94a3b8}@media(max-width:480px){body{padding:16px}.card{padding:25px 22px}}
</style>`

//go:embed assets/flatrun-logo.svg
var accessLogo string

func accessPage(title, content string) string {
	return brandPage(title, "", content, "Protected by FlatRun")
}

func brandPage(title, head, content, note string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` + head + `<title>` + title + ` | FlatRun</title>` + accessPageStyles + `</head><body><main class="shell"><div class="brand"><a href="https://flatrun.dev" aria-label="FlatRun website">` + accessLogo + `</a></div><section class="card">` + content + `</section><p class="note">` + note + `</p></main></body></html>`
}

func loginPath(returnPath string) string {
	return "/_flatrun/access/login?return=" + url.QueryEscape(access.SafeReturn(returnPath))
}

func (s *Server) accessError(c *gin.Context, status int, title, message, returnPath string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	content := fmt.Sprintf(`<h1>%s</h1><p>%s</p><a class="action" href="%s">Request a new code</a>`, title, message, html.EscapeString(loginPath(returnPath)))
	c.String(status, accessPage(title, content))
}

func accessCodePage(email, returnPath, message string, linkToo bool) string {
	hint := "No email?"
	if linkToo {
		hint = "You can also open the link in the email. No email?"
	}
	content := fmt.Sprintf(`<h1>Check your email</h1>%s<form method="post" action="/_flatrun/access/code"><input type="hidden" name="email" value="%s"><input type="hidden" name="return" value="%s"><label for="code">Sign-in code</label><input id="code" class="code" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9 ]{6,8}" maxlength="8" placeholder="000000" required autofocus><button type="submit">Continue</button></form><p class="hint">%s <a href="%s">Try again</a>.</p>`,
		message, html.EscapeString(email), html.EscapeString(returnPath), hint, html.EscapeString(loginPath(returnPath)))
	return accessPage("Check your email", content)
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
	policy, ok := s.applicationAccessPolicy(c.Request.Host, returnPath)
	sends, button := "a sign-in code and link", "Email me a code"
	if ok && !access.SendsCode(policy) {
		sends, button = "a sign-in link", "Email me a link"
	} else if ok && !access.SendsLink(policy) {
		sends = "a sign-in code"
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	content := fmt.Sprintf(`<h1>Verify your email</h1><p>Enter your email address and we will send you %s.</p><form method="post" action="/_flatrun/access/request"><input type="hidden" name="return" value="%s"><label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="email" placeholder="you@example.com" required autofocus><button type="submit">%s</button></form>`, sends, html.EscapeString(returnPath), button)
	c.String(http.StatusOK, accessPage("Sign in", content))
}

func (s *Server) requestApplicationAccess(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	returnPath := access.SafeReturn(c.PostForm("return"))
	policy, ok := s.applicationAccessPolicy(c.Request.Host, returnPath)
	if !ok || !access.Allows(policy, email) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		content := fmt.Sprintf(`<h1>Access denied</h1><p>This email address is not allowed to access this application.</p><a class="action" href="%s">Use a different email</a>`, html.EscapeString(loginPath(returnPath)))
		c.String(http.StatusForbidden, accessPage("Access denied", content))
		return
	}
	if s.access != nil && s.accessEmailSender != nil && s.access.AllowEmailRequest(c.Request.Host, email) {
		message, err := s.accessEmail(c, policy, email, returnPath)
		if err == nil {
			err = s.accessEmailSender.SendEmailTo(policy.EmailTargetID, email, message)
		}
		if err != nil {
			log.Printf("application access email failed for host %q: %v", c.Request.Host, err)
		}
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if !access.SendsCode(policy) {
		content := fmt.Sprintf(`<h1>Check your email</h1><p>If this address is allowed, a sign-in link is on its way. Open it on this device to continue.</p><p class="hint">No email? <a href="%s">Try again</a>.</p>`, html.EscapeString(loginPath(returnPath)))
		c.String(http.StatusAccepted, accessPage("Check your email", content))
		return
	}
	c.String(http.StatusAccepted, accessCodePage(email, returnPath, `<p>If this address is allowed, a 6-digit code is on its way. Enter it below.</p>`, access.SendsLink(policy)))
}

func (s *Server) accessEmail(c *gin.Context, policy *models.DomainAccessConfig, email, returnPath string) (notify.Notification, error) {
	var message notify.Notification
	if access.SendsLink(policy) {
		token, err := s.access.MagicLink(email, c.Request.Host, returnPath)
		if err != nil {
			return message, err
		}
		scheme := c.GetHeader("X-Forwarded-Proto")
		if scheme != "https" {
			scheme = "http"
		}
		link := fmt.Sprintf("%s://%s/_flatrun/access/verify?token=%s", scheme, c.Request.Host, url.QueryEscape(token))
		message.Title = "Your sign-in link"
		message.Message = "Open this link to continue: " + link
	}
	if access.SendsCode(policy) {
		code, err := s.access.IssueCode(email, c.Request.Host, returnPath)
		if err != nil {
			return message, err
		}
		message.Title = "Your sign-in code is " + code
		if message.Message == "" {
			message.Message = "Enter this code on the sign-in page."
		}
		message.Panels = []notify.Panel{{Title: "Sign-in code", Value: code, Detail: "Expires in 15 minutes"}}
	}
	return message, nil
}

func (s *Server) redeemApplicationAccessCode(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	formReturn := access.SafeReturn(c.PostForm("return"))
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/html; charset=utf-8")
	invalid := func() {
		c.String(http.StatusUnauthorized, accessCodePage(email, formReturn, `<p class="error">That code is wrong or has expired. Check the latest email, or request a new code.</p>`, false))
	}
	if s.access == nil {
		invalid()
		return
	}
	returnPath, err := s.access.RedeemCode(email, c.Request.Host, strings.ReplaceAll(c.PostForm("code"), " ", ""))
	if err != nil {
		invalid()
		return
	}
	policy, ok := s.applicationAccessPolicy(c.Request.Host, returnPath)
	if !ok || !access.SendsCode(policy) || !access.Allows(policy, email) {
		invalid()
		return
	}
	s.startApplicationAccessSession(c, email, c.Request.Host, returnPath, policy)
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
	s.handleApplicationAccessVerification(c, c.Query("token"), false)
}

type applicationAccessConfirmationRequest struct {
	Token string `json:"token" form:"token" binding:"required"`
}

func (s *Server) confirmApplicationAccess(c *gin.Context) {
	var request applicationAccessConfirmationRequest
	if err := c.ShouldBind(&request); err != nil {
		s.accessError(c, http.StatusBadRequest, "Link incomplete", "This sign-in link is missing part of its address. Open it again from the email, or request a new code.", "/")
		return
	}
	s.handleApplicationAccessVerification(c, request.Token, true)
}

func (s *Server) handleApplicationAccessVerification(c *gin.Context, token string, confirm bool) {
	if s.access == nil {
		s.accessError(c, http.StatusServiceUnavailable, "Sign-in unavailable", "Sign-in is not working on this server right now. Try again in a few minutes.", "/")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	email, host, returnPath, err := s.access.InspectMagicLink(token)
	if err != nil {
		s.accessError(c, http.StatusUnauthorized, "Link expired", "This sign-in link has expired or was already used. Each link works once, for 15 minutes.", returnPath)
		return
	}
	policy, ok := s.applicationAccessPolicy(host, returnPath)
	if !ok || !access.SendsLink(policy) || !access.Allows(policy, email) || access.Hostname(c.Request.Host) != access.Hostname(host) {
		s.accessError(c, http.StatusUnauthorized, "Link expired", "This sign-in link has expired or was already used. Each link works once, for 15 minutes.", returnPath)
		return
	}
	if !confirm {
		c.Header("Content-Type", "text/html; charset=utf-8")
		content := fmt.Sprintf(`<h1>Confirm sign-in</h1><p>Continue to the application with your verified email address.</p><form method="post" action="/_flatrun/access/verify"><input type="hidden" name="token" value="%s"><button type="submit">Continue to application</button></form>`, html.EscapeString(token))
		c.String(http.StatusOK, accessPage("Confirm sign-in", content))
		return
	}
	if _, _, _, err := s.access.VerifyMagicLink(token); err != nil {
		s.accessError(c, http.StatusUnauthorized, "Link expired", "This sign-in link has expired or was already used. Each link works once, for 15 minutes.", returnPath)
		return
	}
	s.startApplicationAccessSession(c, email, host, returnPath, policy)
}

func (s *Server) startApplicationAccessSession(c *gin.Context, email, host, returnPath string, policy *models.DomainAccessConfig) {
	session, err := s.access.Session(email, host, policy.SessionHours)
	if err != nil {
		s.accessError(c, http.StatusInternalServerError, "Sign-in failed", "We could not sign you in. Try again.", returnPath)
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
