package observability

import (
	"strings"
	"sync"
)

// Probe traffic is not request traffic, and on most workloads it is ALL the traffic.
//
// Measured on the dev cluster: of 223 workloads with eBPF HTTP data, a large share
// see nothing but kubelet probes and /metrics scrapes — `traefik`, `kube-state-metrics`,
// `temporal-worker` and others report a steady 0.2-2.4 requests/second of which 100%
// is probes. A "request rate" that counts those is reporting the observability stack's
// own polling, and anything reading it — a forecast, a threshold, a chart — is reading
// the scrape interval rather than the workload's load.
//
// So the exclusion is part of what the key MEANS, not a filter a caller bolts on. It
// lives here for two reasons: every provider has to apply the same definition or the
// same key answers differently per account, and a caller that passed its own list
// would be putting consumer configuration into a shared contract — the thing the
// 2026-09-21 decision entry rejected.
//
// The list came from ml-k8s-server, which had accumulated it as the consumer of these
// metrics; it is now the layer's, and a provider added later inherits it rather than
// reinventing it.
var healthCheckPaths = []string{
	"/actuator/health",
	"/alive",
	"/api/health",
	"/check",
	"/diagnostics",
	"/health",
	"/health/live",
	"/health/ready",
	"/healthcheck",
	"/healthz",
	"/heartbeat",
	"/isalive",
	"/isready",
	"/livez",
	"/ping",
	"/probe",
	"/probe/alive",
	"/probe/check",
	"/probe/health",
	"/probe/heartbeat",
	"/probe/liveness",
	"/probe/readiness",
	"/probe/status",
	"/readiness",
	"/status",
	"/statuscheck",
}

// healthCheckPathsRegex renders the list as a PromQL alternation for `path!~`.
//
// Anchored implicitly by PromQL, which matches the whole label value, so `/health`
// does not exclude `/healthy-users`. That is deliberate: a prefix match would hide
// real endpoints whose names start with a probe's.
// Built once: the list is a package constant in all but name, and this is called on
// every query generation — twice for the mean-latency key, which names it on both
// halves of its division.
var healthCheckPathsAlternation = sync.OnceValue(func() string {
	escaped := make([]string, 0, len(healthCheckPaths))
	for _, p := range healthCheckPaths {
		escaped = append(escaped, escapePromQLRegex(p))
	}
	return strings.Join(escaped, "|")
})

func healthCheckPathsRegex() string { return healthCheckPathsAlternation() }

// escapePromQLRegex escapes the characters a path could carry that mean something in
// RE2. Only `.` and `+` are plausible in a URL path, but the whole set is cheap.
func escapePromQLRegex(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
