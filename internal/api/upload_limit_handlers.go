package api

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flatrun/agent/internal/access"
	"github.com/flatrun/agent/internal/events"
	"github.com/flatrun/agent/internal/nginx"
	"github.com/flatrun/agent/pkg/models"
	"github.com/gin-gonic/gin"
)

const (
	// uploadRejectionWindow is how long a rejected upload stays on the
	// deployment's banner after the last one.
	uploadRejectionWindow = 7 * 24 * time.Hour
	// uploadRejectionNotifyEvery keeps a burst of rejected uploads to one
	// notification event per domain.
	uploadRejectionNotifyEvery = 15 * time.Minute
)

// UploadRejection is a domain's recent requests that nginx refused for
// being over its size limit.
type UploadRejection struct {
	DomainID string    `json:"domain_id"`
	Domain   string    `json:"domain"`
	Limit    string    `json:"limit"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
	LastPath string    `json:"last_path"`
}

type uploadRejections struct {
	mu        sync.Mutex
	byKey     map[string]*UploadRejection
	published map[string]time.Time
}

// record counts one rejection and reports whether it should be published.
func (u *uploadRejections) record(deployment string, r UploadRejection, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.byKey == nil {
		u.byKey = map[string]*UploadRejection{}
		u.published = map[string]time.Time{}
	}
	key := deployment + "/" + r.DomainID
	existing := u.byKey[key]
	if existing == nil || now.Sub(existing.LastAt) > uploadRejectionWindow {
		existing = &UploadRejection{DomainID: r.DomainID}
		u.byKey[key] = existing
	}
	existing.Domain = r.Domain
	existing.Limit = r.Limit
	existing.Count++
	existing.LastAt = now
	existing.LastPath = r.LastPath

	if now.Sub(u.published[key]) < uploadRejectionNotifyEvery {
		return false
	}
	u.published[key] = now
	return true
}

func (u *uploadRejections) list(deployment string, now time.Time) []UploadRejection {
	u.mu.Lock()
	defer u.mu.Unlock()
	prefix := deployment + "/"
	out := []UploadRejection{}
	for key, r := range u.byKey {
		if strings.HasPrefix(key, prefix) && now.Sub(r.LastAt) <= uploadRejectionWindow {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

func (u *uploadRejections) clear(deployment, domainID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.byKey, deployment+"/"+domainID)
}

// nginxTooLargeBody is the page nginx itself sends with a 413, so a domain
// without the upload limit page answers exactly as it did before the hook.
const nginxTooLargeBody = "<html>\r\n<head><title>413 Request Entity Too Large</title></head>\r\n<body>\r\n<center><h1>413 Request Entity Too Large</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

func wantsJSON(c *gin.Context) bool {
	accept := strings.ToLower(c.GetHeader("Accept"))
	return strings.Contains(accept, "json") || strings.EqualFold(c.GetHeader("X-Requested-With"), "XMLHttpRequest")
}

func tooLargePage(host, limit string) string {
	site := "This site"
	if host != "" {
		site = "<strong>" + html.EscapeString(host) + "</strong>"
	}
	accepts := ` refused the upload because it is larger than the server accepts.`
	if limit != "" {
		accepts = ` accepts requests up to <strong>` + html.EscapeString(limit) + `</strong>.`
	}
	content := `<h1>Upload too large</h1><p>` + site + accepts +
		` Try a smaller file, or ask the site's administrator to raise the limit.</p>`
	return brandPage("Upload too large", "", content, "Served by FlatRun")
}

// requestTooLarge is reached through nginx when a request is over its
// domain's size limit. It records the rejection for the deployment's owners
// and answers the visitor with the limit page when the domain enables it.
func (s *Server) requestTooLarge(c *gin.Context) {
	name := c.Param("deployment")
	domainID := c.Param("domain")
	c.Header("Cache-Control", "no-store")

	if s.proxyOrchestrator == nil || !s.proxyOrchestrator.NginxManager().ValidHookToken(c.GetHeader(nginx.HookTokenHeader())) {
		c.Data(http.StatusRequestEntityTooLarge, "text/html", []byte(nginxTooLargeBody))
		return
	}

	var domain *models.DomainConfig
	if deployment, err := s.manager.GetDeployment(name); err == nil && deployment.Metadata != nil {
		for _, d := range deployment.Metadata.GetDomains() {
			if d.ID == domainID {
				d := d
				domain = &d
				break
			}
		}
	}
	if domain == nil {
		c.Data(http.StatusRequestEntityTooLarge, "text/html", []byte(nginxTooLargeBody))
		return
	}

	limit := models.ConfiguredBodySize(domain.ClientMaxBodySize, s.nginxBodySizeDefault())
	host := access.Hostname(c.GetHeader("X-Original-Host"))
	if host == "" {
		host = domain.Domain
	}
	path := c.GetHeader("X-Original-URI")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if len(path) > 512 {
		path = path[:512]
	}

	now := time.Now().UTC()
	if s.uploadRejections.record(name, UploadRejection{DomainID: domainID, Domain: domain.Domain, Limit: limit, LastPath: path}, now) {
		s.publishUploadRejection(name, domain, limit, path, now)
	}

	if !domain.UploadLimitPage {
		c.Data(http.StatusRequestEntityTooLarge, "text/html", []byte(nginxTooLargeBody))
		return
	}
	if wantsJSON(c) {
		problem := gin.H{
			"type":   "about:blank",
			"title":  "Request too large",
			"status": http.StatusRequestEntityTooLarge,
			"detail": "The request is larger than this site accepts.",
		}
		if limit != "" {
			problem["detail"] = "This site accepts requests up to " + limit + "."
			problem["limit"] = limit
		}
		body, _ := json.Marshal(problem)
		c.Data(http.StatusRequestEntityTooLarge, "application/problem+json", body)
		return
	}
	c.Data(http.StatusRequestEntityTooLarge, "text/html; charset=utf-8", []byte(tooLargePage(host, limit)))
}

func (s *Server) nginxBodySizeDefault() string {
	if s.config == nil {
		return ""
	}
	return s.config.Nginx.ClientMaxBodySize
}

func (s *Server) publishUploadRejection(deployment string, domain *models.DomainConfig, limit, path string, now time.Time) {
	if s.notify == nil {
		return
	}
	refusedBy := "the " + limit + " limit"
	if limit == "" {
		refusedBy = "the limit in nginx.conf, as neither the domain nor the agent sets one"
	}
	_, err := s.notify.Publish(events.Event{
		Source:         "proxy",
		Type:           "request_too_large",
		Severity:       events.SeverityWarning,
		Title:          "Uploads refused on " + domain.Domain,
		Message:        "A request to " + domain.Domain + path + " was larger than " + refusedBy + " and was refused with HTTP 413. Raise the maximum upload request size for this domain if uploads of this size are expected.",
		Scope:          events.Scope{Deployment: deployment},
		CorrelationKey: "proxy/request_too_large/" + deployment + "/" + domain.ID,
		OccurredAt:     now,
		Attributes: map[string]any{
			"domain_id": domain.ID,
			"domain":    domain.Domain,
			"limit":     limit,
			"path":      path,
		},
	})
	if err != nil {
		log.Printf("warning: failed to publish upload rejection for %s: %v", deployment, err)
	}
}

// listUploadRejections returns the deployment's domains that refused
// requests for being too large in the last week.
func (s *Server) listUploadRejections(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"rejections": s.uploadRejections.list(c.Param("name"), time.Now().UTC())})
}

// reapplyAllProxies re-renders every installed vhost so an agent-wide proxy
// default takes effect. A deployment nginx rejects keeps its previous vhost.
func (s *Server) reapplyAllProxies() error {
	if s.proxyOrchestrator == nil {
		return nil
	}
	deployments, err := s.manager.FindDeployments()
	if err != nil {
		return err
	}
	var failed []string
	for i := range deployments {
		deployment := &deployments[i]
		if !s.proxyOrchestrator.NginxManager().VirtualHostExists(deployment.Name) {
			continue
		}
		if err := s.proxyOrchestrator.ReapplyDeployment(deployment); err != nil {
			log.Printf("warning: failed to reapply proxy for %s: %v", deployment.Name, err)
			failed = append(failed, deployment.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("nginx kept the previous config for %s", strings.Join(failed, ", "))
	}
	return nil
}
