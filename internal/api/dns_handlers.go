package api

import (
	"net/http"

	dnsPlugins "github.com/flatrun/agent/pkg/plugins/dns"
	"github.com/gin-gonic/gin"
)

func (s *Server) listDNSProviders(c *gin.Context) {
	providers := []gin.H{
		{
			"name":         "dns-cloudflare",
			"display_name": "Cloudflare DNS",
			"provider":     "cloudflare",
			"credentials":  dnsPlugins.NewCloudflarePlugin().RequiredCredentials(),
		},
		{
			"name":         "dns-route53",
			"display_name": "AWS Route 53",
			"provider":     "route53",
			"credentials":  dnsPlugins.NewRoute53Plugin().RequiredCredentials(),
		},
		{
			"name":         "dns-digitalocean",
			"display_name": "DigitalOcean DNS",
			"provider":     "digitalocean",
			"credentials":  dnsPlugins.NewDigitalOceanPlugin().RequiredCredentials(),
		},
		{
			"name":         "dns-hetzner",
			"display_name": "Hetzner DNS",
			"provider":     "hetzner",
			"credentials":  dnsPlugins.NewHetznerPlugin().RequiredCredentials(),
		},
	}

	c.JSON(http.StatusOK, gin.H{"providers": providers})
}
