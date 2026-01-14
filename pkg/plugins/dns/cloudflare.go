package dns

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudflare/cloudflare-go"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

type CloudflarePlugin struct {
	BaseDNSPlugin
}

func NewCloudflarePlugin() *CloudflarePlugin {
	return &CloudflarePlugin{
		BaseDNSPlugin: BaseDNSPlugin{
			info: plugins.PluginInfo{
				Name:        "dns-cloudflare",
				DisplayName: "Cloudflare DNS",
				Version:     "1.0.0",
				Description: "Cloudflare DNS management",
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

func (p *CloudflarePlugin) ProviderName() string {
	return "cloudflare"
}

func (p *CloudflarePlugin) RequiredCredentials() []plugins.CredentialField {
	return []plugins.CredentialField{
		{
			Name:     "api_token",
			Label:    "API Token",
			Type:     "password",
			Required: true,
			HelpText: "Cloudflare API token with Zone:Read and DNS:Edit permissions",
		},
	}
}

func (p *CloudflarePlugin) RegisterRoutes(router *gin.RouterGroup) error {
	provider := router.Group("/cloudflare")
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

type cloudflareRequest struct {
	Credentials map[string]string `json:"credentials" binding:"required"`
}

type cloudflareRecordRequest struct {
	Credentials map[string]string        `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordCreate  `json:"record"`
}

type cloudflareUpdateRequest struct {
	Credentials map[string]string        `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordUpdate  `json:"record"`
}

func (p *CloudflarePlugin) getAPI(creds map[string]string) (*cloudflare.API, error) {
	token, ok := creds["api_token"]
	if !ok || token == "" {
		return nil, fmt.Errorf("api_token is required")
	}
	return cloudflare.NewWithAPIToken(token)
}

func (p *CloudflarePlugin) handleInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":         p.info.Name,
		"display_name": p.info.DisplayName,
		"provider":     p.ProviderName(),
		"credentials":  p.RequiredCredentials(),
	})
}

func (p *CloudflarePlugin) handleValidate(c *gin.Context) {
	var req cloudflareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	_, err = api.ListZones(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (p *CloudflarePlugin) handleListZones(c *gin.Context) {
	var req cloudflareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	zones, err := api.ListZones(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSZone
	for _, z := range zones {
		result = append(result, plugins.DNSZone{
			ID:          z.ID,
			Name:        z.Name,
			Status:      z.Status,
			NameServers: z.NameServers,
		})
	}

	c.JSON(http.StatusOK, gin.H{"zones": result})
}

func (p *CloudflarePlugin) handleGetZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req cloudflareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	z, err := api.ZoneDetails(c.Request.Context(), zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, plugins.DNSZone{
		ID:          z.ID,
		Name:        z.Name,
		Status:      z.Status,
		NameServers: z.NameServers,
	})
}

func (p *CloudflarePlugin) handleListRecords(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req cloudflareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rc := cloudflare.ZoneIdentifier(zoneID)
	records, _, err := api.ListDNSRecords(c.Request.Context(), rc, cloudflare.ListDNSRecordsParams{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSRecord
	for _, r := range records {
		record := plugins.DNSRecord{
			ID:      r.ID,
			ZoneID:  zoneID,
			Type:    r.Type,
			Name:    r.Name,
			Content: r.Content,
			TTL:     r.TTL,
		}
		if r.Priority != nil {
			priority := int(*r.Priority)
			record.Priority = &priority
		}
		proxied := r.Proxied != nil && *r.Proxied
		record.Proxied = &proxied
		result = append(result, record)
	}

	c.JSON(http.StatusOK, gin.H{"records": result})
}

func (p *CloudflarePlugin) handleCreateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req cloudflareRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rc := cloudflare.ZoneIdentifier(zoneID)
	params := cloudflare.CreateDNSRecordParams{
		Type:    req.Record.Type,
		Name:    req.Record.Name,
		Content: req.Record.Content,
		TTL:     req.Record.TTL,
	}

	if req.Record.Priority != nil {
		priority := uint16(*req.Record.Priority)
		params.Priority = &priority
	}
	if req.Record.Proxied != nil {
		params.Proxied = req.Record.Proxied
	}

	r, err := api.CreateDNSRecord(c.Request.Context(), rc, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := plugins.DNSRecord{
		ID:      r.ID,
		ZoneID:  zoneID,
		Type:    r.Type,
		Name:    r.Name,
		Content: r.Content,
		TTL:     r.TTL,
	}
	if r.Priority != nil {
		priority := int(*r.Priority)
		result.Priority = &priority
	}
	if r.Proxied != nil {
		result.Proxied = r.Proxied
	}

	c.JSON(http.StatusCreated, result)
}

func (p *CloudflarePlugin) handleUpdateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req cloudflareUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rc := cloudflare.ZoneIdentifier(zoneID)
	params := cloudflare.UpdateDNSRecordParams{ID: recordID}

	if req.Record.Content != nil {
		params.Content = *req.Record.Content
	}
	if req.Record.TTL != nil {
		params.TTL = *req.Record.TTL
	}
	if req.Record.Priority != nil {
		priority := uint16(*req.Record.Priority)
		params.Priority = &priority
	}
	if req.Record.Proxied != nil {
		params.Proxied = req.Record.Proxied
	}

	r, err := api.UpdateDNSRecord(c.Request.Context(), rc, params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := plugins.DNSRecord{
		ID:      r.ID,
		ZoneID:  zoneID,
		Type:    r.Type,
		Name:    r.Name,
		Content: r.Content,
		TTL:     r.TTL,
	}
	if r.Priority != nil {
		priority := int(*r.Priority)
		result.Priority = &priority
	}
	if r.Proxied != nil {
		result.Proxied = r.Proxied
	}

	c.JSON(http.StatusOK, result)
}

func (p *CloudflarePlugin) handleDeleteRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req cloudflareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api, err := p.getAPI(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rc := cloudflare.ZoneIdentifier(zoneID)
	if err := api.DeleteDNSRecord(c.Request.Context(), rc, recordID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Record deleted"})
}

func (p *CloudflarePlugin) SetCredentials(credentials map[string]string) error {
	return nil
}

func (p *CloudflarePlugin) ValidateCredentials() error {
	return nil
}

func (p *CloudflarePlugin) ListZones(ctx context.Context) ([]plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *CloudflarePlugin) GetZone(ctx context.Context, zoneID string) (*plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *CloudflarePlugin) ListRecords(ctx context.Context, zoneID string) ([]plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *CloudflarePlugin) CreateRecord(ctx context.Context, zoneID string, record plugins.DNSRecordCreate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *CloudflarePlugin) UpdateRecord(ctx context.Context, zoneID, recordID string, record plugins.DNSRecordUpdate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *CloudflarePlugin) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return fmt.Errorf("use RegisterRoutes API")
}
