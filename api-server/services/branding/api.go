package branding

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes wires GET /v1/branding/overrides — the bulk snapshot the
// frontend polls to fill its in-process cache.
//
// Bulk, not per-host, on purpose. The table holds one row per partner hostname
// (a handful), and branding is resolved synchronously during SSR and inside
// /api/public/app_config, which cannot await a query. One periodic fetch of the
// whole set lets the frontend answer every host from memory; a per-host lookup
// would put a database round trip on the render path of the signin page.
//
// Only enabled rows are returned: `enabled=false` is the operator kill-switch,
// and a disabled row must be indistinguishable from an absent one to every
// reader. Metadata (ids, timestamps, previous values) is deliberately omitted —
// the frontend needs the values, and the admin surface reads through the
// authenticated RPC actions instead.
//
// Route auth: the server-wide middleware in cmd/main.go requires
// X-ACTION-TOKEN on everything outside /health, /api/webhooks and /swagger, so
// this endpoint is already service-to-service only. It is NOT reachable from a
// browser, and must not become so — it exposes every partner's brand at once.
func RegisterRoutes(r *gin.Engine) {
	r.GET("/v1/branding/overrides", func(c *gin.Context) {
		d, err := db()
		if err != nil {
			slog.Error("branding: database unavailable", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
			return
		}
		rows, err := listRows(d, true)
		if err != nil {
			slog.Error("branding: failed to list overrides", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list overrides"})
			return
		}
		// Key by the NORMALIZED host, not the stored string. Upsert normalizes
		// on write, but nothing stops a row arriving by hand (psql, a restore, a
		// migration), and a stored key that normalizes differently from the
		// request host — `localhost:3000` vs `localhost` is the one that bites —
		// silently never matches. The row looks perfect in the table and the
		// override simply does nothing, with no error anywhere. Normalizing here
		// too makes the served map correct regardless of how the row got written.
		byHost := make(map[string]map[string]any, len(rows))
		for _, row := range rows {
			if len(row.Overrides) == 0 {
				continue
			}
			key := NormalizeHost(row.Host)
			if key == "" {
				slog.Warn("branding: skipping override whose host normalizes to empty", "stored_host", row.Host)
				continue
			}
			if key != row.Host {
				slog.Warn("branding: stored host is not normalized; serving it under the normalized key",
					"stored_host", row.Host, "served_as", key)
			}
			// listRows orders by host, so a collision resolves deterministically
			// to the first row rather than to whichever the map saw last.
			if _, taken := byHost[key]; taken {
				slog.Warn("branding: two rows normalize to the same host; ignoring the later one",
					"host", key, "ignored_stored_host", row.Host)
				continue
			}
			byHost[key] = row.Overrides
		}
		c.JSON(http.StatusOK, gin.H{
			"overrides":   byHost,
			"server_time": time.Now().Unix(),
		})
	})
}
