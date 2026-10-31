# Patched mirror of aquasec/trivy, pulled by the on-demand scanner jobs
# (trivy_cis_scan runs it directly; image_scanner copies /usr/local/bin/trivy
# out of it into the scanned workload). Pinned in
# api-server/services/scan_orchestrator/images.go (TRIVY_IMAGE).
#
# Re-mirror + an apk upgrade layer: the upstream tag snapshot lags Alpine
# security releases (0.76.1 ships brotli-libs 1.2.0-r1, CVE-2026-111497).
# Same trivy binary, no behaviour change. Bump OS_PKG_EPOCH (ISO week) to force
# a re-pull; the weekly workflow run passes the current week.
ARG OS_PKG_EPOCH=2026-W44
FROM docker.io/aquasec/trivy:0.76.1

ARG OS_PKG_EPOCH

RUN echo "os-pkg-epoch: ${OS_PKG_EPOCH}" \
    && apk upgrade --no-cache
