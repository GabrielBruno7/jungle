package health

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"jungle/internal/config"
	"jungle/internal/router"
)

type Handler struct {
	pool *pgxpool.Pool
	sqs  *sqs.Client
	cfg  *config.Config
}

func NewHandler(pool *pgxpool.Pool, sqsClient *sqs.Client, cfg *config.Config) *Handler {
	return &Handler{pool: pool, sqs: sqsClient, cfg: cfg}
}

func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	checks := gin.H{}
	ready := true

	if err := h.pool.Ping(ctx); err != nil {
		checks["postgres"] = "unavailable"
		ready = false
	} else {
		checks["postgres"] = "ok"
	}

	if _, err := h.sqs.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{
		QueueName: aws.String(queueNameFromURL(h.cfg.SQS.RequestQueueURL)),
	}); err != nil {
		checks["sqs"] = "unavailable"
		ready = false
	} else {
		checks["sqs"] = "ok"
	}

	status := http.StatusOK
	label := "ok"
	if !ready {
		status = http.StatusServiceUnavailable
		label = "degraded"
	}
	c.JSON(status, gin.H{"status": label, "checks": checks})
}

func queueNameFromURL(url string) string {
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			return url[i+1:]
		}
	}
	return url
}

func NewRouteSet(h *Handler) router.Set {
	return router.Set{
		Routes: []router.Route{
			router.Simple(http.MethodGet, "/health/live", h.Live),
			router.Simple(http.MethodGet, "/health/ready", h.Ready),
		},
	}
}

var Module = fx.Module("health",
	fx.Provide(
		NewHandler,
		router.AsRouteSet(NewRouteSet),
	),
)
