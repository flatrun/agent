package api

import (
	"net/http"

	"github.com/flatrun/agent/pkg/updater"
	"github.com/gin-gonic/gin"
)

// parseChannel maps a request's channel value to an updater channel, defaulting
// to stable so a missing or unknown value never silently opts into prereleases.
func parseChannel(v string) updater.Channel {
	if v == string(updater.ChannelPrerelease) {
		return updater.ChannelPrerelease
	}
	return updater.ChannelStable
}

// getAgentUpdate reports the running version and the releases installable on
// the requested channel, newest-first with their changelogs, so the dashboard
// can show what an update would move to without shelling out to the CLI.
func (s *Server) getAgentUpdate(c *gin.Context) {
	channel := parseChannel(c.Query("channel"))

	availability, err := updater.ListAvailable(channel)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, availability)
}

type agentUpdateRequest struct {
	Channel string `json:"channel"`
	Force   bool   `json:"force"`
	Restart bool   `json:"restart"`
}

// triggerAgentUpdate installs the newest release on the requested channel. It
// runs synchronously: the download and install complete in seconds and the
// result reports what happened, after which the dashboard confirms the new
// version from the health endpoint. When restart is requested the agent
// restarts its own service, which the caller expects to drop the connection.
func (s *Server) triggerAgentUpdate(c *gin.Context) {
	var req agentUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	channel := parseChannel(req.Channel)

	result, err := updater.Update(req.Force, channel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if result.Installed && req.Restart {
		if err := updater.RestartService(); err != nil {
			c.JSON(http.StatusOK, gin.H{
				"result":         result,
				"restarted":      false,
				"restart_error":  err.Error(),
				"restart_manual": "sudo systemctl restart flatrun-agent",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"result": result, "restarted": true})
		return
	}

	c.JSON(http.StatusOK, gin.H{"result": result, "restarted": false})
}
