package dns

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/digitalocean/godo"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type DigitalOceanPlugin struct {
	BaseDNSPlugin
}

func NewDigitalOceanPlugin() *DigitalOceanPlugin {
	return &DigitalOceanPlugin{
		BaseDNSPlugin: BaseDNSPlugin{
			info: plugins.PluginInfo{
				Name:        "dns-digitalocean",
				DisplayName: "DigitalOcean DNS",
				Version:     "1.0.0",
				Description: "DigitalOcean DNS management",
				Author:      "FlatRun",
				Type:        plugins.TypeDNS,
				Category:    "dns",
				Enabled:     true,
				Capabilities: []string{
					string(plugins.CapDNSZoneManagement),
					string(plugins.CapDNSRecordManagement),
				},
			},
		},
	}
}

func (p *DigitalOceanPlugin) ProviderName() string {
	return "digitalocean"
}

func (p *DigitalOceanPlugin) RequiredCredentials() []plugins.CredentialField {
	return []plugins.CredentialField{
		{
			Name:     "api_token",
			Label:    "API Token",
			Type:     "password",
			Required: true,
			HelpText: "DigitalOcean API token with read/write access",
		},
	}
}

func (p *DigitalOceanPlugin) RegisterRoutes(router *gin.RouterGroup) error {
	provider := router.Group("/digitalocean")
	{
		provider.GET("/info", p.handleInfo)
		provider.POST("/validate", p.handleValidate)
		provider.POST("/zones", p.handleListZones)
		provider.POST("/zones/:zoneId", p.handleGetZone)
		provider.POST("/zones/:zoneId/records", p.handleListRecords)
		provider.POST("/zones/:zoneId/records/create", p.handleCreateRecord)
		provider.PUT("/zones/:zoneId/records/:recordId", p.handleUpdateRecord)
		provider.DELETE("/zones/:zoneId/records/:recordId", p.handleDeleteRecord)
	}
	return nil
}

type digitaloceanRequest struct {
	Credentials map[string]string `json:"credentials" binding:"required"`
}

type digitaloceanRecordRequest struct {
	Credentials map[string]string       `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordCreate `json:"record"`
}

type digitaloceanUpdateRequest struct {
	Credentials map[string]string        `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordUpdate `json:"record"`
}

func (p *DigitalOceanPlugin) getClient(creds map[string]string) (*godo.Client, error) {
	token, ok := creds["api_token"]
	if !ok || token == "" {
		return nil, fmt.Errorf("api_token is required")
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)
	return godo.NewClient(oauthClient), nil
}

func (p *DigitalOceanPlugin) handleInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":         p.info.Name,
		"display_name": p.info.DisplayName,
		"provider":     p.ProviderName(),
		"credentials":  p.RequiredCredentials(),
	})
}

func (p *DigitalOceanPlugin) handleValidate(c *gin.Context) {
	var req digitaloceanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	_, _, err = client.Domains.List(c.Request.Context(), &godo.ListOptions{PerPage: 1})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (p *DigitalOceanPlugin) handleListZones(c *gin.Context) {
	var req digitaloceanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSZone
	opt := &godo.ListOptions{PerPage: 100}

	for {
		domains, resp, err := client.Domains.List(c.Request.Context(), opt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		for _, d := range domains {
			result = append(result, plugins.DNSZone{
				ID:     d.Name,
				Name:   d.Name,
				Status: "active",
			})
		}

		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}

		page, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opt.Page = page + 1
	}

	c.JSON(http.StatusOK, gin.H{"zones": result})
}

func (p *DigitalOceanPlugin) handleGetZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req digitaloceanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	domain, _, err := client.Domains.Get(c.Request.Context(), zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, plugins.DNSZone{
		ID:     domain.Name,
		Name:   domain.Name,
		Status: "active",
	})
}

func (p *DigitalOceanPlugin) handleListRecords(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req digitaloceanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSRecord
	opt := &godo.ListOptions{PerPage: 100}

	for {
		records, resp, err := client.Domains.Records(c.Request.Context(), zoneID, opt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		for _, r := range records {
			record := plugins.DNSRecord{
				ID:      strconv.Itoa(r.ID),
				ZoneID:  zoneID,
				Type:    r.Type,
				Name:    r.Name,
				Content: r.Data,
				TTL:     r.TTL,
			}
			if r.Priority > 0 {
				priority := r.Priority
				record.Priority = &priority
			}
			result = append(result, record)
		}

		if resp.Links == nil || resp.Links.IsLastPage() {
			break
		}

		page, err := resp.Links.CurrentPage()
		if err != nil {
			break
		}
		opt.Page = page + 1
	}

	c.JSON(http.StatusOK, gin.H{"records": result})
}

func (p *DigitalOceanPlugin) handleCreateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req digitaloceanRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	createReq := &godo.DomainRecordEditRequest{
		Type: req.Record.Type,
		Name: req.Record.Name,
		Data: req.Record.Content,
		TTL:  req.Record.TTL,
	}

	if req.Record.Priority != nil {
		createReq.Priority = *req.Record.Priority
	}

	r, _, err := client.Domains.CreateRecord(c.Request.Context(), zoneID, createReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := plugins.DNSRecord{
		ID:      strconv.Itoa(r.ID),
		ZoneID:  zoneID,
		Type:    r.Type,
		Name:    r.Name,
		Content: r.Data,
		TTL:     r.TTL,
	}
	if r.Priority > 0 {
		result.Priority = &r.Priority
	}

	c.JSON(http.StatusCreated, result)
}

func (p *DigitalOceanPlugin) handleUpdateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req digitaloceanUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id, err := strconv.Atoi(recordID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record ID"})
		return
	}

	updateReq := &godo.DomainRecordEditRequest{}

	if req.Record.Content != nil {
		updateReq.Data = *req.Record.Content
	}
	if req.Record.TTL != nil {
		updateReq.TTL = *req.Record.TTL
	}
	if req.Record.Priority != nil {
		updateReq.Priority = *req.Record.Priority
	}

	r, _, err := client.Domains.EditRecord(c.Request.Context(), zoneID, id, updateReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := plugins.DNSRecord{
		ID:      strconv.Itoa(r.ID),
		ZoneID:  zoneID,
		Type:    r.Type,
		Name:    r.Name,
		Content: r.Data,
		TTL:     r.TTL,
	}
	if r.Priority > 0 {
		result.Priority = &r.Priority
	}

	c.JSON(http.StatusOK, result)
}

func (p *DigitalOceanPlugin) handleDeleteRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req digitaloceanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id, err := strconv.Atoi(recordID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record ID"})
		return
	}

	_, err = client.Domains.DeleteRecord(c.Request.Context(), zoneID, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Record deleted"})
}

func (p *DigitalOceanPlugin) SetCredentials(credentials map[string]string) error {
	return nil
}

func (p *DigitalOceanPlugin) ValidateCredentials() error {
	return nil
}

func (p *DigitalOceanPlugin) ListZones(ctx context.Context) ([]plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *DigitalOceanPlugin) GetZone(ctx context.Context, zoneID string) (*plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *DigitalOceanPlugin) ListRecords(ctx context.Context, zoneID string) ([]plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *DigitalOceanPlugin) CreateRecord(ctx context.Context, zoneID string, record plugins.DNSRecordCreate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *DigitalOceanPlugin) UpdateRecord(ctx context.Context, zoneID, recordID string, record plugins.DNSRecordUpdate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *DigitalOceanPlugin) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return fmt.Errorf("use RegisterRoutes API")
}
