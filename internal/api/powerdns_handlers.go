package api

import (
	"net/http"

	"github.com/flatrun/agent/internal/dns"
	"github.com/gin-gonic/gin"
)

type PowerDNSHandlers struct {
	manager *dns.PowerDNSManager
}

func NewPowerDNSHandlers(manager *dns.PowerDNSManager) *PowerDNSHandlers {
	return &PowerDNSHandlers{manager: manager}
}

func (h *PowerDNSHandlers) RegisterRoutes(rg *gin.RouterGroup) {
	pdns := rg.Group("/powerdns")
	{
		pdns.GET("/status", h.GetStatus)
		pdns.POST("/enable", h.EnableService)
		pdns.POST("/disable", h.DisableService)
		pdns.POST("/restart", h.RestartService)

		pdns.GET("/zones", h.ListZones)
		pdns.POST("/zones", h.CreateZone)
		pdns.GET("/zones/:zoneId", h.GetZone)
		pdns.DELETE("/zones/:zoneId", h.DeleteZone)
		pdns.PATCH("/zones/:zoneId", h.UpdateRecords)
	}
}

func (h *PowerDNSHandlers) GetStatus(c *gin.Context) {
	status, err := h.manager.GetStatus()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *PowerDNSHandlers) EnableService(c *gin.Context) {
	if err := h.manager.EnableService(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "PowerDNS service enabled"})
}

func (h *PowerDNSHandlers) DisableService(c *gin.Context) {
	if err := h.manager.DisableService(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "PowerDNS service disabled"})
}

func (h *PowerDNSHandlers) RestartService(c *gin.Context) {
	if err := h.manager.RestartService(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "PowerDNS service restarted"})
}

func (h *PowerDNSHandlers) ListZones(c *gin.Context) {
	zones, err := h.manager.ListZones()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"zones": zones})
}

func (h *PowerDNSHandlers) GetZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	zone, err := h.manager.GetZone(zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, zone)
}

func (h *PowerDNSHandlers) CreateZone(c *gin.Context) {
	var req dns.ZoneCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	zone, err := h.manager.CreateZone(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, zone)
}

func (h *PowerDNSHandlers) DeleteZone(c *gin.Context) {
	zoneID := c.Param("zoneId")

	if err := h.manager.DeleteZone(zoneID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Zone deleted"})
}

func (h *PowerDNSHandlers) UpdateRecords(c *gin.Context) {
	zoneID := c.Param("zoneId")

	var req struct {
		RRSets []dns.RRSet `json:"rrsets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.manager.UpdateRecords(zoneID, req.RRSets); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	zone, err := h.manager.GetZone(zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, zone)
}
