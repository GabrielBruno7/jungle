package observability

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"jungle/internal/router"
)

func NewRouteSet() router.Set {
	handler := promhttp.Handler()
	return router.Set{
		Routes: []router.Route{
			router.Simple(http.MethodGet, "/metrics", func(c *gin.Context) {
				handler.ServeHTTP(c.Writer, c.Request)
			}),
		},
	}
}

var Module = fx.Module("observability",
	fx.Provide(router.AsRouteSet(NewRouteSet)),
)
