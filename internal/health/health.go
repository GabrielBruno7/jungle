package health

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"jungle/internal/router"
)

// Handler answers liveness checks with a plain 200 OK.
type Handler struct{}

func New() *Handler {
	return &Handler{}
}

func (*Handler) Method() string { return http.MethodGet }

func (*Handler) Pattern() string { return "/" }

func (*Handler) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Status(http.StatusOK)
	}
}

// Module registers the health route into the shared "routes" group.
var Module = fx.Module("health",
	fx.Provide(router.AsRoute(New)),
)
