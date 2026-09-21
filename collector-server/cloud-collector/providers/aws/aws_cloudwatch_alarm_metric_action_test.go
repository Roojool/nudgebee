package aws

import (
	"context"
	"testing"
	"time"

	"nudgebee/collector/cloud/providers"
)

// realAlarmDetail is the verbatim EventBridge detail for the alarm
// nb-demo-alb-target-5xx, whose get_cloudwatch_metric_data_on_alarm evidence
// came back as {"items":[],"start_date":"0001-01-01T00:00:00Z",...,"step":0} —
// a zero-value QueryMetricsResponse, i.e. an early return before any query ran.
const realAlarmDetail = `{
  "alarmName": "nb-demo-alb-target-5xx",
  "state": {"value": "ALARM", "reason": "Threshold Crossed", "timestamp": "2026-08-06T04:37:01.389+0000"},
  "previousState": {"value": "OK"},
  "configuration": {
    "metrics": [
      {
        "id": "1acfeaa5-1a1a-a461-0822-4891755890fe",
        "metricStat": {
          "stat": "Sum",
          "period": 60,
          "metric": {
            "name": "HTTPCode_Target_5XX_Count",
            "namespace": "AWS/ApplicationELB",
            "dimensions": {"LoadBalancer": "app/nb-demo-alb/3c4737067cb64e8e"}
          }
        },
        "returnData": true
      }
    ]
  }
}`

func TestAlarmMetricDetailsExtraction(t *testing.T) {
	dims, stat := extractAlarmMetricDetails(EventBridgeEvent{Detail: []byte(realAlarmDetail)})
	if len(dims) != 1 || dims[0]["Name"] != "LoadBalancer" || dims[0]["Value"] != "app/nb-demo-alb/3c4737067cb64e8e" {
		t.Errorf("dimensions = %#v, want single LoadBalancer=app/nb-demo-alb/3c4737067cb64e8e", dims)
	}
	if stat != "Sum" {
		t.Errorf("statistic = %q, want %q", stat, "Sum")
	}
}

// TestAlarmActionNamespaceRendering renders the namespace template exactly as
// aws_runbook.yaml declares it for get_cloudwatch_metric_data_on_alarm.
func TestAlarmActionNamespaceRendering(t *testing.T) {
	processor := NewTemplatedEventBridgeProcessor(EventRuleSet{}, nil)
	pCtx := providers.NewCloudProviderContext(context.Background())

	templateData := processor.prepareTemplateData(pCtx, EventBridgeEvent{
		ID:         "repro",
		Source:     "aws.cloudwatch",
		Region:     "us-east-1",
		DetailType: "CloudWatch Alarm State Change",
		Time:       time.Date(2026, 8, 6, 4, 37, 1, 0, time.UTC),
		Detail:     []byte(realAlarmDetail),
	}, providers.Account{})

	nsTemplate := `
              {{ $namespace := "" }}
              {{ $metrics := (index .Detail "configuration" "metrics") }}
              {{ range $m := $metrics }}
                {{ $metricStat := (index $m "metricStat") }}
                {{ if $metricStat }}
                  {{ $metric := (index $metricStat "metric") }}
                  {{ if $metric }}
                    {{ if or (index $m "returnData") (eq $namespace "") }}
                      {{ $namespace = (index $metric "namespace") | str }}
                    {{ end }}
                  {{ end }}
                {{ end }}
              {{ end }}
              {{ $namespace }}`

	if got := processor.renderParamValue(pCtx, "namespace", nsTemplate, templateData); got != "AWS/ApplicationELB" {
		t.Errorf("namespace rendered as %q, want %q", got, "AWS/ApplicationELB")
	}
}

// mathExpressionAlarmDetail is a metric-math alarm (error rate = errors/requests*100)
// whose "metrics" array puts the expression FIRST, ahead of the two raw metrics it
// references. Only "e1" (the expression) is marked returnData: true — that's the
// series CloudWatch actually evaluated against the threshold, but it has no
// "metricStat"/"metric" of its own, so namespace/name must fall back to a raw
// sub-metric while still picking up returnData's precedence when it does apply.
const mathExpressionAlarmDetail = `{
  "alarmName": "nb-demo-error-rate",
  "state": {"value": "ALARM", "reason": "Threshold Crossed", "timestamp": "2026-08-06T04:37:01.389+0000"},
  "previousState": {"value": "OK"},
  "configuration": {
    "metrics": [
      {
        "id": "e1",
        "expression": "(m2/m1)*100",
        "returnData": true
      },
      {
        "id": "m1",
        "metricStat": {
          "stat": "Sum",
          "period": 60,
          "metric": {
            "name": "RequestCount",
            "namespace": "AWS/ApplicationELB",
            "dimensions": {"LoadBalancer": "app/nb-demo-alb/3c4737067cb64e8e"}
          }
        },
        "returnData": false
      },
      {
        "id": "m2",
        "metricStat": {
          "stat": "Sum",
          "period": 60,
          "metric": {
            "name": "HTTPCode_Target_5XX_Count",
            "namespace": "AWS/ApplicationELB",
            "dimensions": {"LoadBalancer": "app/nb-demo-alb/3c4737067cb64e8e"}
          }
        },
        "returnData": false
      }
    ]
  }
}`

// TestAlarmActionNamespaceRenderingMathExpressionFirst pins the fix for the bug
// where the namespace/metric_name templates in aws_runbook.yaml hardcoded
// "metrics[0]". When the returnData expression comes first in the array, index-0
// resolves to an entry with no "metricStat", so the old template rendered an
// empty namespace even though the array holds two perfectly good raw metrics.
// The range-based template must walk the whole array and fall back to the first
// entry that actually has a metric identity.
func TestAlarmActionNamespaceRenderingMathExpressionFirst(t *testing.T) {
	processor := NewTemplatedEventBridgeProcessor(EventRuleSet{}, nil)
	pCtx := providers.NewCloudProviderContext(context.Background())

	templateData := processor.prepareTemplateData(pCtx, EventBridgeEvent{
		ID:         "repro-math",
		Source:     "aws.cloudwatch",
		Region:     "us-east-1",
		DetailType: "CloudWatch Alarm State Change",
		Time:       time.Date(2026, 8, 6, 4, 37, 1, 0, time.UTC),
		Detail:     []byte(mathExpressionAlarmDetail),
	}, providers.Account{})

	nsTemplate := `
              {{ $namespace := "" }}
              {{ $metrics := (index .Detail "configuration" "metrics") }}
              {{ range $m := $metrics }}
                {{ $metricStat := (index $m "metricStat") }}
                {{ if $metricStat }}
                  {{ $metric := (index $metricStat "metric") }}
                  {{ if $metric }}
                    {{ if or (index $m "returnData") (eq $namespace "") }}
                      {{ $namespace = (index $metric "namespace") | str }}
                    {{ end }}
                  {{ end }}
                {{ end }}
              {{ end }}
              {{ $namespace }}`

	nameTemplate := `
              {{ $metricName := "" }}
              {{ $metrics := (index .Detail "configuration" "metrics") }}
              {{ range $m := $metrics }}
                {{ $metricStat := (index $m "metricStat") }}
                {{ if $metricStat }}
                  {{ $metric := (index $metricStat "metric") }}
                  {{ if $metric }}
                    {{ if or (index $m "returnData") (eq $metricName "") }}
                      {{ $metricName = (index $metric "name") | str }}
                    {{ end }}
                  {{ end }}
                {{ end }}
              {{ end }}
              {{ $metricName }}`

	if got := processor.renderParamValue(pCtx, "namespace", nsTemplate, templateData); got != "AWS/ApplicationELB" {
		t.Errorf("namespace rendered as %q, want %q (fell through to a raw sub-metric)", got, "AWS/ApplicationELB")
	}
	if got := processor.renderParamValue(pCtx, "metric_name", nameTemplate, templateData); got != "RequestCount" {
		t.Errorf("metric_name rendered as %q, want %q (first raw sub-metric, since the returnData entry is an expression with no metric identity)", got, "RequestCount")
	}
}

// TestGetAwsServiceRejectsCloudwatchNamespace pins the dispatch behaviour that
// made the alarm action return an empty card: the aws_get_metric action passes
// the CloudWatch namespace as ServiceName, and no AWS service is registered
// under a namespace. awsProvider.QueryMetrices must therefore fall through to a
// direct CloudWatch query rather than returning an empty response.
func TestGetAwsServiceRejectsCloudwatchNamespace(t *testing.T) {
	for _, namespace := range []string{"AWS/ApplicationELB", "AWS/EC2", "AWS/ECS"} {
		if _, ok := GetAwsService(namespace); ok {
			t.Errorf("GetAwsService(%q) resolved; the namespace fallback in QueryMetrices assumes it does not", namespace)
		}
	}
}

func TestRefineELBServiceConfig(t *testing.T) {
	classic := serviceCloudwatchNamespaceMap["awselb"]

	tests := []struct {
		name          string
		resourceIds   []string
		dimensions    []map[string]string
		wantNamespace string
		wantDimension string
	}{
		{
			name:          "ALB from resource id",
			resourceIds:   []string{"app/nb-demo-alb/3c4737067cb64e8e"},
			wantNamespace: "AWS/ApplicationELB",
			wantDimension: "LoadBalancer",
		},
		{
			name:          "ALB from alarm dimension",
			dimensions:    []map[string]string{{"Name": "LoadBalancer", "Value": "app/nb-demo-alb/3c4737067cb64e8e"}},
			wantNamespace: "AWS/ApplicationELB",
			wantDimension: "LoadBalancer",
		},
		{
			name:          "NLB from resource id",
			resourceIds:   []string{"net/nb-demo-nlb/abc123"},
			wantNamespace: "AWS/NetworkELB",
			wantDimension: "LoadBalancer",
		},
		{
			name:          "Classic ELB is left alone",
			resourceIds:   []string{"my-classic-lb"},
			wantNamespace: "AWS/ELB",
			wantDimension: "LoadBalancerName",
		},
		{
			name:          "no identifiers falls back to Classic",
			wantNamespace: "AWS/ELB",
			wantDimension: "LoadBalancerName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refineELBServiceConfig(classic, tt.resourceIds, tt.dimensions)
			if got.Name != tt.wantNamespace {
				t.Errorf("namespace = %q, want %q", got.Name, tt.wantNamespace)
			}
			if got.ResourceDimensionName != tt.wantDimension {
				t.Errorf("dimension name = %q, want %q", got.ResourceDimensionName, tt.wantDimension)
			}
		})
	}
}

// TestRefineELBServiceConfigIgnoresNonELB guards against the refinement leaking
// into other services that happen to carry an "app/"-prefixed resource id.
func TestRefineELBServiceConfigIgnoresNonELB(t *testing.T) {
	ec2 := serviceCloudwatchNamespaceMap["amazonec2"]
	got := refineELBServiceConfig(ec2, []string{"app/whatever/123"}, nil)
	if got.Name != ec2.Name {
		t.Errorf("EC2 config was rewritten to %q", got.Name)
	}
}

// TestALBMetricsAreCountedNotAveraged pins the statistic choice: averaging a
// per-minute error count reports ~1 for a minute that served 142 errors.
func TestALBMetricsAreCountedNotAveraged(t *testing.T) {
	for _, metric := range []string{"RequestCount", "HTTPCode_Target_5XX_Count", "HTTPCode_ELB_5XX_Count"} {
		stats := elbALBConfig.MetricsStats[metric]
		if len(stats) != 1 || stats[0] != "Sum" {
			t.Errorf("%s statistics = %v, want [Sum]", metric, stats)
		}
	}
	// Gauges must stay averaged.
	if _, ok := elbALBConfig.MetricsStats["HealthyHostCount"]; ok {
		t.Errorf("HealthyHostCount is a gauge and should keep the default Average")
	}
}
