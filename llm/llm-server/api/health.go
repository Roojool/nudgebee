package api

import (
	"net/http"
	"nudgebee/llm/common"
	"time"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// HealthResponse is the body returned by the liveness endpoint.
type HealthResponse struct {
	Status string `json:"status" example:"ok"`
}

// handleHeathCheckApis registers GET /health.
//
// @Summary      Liveness probe
// @Description  Returns 200 OK when the llm-server process is up. Does not verify Postgres/RabbitMQ/LLM provider.
// @Tags         system
// @Produce      json
// @Success      200  {object}  HealthResponse
// @Router       /health [get]
func handleHeathCheckApis(r *gin.Engine, tracer trace.Tracer, meter metric.Meter) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, HealthResponse{Status: "ok"})
	})
	r.GET("/health/mq", handleMqHealth)
}

// Probe contract #39159: /health = liveness only, never dependency-aware (no restart loops on outages).
// /health/mq = messaging readiness signal, 503 = consumer detached / broker conn down > grace. Not a k8s probe:
// readiness would also take chat (no RabbitMQ need) offline; consumers self-heal -> use it for alerting.
var mqHealthGrace = 2 * time.Minute // absorbs normal broker restarts & pod startup

// MqHealthResponse is the body returned by /health/mq.
type MqHealthResponse struct {
	Status    string                    `json:"status" example:"ok"`
	Consumers []common.MqConsumerStatus `json:"consumers"`
}

// handleMqHealth reports whether every required RabbitMQ consumer is attached.
//
// @Summary      RabbitMQ consumer health
// @Description  503 when no consumer is registered, or one has been detached or its broker connection down, for over 2 minutes. Not a Kubernetes probe.
// @Tags         system
// @Produce      json
// @Success      200  {object}  MqHealthResponse
// @Failure      503  {object}  MqHealthResponse
// @Router       /health/mq [get]
func handleMqHealth(c *gin.Context) {
	healthy, consumers := common.MqConsumersHealthy(mqHealthGrace)
	if !healthy {
		c.JSON(http.StatusServiceUnavailable, MqHealthResponse{Status: "degraded", Consumers: consumers})
		return
	}
	c.JSON(http.StatusOK, MqHealthResponse{Status: "ok", Consumers: consumers})
}
