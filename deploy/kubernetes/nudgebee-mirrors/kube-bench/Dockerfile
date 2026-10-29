# Patched mirror of aquasec/kube-bench, pulled by the on-demand kube_bench_scan
# job. Pinned in api-server/services/scan_orchestrator/images.go
# (KUBE_BENCH_IMAGE).
#
# Re-mirror + an apk upgrade layer so OS packages track Alpine security
# releases between upstream tags. Same kube-bench binary and config, no
# behaviour change. Bump OS_PKG_EPOCH (ISO week) to force a re-pull; the weekly
# workflow run passes the current week.
ARG OS_PKG_EPOCH=2026-W44
FROM docker.io/aquasec/kube-bench:v0.16.0

ARG OS_PKG_EPOCH

RUN echo "os-pkg-epoch: ${OS_PKG_EPOCH}" \
    && apk upgrade --no-cache
