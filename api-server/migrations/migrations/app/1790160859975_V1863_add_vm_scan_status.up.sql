-- vm_scan_status tracks the outcome of each VM package/vulnerability scan
-- (manual, cron sweep, and queue-consumed) so the UI can show whether a scan
-- is running, finished, or failed instead of silently ageing (see #38466).
-- One row per cloud_resource_id, upserted in place on every scan attempt.
CREATE TABLE IF NOT EXISTS "public"."vm_scan_status" (
    "cloud_resource_id" uuid NOT NULL,
    "tenant_id" uuid NOT NULL,
    "cloud_account_id" uuid NOT NULL,
    "status" text NOT NULL,
    -- Identifies one scan attempt. A terminal write (completed/failed) is
    -- only applied when its attempt_id still matches the row's current
    -- value, so an older attempt finishing after a newer overlapping one
    -- started (e.g. a manual scan racing the cron/queue sweep on the same
    -- VM) can't clobber the newer attempt's still-running status.
    "attempt_id" text NOT NULL DEFAULT '',
    "error" text NOT NULL DEFAULT '',
    "package_count" integer NOT NULL DEFAULT 0,
    "finding_count" integer NOT NULL DEFAULT 0,
    "scanned_at" timestamp NOT NULL DEFAULT now(),
    "updated_at" timestamp NOT NULL DEFAULT now(),
    PRIMARY KEY ("cloud_resource_id"),
    CONSTRAINT "vm_scan_status_cloud_resource_id_fkey" FOREIGN KEY ("cloud_resource_id") REFERENCES "public"."cloud_resourses"("id") ON UPDATE restrict ON DELETE cascade,
    CONSTRAINT "vm_scan_status_tenant_id_fkey" FOREIGN KEY ("tenant_id") REFERENCES "public"."tenant"("id") ON UPDATE restrict ON DELETE cascade,
    CONSTRAINT "vm_scan_status_status_check" CHECK ("status" IN ('started', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS "idx_vm_scan_status_tenant_account" ON "public"."vm_scan_status" ("tenant_id", "cloud_account_id");

-- Lets the stale-scan sweep find long-running "started" rows without a
-- sequential scan across every VM's status.
CREATE INDEX IF NOT EXISTS "idx_vm_scan_status_started_updated" ON "public"."vm_scan_status" ("updated_at") WHERE "status" = 'started';
