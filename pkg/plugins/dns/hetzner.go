package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

const hetznerAPIBase = "https://dns.hetzner.com/api/v1"

type HetznerPlugin struct {
	BaseDNSPlugin
	httpClient *http.Client
}

func NewHetznerPlugin() *HetznerPlugin {
	return &HetznerPlugin{
		BaseDNSPlugin: BaseDNSPlugin{
			info: plugins.PluginInfo{
				Name:        "dns-hetzner",
				DisplayName: "Hetzner DNS",
				Version:     "1.0.0",
				Description: "Hetzner DNS management",
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
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *HetznerPlugin) ProviderName() string {
	return "hetzner"
}

func (p *HetznerPlugin) RequiredCredentials() []plugins.CredentialField {
	return []plugins.CredentialField{
		{
			Name:     "api_token",
			Label:    "API Token",
			Type:     "password",
			Required: true,
			HelpText: "Hetzner DNS API token",
		},
	}
}

func (p *HetznerPlugin) RegisterRoutes(router *gin.RouterGroup) error {
	provider := router.Group("/hetzner")
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

type hetznerRequest struct {
	Credentials map[string]string `json:"credentials" binding:"required"`
}

type hetznerRecordRequest struct {
	Credentials map[string]string       `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordCreate `json:"record"`
}

type hetznerUpdateRequest struct {
	Credentials map[string]string       `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordUpdate `json:"record"`
}

func (p *HetznerPlugin) doRequest(ctx context.Context, apiToken, method, path string, body interface{}) ([]byte, error) {
	if apiToken == "" {
		return nil, fmt.Errorf("api_token is required")
	}

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, hetznerAPIBase+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Auth-API-Token", apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

type hetznerZonesResponse struct {
	Zones []struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Status          string `json:"status"`
		RecordsCount    int    `json:"records_count"`
		IsSecondaryDNS  bool   `json:"is_secondary_dns"`
		TxtVerification struct {
			Name  string `json:"name"`
			Token string `json:"token"`
		} `json:"txt_verification"`
	} `json:"zones"`
}

type hetznerZoneResponse struct {
	Zone struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Status       string   `json:"status"`
		RecordsCount int      `json:"records_count"`
		NS           []string `json:"ns"`
	} `json:"zone"`
}

type hetznerRecordsResponse struct {
	Records []struct {
		ID       string `json:"id"`
		ZoneID   string `json:"zone_id"`
		Type     string `json:"type"`
		Name     string `json:"name"`
		Value    string `json:"value"`
		TTL      int    `json:"ttl"`
		Priority *int   `json:"priority,omitempty"`
	} `json:"records"`
}

type hetznerRecordResponse struct {
	Record struct {
		ID       string `json:"id"`
		ZoneID   string `json:"zone_id"`
		Type     string `json:"type"`
		Name     string `json:"name"`
		Value    string `json:"value"`
		TTL      int    `json:"ttl"`
		Priority *int   `json:"priority,omitempty"`
	} `json:"record"`
}

func (p *HetznerPlugin) handleInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":         p.info.Name,
		"display_name": p.info.DisplayName,
		"provider":     p.ProviderName(),
		"credentials":  p.RequiredCredentials(),
	})
}

func (p *HetznerPlugin) handleValidate(c *gin.Context) {
	var req hetznerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := req.Credentials["api_token"]
	_, err := p.doRequest(c.Request.Context(), token, "GET", "/zones", nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (p *HetznerPlugin) handleListZones(c *gin.Context) {
	var req hetznerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := req.Credentials["api_token"]
	data, err := p.doRequest(c.Request.Context(), token, "GET", "/zones", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var resp hetznerZonesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSZone
	for _, z := range resp.Zones {
		result = append(result, plugins.DNSZone{
			ID:          z.ID,
			Name:        z.Name,
			Status:      z.Status,
			RecordCount: z.RecordsCount,
		})
	}

	c.JSON(http.StatusOK, gin.H{"zones": result})
}

func (p *HetznerPlugin) handleGetZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req hetznerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := req.Credentials["api_token"]
	data, err := p.doRequest(c.Request.Context(), token, "GET", "/zones/"+zoneID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var resp hetznerZoneResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, plugins.DNSZone{
		ID:          resp.Zone.ID,
		Name:        resp.Zone.Name,
		Status:      resp.Zone.Status,
		RecordCount: resp.Zone.RecordsCount,
		NameServers: resp.Zone.NS,
	})
}

func (p *HetznerPlugin) handleListRecords(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req hetznerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := req.Credentials["api_token"]
	data, err := p.doRequest(c.Request.Context(), token, "GET", "/records?zone_id="+zoneID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var resp hetznerRecordsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var result []plugins.DNSRecord
	for _, r := range resp.Records {
		result = append(result, plugins.DNSRecord{
			ID:       r.ID,
			ZoneID:   r.ZoneID,
			Type:     r.Type,
			Name:     r.Name,
			Content:  r.Value,
			TTL:      r.TTL,
			Priority: r.Priority,
		})
	}

	c.JSON(http.StatusOK, gin.H{"records": result})
}

func (p *HetznerPlugin) handleCreateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req hetznerRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	body := map[string]interface{}{
		"zone_id": zoneID,
		"type":    req.Record.Type,
		"name":    req.Record.Name,
		"value":   req.Record.Content,
		"ttl":     req.Record.TTL,
	}

	if req.Record.Priority != nil {
		body["priority"] = *req.Record.Priority
	}

	token := req.Credentials["api_token"]
	data, err := p.doRequest(c.Request.Context(), token, "POST", "/records", body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var resp hetznerRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, plugins.DNSRecord{
		ID:       resp.Record.ID,
		ZoneID:   resp.Record.ZoneID,
		Type:     resp.Record.Type,
		Name:     resp.Record.Name,
		Content:  resp.Record.Value,
		TTL:      resp.Record.TTL,
		Priority: resp.Record.Priority,
	})
}

func (p *HetznerPlugin) handleUpdateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req hetznerUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	body := map[string]interface{}{
		"zone_id": zoneID,
	}

	if req.Record.Content != nil {
		body["value"] = *req.Record.Content
	}
	if req.Record.TTL != nil {
		body["ttl"] = *req.Record.TTL
	}
	if req.Record.Priority != nil {
		body["priority"] = *req.Record.Priority
	}

	token := req.Credentials["api_token"]
	data, err := p.doRequest(c.Request.Context(), token, "PUT", "/records/"+recordID, body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var resp hetznerRecordResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, plugins.DNSRecord{
		ID:       resp.Record.ID,
		ZoneID:   resp.Record.ZoneID,
		Type:     resp.Record.Type,
		Name:     resp.Record.Name,
		Content:  resp.Record.Value,
		TTL:      resp.Record.TTL,
		Priority: resp.Record.Priority,
	})
}

func (p *HetznerPlugin) handleDeleteRecord(c *gin.Context) {
	recordID := c.Param("recordId")

	var req hetznerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token := req.Credentials["api_token"]
	_, err := p.doRequest(c.Request.Context(), token, "DELETE", "/records/"+recordID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Record deleted"})
}

func (p *HetznerPlugin) SetCredentials(credentials map[string]string) error {
	return nil
}

func (p *HetznerPlugin) ValidateCredentials() error {
	return nil
}

func (p *HetznerPlugin) ListZones(ctx context.Context) ([]plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *HetznerPlugin) GetZone(ctx context.Context, zoneID string) (*plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *HetznerPlugin) ListRecords(ctx context.Context, zoneID string) ([]plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *HetznerPlugin) CreateRecord(ctx context.Context, zoneID string, record plugins.DNSRecordCreate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *HetznerPlugin) UpdateRecord(ctx context.Context, zoneID, recordID string, record plugins.DNSRecordUpdate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *HetznerPlugin) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return fmt.Errorf("use RegisterRoutes API")
}
