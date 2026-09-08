package observability

import (
	"errors"
	"fmt"
	"nudgebee/services/eventrule/playbooks"
	"nudgebee/services/security"
	"time"
)

// impacted_services_enricher answers "what upstream services were hitting
// this pod when it crashed?" Fires on crash-loop. Reads coroot-node-agent /
// istio HTTP metrics for the pod.
//
// The action lives in observability rather than in playbooks because the
// metrics provider lookup is here, and playbooks cannot import this package
// (this package imports playbooks in order to register actions).
func init() {
	playbooks.RegisterAction("impacted_services_enricher", &impactedServicesAction{})
}

const impactedServicesLookbackMinutes = 30

type impactedServicesAction struct{}

func (a *impactedServicesAction) CanAutoExecute(ctx playbooks.PlaybookActionContext) bool {
	if ctx.GetEvent().AggregationKey != "report_crash_loop" {
		return false
	}
	name, ns := playbooks.SubjectPodNamespace(ctx.GetEvent())
	return name != "" && ns != ""
}

func (a *impactedServicesAction) AutoExecute(ctx playbooks.PlaybookActionContext) (playbooks.PlaybookActionResponse, error) {
	podName, ns := playbooks.SubjectPodNamespace(ctx.GetEvent())
	return a.Execute(ctx, map[string]any{"pod_name": podName, "namespace": ns})
}

func (a *impactedServicesAction) Execute(ctx playbooks.PlaybookActionContext, rawParams map[string]any) (playbooks.PlaybookActionResponse, error) {
	podName, _ := rawParams["pod_name"].(string)
	namespace, _ := rawParams["namespace"].(string)
	if podName == "" || namespace == "" {
		return nil, errors.New("impacted_services_enricher: pod_name + namespace required")
	}

	tenantID, accountID := ctx.GetTenantId(), ctx.GetAccountId()
	if tenantID == "" || accountID == "" {
		return nil, errors.New("impacted_services_enricher: tenant ID and account ID required")
	}
	requestCtx := security.NewRequestContextForTenantAdmin(tenantID, ctx.GetLogger(), nil, nil)

	// coroot-node-agent emits container_http_requests_total with source/destination
	// workload labels. Aggregating by source_workload gives the upstream caller
	// fan-in across the last 30 minutes.
	query := fmt.Sprintf(
		`sum by (source_workload_name, source_workload_namespace) (`+
			`rate(container_http_requests_total{__CLUSTER__ destination_workload_name="%s", destination_workload_namespace="%s"}[30m])`+
			`)`,
		podName, namespace,
	)

	// Through the metrics layer rather than relay-server's /prometheus facade. That
	// facade reaches the agent's globalConfig.prometheus_url, which an account whose
	// Prometheus is connected directly does not have — so this card was permanently
	// empty there, with no error to say why.
	start, end := playbooks.RangeQueryWindow(ctx.GetEvent(), impactedServicesLookbackMinutes, time.Now().UTC())
	output, err := FetchMetricsQuery(requestCtx, FetchMetricsRequest{
		AccountId:      accountID,
		MetricProvider: integrationPrometheus,
		Queries:        map[string]string{"impacted": query},
		StartTime:      start.UnixMilli(),
		EndTime:        end.UnixMilli(),
		StepInterval:   30,
	})
	if err != nil {
		return nil, fmt.Errorf("impacted_services_enricher: prom: %w", err)
	}

	rows := [][]any{}
	headers := []string{"Service", "Namespace", "Requests/sec (avg 30m)"}
	for _, series := range latestValuesByKey(output)["impacted"] {
		rows = append(rows, []any{
			series.Metric["source_workload_name"],
			series.Metric["source_workload_namespace"],
			fmt.Sprintf("%.3f", series.Value),
		})
	}

	return playbooks.PlaybookActionResponseTable{
		Rows:    rows,
		Headers: headers,
		AdditionalInfo: map[string]any{
			"title":              "Upstream services hitting this pod (30m)",
			"action_name":        "impacted_services_enricher",
			"actual_action_name": "impacted_services_enricher",
			"pod_name":           podName,
			"namespace":          namespace,
		},
		Insight: []playbooks.PlaybookActionResponseInsight{},
	}, nil
}
