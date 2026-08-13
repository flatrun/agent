package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The description of this agent's own API, generated from its routes and types by
// tools/genspec and checked against them in CI. Serving it means a client can ask the instance
// it is talking to what that instance accepts, rather than assuming whatever was true when the
// client was built.
//
//go:embed openapi.json
var openAPISpec []byte

func (s *Server) getOpenAPISpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", openAPISpec)
}
