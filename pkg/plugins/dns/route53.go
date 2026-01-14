package dns

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/flatrun/agent/pkg/plugins"
	"github.com/gin-gonic/gin"
)

type Route53Plugin struct {
	BaseDNSPlugin
}

func NewRoute53Plugin() *Route53Plugin {
	return &Route53Plugin{
		BaseDNSPlugin: BaseDNSPlugin{
			info: plugins.PluginInfo{
				Name:        "dns-route53",
				DisplayName: "AWS Route 53",
				Version:     "1.0.0",
				Description: "AWS Route 53 DNS management",
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

func (p *Route53Plugin) ProviderName() string {
	return "route53"
}

func (p *Route53Plugin) RequiredCredentials() []plugins.CredentialField {
	return []plugins.CredentialField{
		{
			Name:     "access_key_id",
			Label:    "Access Key ID",
			Type:     "text",
			Required: true,
			HelpText: "AWS Access Key ID",
		},
		{
			Name:     "secret_access_key",
			Label:    "Secret Access Key",
			Type:     "password",
			Required: true,
			HelpText: "AWS Secret Access Key",
		},
		{
			Name:     "region",
			Label:    "Region",
			Type:     "text",
			Required: false,
			HelpText: "AWS Region (default: us-east-1)",
		},
	}
}

func (p *Route53Plugin) RegisterRoutes(router *gin.RouterGroup) error {
	provider := router.Group("/route53")
	{
		provider.GET("/info", p.handleInfo)
		provider.POST("/validate", p.handleValidate)
		provider.POST("/zones", p.handleListZones)
		provider.POST("/zones/:zoneId", p.handleGetZone)
		provider.POST("/zones/:zoneId/records", p.handleListRecords)
		provider.POST("/zones/:zoneId/records/create", p.handleCreateRecord)
		provider.DELETE("/zones/:zoneId/records/:recordId", p.handleDeleteRecord)
	}
	return nil
}

type route53Request struct {
	Credentials map[string]string `json:"credentials" binding:"required"`
}

type route53RecordRequest struct {
	Credentials map[string]string       `json:"credentials" binding:"required"`
	Record      plugins.DNSRecordCreate `json:"record"`
}

func (p *Route53Plugin) getClient(creds map[string]string) (*route53.Client, error) {
	accessKey, ok := creds["access_key_id"]
	if !ok || accessKey == "" {
		return nil, fmt.Errorf("access_key_id is required")
	}

	secretKey, ok := creds["secret_access_key"]
	if !ok || secretKey == "" {
		return nil, fmt.Errorf("secret_access_key is required")
	}

	region := creds["region"]
	if region == "" {
		region = "us-east-1"
	}

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, err
	}

	return route53.NewFromConfig(cfg), nil
}

func (p *Route53Plugin) handleInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":         p.info.Name,
		"display_name": p.info.DisplayName,
		"provider":     p.ProviderName(),
		"credentials":  p.RequiredCredentials(),
	})
}

func (p *Route53Plugin) handleValidate(c *gin.Context) {
	var req route53Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	_, err = client.ListHostedZones(c.Request.Context(), &route53.ListHostedZonesInput{
		MaxItems: intPtr(1),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"valid": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"valid": true})
}

func (p *Route53Plugin) handleListZones(c *gin.Context) {
	var req route53Request
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
	var marker *string

	for {
		output, err := client.ListHostedZones(c.Request.Context(), &route53.ListHostedZonesInput{
			Marker: marker,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		for _, z := range output.HostedZones {
			zoneID := strings.TrimPrefix(*z.Id, "/hostedzone/")
			result = append(result, plugins.DNSZone{
				ID:          zoneID,
				Name:        strings.TrimSuffix(*z.Name, "."),
				Status:      "active",
				RecordCount: int(*z.ResourceRecordSetCount),
			})
		}

		if !output.IsTruncated {
			break
		}
		marker = output.NextMarker
	}

	c.JSON(http.StatusOK, gin.H{"zones": result})
}

func (p *Route53Plugin) handleGetZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req route53Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	output, err := client.GetHostedZone(c.Request.Context(), &route53.GetHostedZoneInput{
		Id: &zoneID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	z := output.HostedZone
	c.JSON(http.StatusOK, plugins.DNSZone{
		ID:          strings.TrimPrefix(*z.Id, "/hostedzone/"),
		Name:        strings.TrimSuffix(*z.Name, "."),
		Status:      "active",
		RecordCount: int(*z.ResourceRecordSetCount),
		NameServers: output.DelegationSet.NameServers,
	})
}

func (p *Route53Plugin) handleListRecords(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req route53Request
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

	paginator := route53.NewListResourceRecordSetsPaginator(client, &route53.ListResourceRecordSetsInput{
		HostedZoneId: &zoneID,
	})

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		for _, r := range output.ResourceRecordSets {
			for _, rr := range r.ResourceRecords {
				record := plugins.DNSRecord{
					ID:      fmt.Sprintf("%s_%s_%s", *r.Name, r.Type, *rr.Value),
					ZoneID:  zoneID,
					Type:    string(r.Type),
					Name:    strings.TrimSuffix(*r.Name, "."),
					Content: *rr.Value,
					TTL:     int(*r.TTL),
				}
				result = append(result, record)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"records": result})
}

func (p *Route53Plugin) handleCreateRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req route53RecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := req.Record.Name
	if !strings.HasSuffix(name, ".") {
		name = name + "."
	}

	ttl := int64(req.Record.TTL)
	if ttl == 0 {
		ttl = 300
	}

	_, err = client.ChangeResourceRecordSets(c.Request.Context(), &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: &zoneID,
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: types.ChangeActionCreate,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: &name,
						Type: types.RRType(req.Record.Type),
						TTL:  &ttl,
						ResourceRecords: []types.ResourceRecord{
							{Value: &req.Record.Content},
						},
					},
				},
			},
		},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	result := plugins.DNSRecord{
		ID:      fmt.Sprintf("%s_%s_%s", name, req.Record.Type, req.Record.Content),
		ZoneID:  zoneID,
		Type:    req.Record.Type,
		Name:    strings.TrimSuffix(name, "."),
		Content: req.Record.Content,
		TTL:     int(ttl),
	}

	c.JSON(http.StatusCreated, result)
}

func (p *Route53Plugin) handleDeleteRecord(c *gin.Context) {
	zoneID := c.Param("zoneId")
	recordID := c.Param("recordId")

	var req route53Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	client, err := p.getClient(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	parts := strings.SplitN(recordID, "_", 3)
	if len(parts) != 3 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid record ID format"})
		return
	}

	name, recordType, content := parts[0], parts[1], parts[2]
	ttl := int64(300)

	_, err = client.ChangeResourceRecordSets(c.Request.Context(), &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: &zoneID,
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: types.ChangeActionDelete,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: &name,
						Type: types.RRType(recordType),
						TTL:  &ttl,
						ResourceRecords: []types.ResourceRecord{
							{Value: &content},
						},
					},
				},
			},
		},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Record deleted"})
}

func (p *Route53Plugin) SetCredentials(credentials map[string]string) error {
	return nil
}

func (p *Route53Plugin) ValidateCredentials() error {
	return nil
}

func (p *Route53Plugin) ListZones(ctx context.Context) ([]plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *Route53Plugin) GetZone(ctx context.Context, zoneID string) (*plugins.DNSZone, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *Route53Plugin) ListRecords(ctx context.Context, zoneID string) ([]plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *Route53Plugin) CreateRecord(ctx context.Context, zoneID string, record plugins.DNSRecordCreate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("use RegisterRoutes API")
}

func (p *Route53Plugin) UpdateRecord(ctx context.Context, zoneID, recordID string, record plugins.DNSRecordUpdate) (*plugins.DNSRecord, error) {
	return nil, fmt.Errorf("Route53 requires delete+create for updates")
}

func (p *Route53Plugin) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return fmt.Errorf("use RegisterRoutes API")
}

func intPtr(i int32) *int32 {
	return &i
}
