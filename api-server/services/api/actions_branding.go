package api

import (
	"log/slog"

	"nudgebee/services/branding"
	"nudgebee/services/common"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// handleBrandingAction serves the admin CRUD surface for runtime branding
// overrides. The read path the frontend uses on every render is NOT here — it
// is GET /v1/branding/overrides, which needs no session.
func handleBrandingAction(actionPayload *ActionRequest, c *gin.Context, tracer *trace.Tracer, meter *metric.Meter, logger *slog.Logger) {
	ctx, err := buildContextFromPayload(c, actionPayload, tracer, meter, logger)
	if err != nil {
		c.JSON(400, common.ErrorActionBadRequest(err.Error()))
		return
	}
	input := actionPayload.Input

	switch actionPayload.Action.Name {
	case "branding_list_overrides":
		var req branding.ListRequest
		if err := common.UnmarshalMapToStruct(input, &req); err != nil {
			c.JSON(400, common.ErrorActionBadRequest(err.Error()))
			return
		}
		resp, err := branding.List(ctx, req)
		respondBranding(c, resp, err)

	case "branding_get_override":
		var req branding.HostRequest
		if err := common.UnmarshalMapToStruct(input, &req); err != nil {
			c.JSON(400, common.ErrorActionBadRequest(err.Error()))
			return
		}
		resp, err := branding.Get(ctx, req)
		respondBranding(c, resp, err)

	case "branding_upsert_override":
		var req branding.UpsertRequest
		if err := common.UnmarshalMapToStruct(input, &req); err != nil {
			c.JSON(400, common.ErrorActionBadRequest(err.Error()))
			return
		}
		resp, err := branding.Upsert(ctx, req)
		respondBranding(c, resp, err)

	case "branding_apply_previous_override":
		var req branding.HostRequest
		if err := common.UnmarshalMapToStruct(input, &req); err != nil {
			c.JSON(400, common.ErrorActionBadRequest(err.Error()))
			return
		}
		resp, err := branding.Restore(ctx, req)
		respondBranding(c, resp, err)

	case "branding_delete_override":
		var req branding.HostRequest
		if err := common.UnmarshalMapToStruct(input, &req); err != nil {
			c.JSON(400, common.ErrorActionBadRequest(err.Error()))
			return
		}
		resp, err := branding.Delete(ctx, req)
		respondBranding(c, resp, err)

	default:
		c.JSON(400, common.ErrorActionBadRequest("unknown action: "+actionPayload.Action.Name))
	}
}

func respondBranding(c *gin.Context, resp any, err error) {
	if err != nil {
		c.JSON(400, common.ErrorActionBadRequest(err.Error()))
		return
	}
	c.JSON(200, resp)
}
