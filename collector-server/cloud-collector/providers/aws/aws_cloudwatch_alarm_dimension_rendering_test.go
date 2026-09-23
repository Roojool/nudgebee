package aws

import (
	"context"
	"testing"
	"time"

	"nudgebee/collector/cloud/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlarmDimensionLookupIsCaseInsensitive pins the same casing quirk
// extractAlarmMetricDetails already guards against (EventBridge sometimes
// sends "LoadBalancer" and "loadBalancer" for the same dimension): a rule
// asking alarmDimension for "LoadBalancer" must still find a dimension the
// payload sent as "loadBalancer".
func TestAlarmDimensionLookupIsCaseInsensitive(t *testing.T) {
	processor := NewTemplatedEventBridgeProcessor(EventRuleSet{}, nil)
	pCtx := providers.NewCloudProviderContext(context.Background())

	detail := []byte(`{
		"alarmName": "nb-demo-alb-target-5xx",
		"state": {"value": "ALARM"},
		"configuration": {
			"metrics": [{
				"id": "m1",
				"metricStat": {
					"stat": "Sum",
					"metric": {
						"name": "HTTPCode_Target_5XX_Count",
						"namespace": "AWS/ApplicationELB",
						"dimensions": {"loadBalancer": "app/nb-demo-alb/3c4737067cb64e8e"}
					}
				},
				"returnData": true
			}]
		}
	}`)

	templateData := processor.prepareTemplateData(pCtx, EventBridgeEvent{
		ID:         "repro",
		Source:     "aws.cloudwatch",
		Region:     "us-east-1",
		DetailType: "CloudWatch Alarm State Change",
		Time:       time.Date(2026, 8, 6, 4, 37, 1, 0, time.UTC),
		Detail:     detail,
	}, providers.Account{})

	got := processor.renderParamValue(pCtx, "dim", `{{ alarmDimension .Detail "LoadBalancer" }}`, templateData)
	assert.Equal(t, "app/nb-demo-alb/3c4737067cb64e8e", got,
		"alarmDimension must find \"loadBalancer\" in the payload when looked up as \"LoadBalancer\"")
}

// TestECSCpuAlarmRule_ArnAndActionParamsRenderFromRealPayloadShape drives
// ECS_Service_CPU_Or_Memory_Alarm through the real aws_runbook.yaml with a
// CloudWatch Alarm State Change payload shaped like AWS actually sends it
// (configuration.metrics[].metricStat.metric.{namespace,name,dimensions}, with
// dimensions as a map) — not the flat configuration.namespace/metricName/
// dimensions shape the rule used to (wrongly) read. Cluster/service names carry
// hyphens on purpose, since the alarmName-splitting fallback used by other
// rules would mis-split them; this rule must derive both purely from
// dimensions.
//
// Every CloudWatch alarm event also matches the catch-all
// CloudWatch_Alarm_State_Change_Generic rule, so actions that both rules
// happen to call (GetECSServiceDetails, QueryMetrices) fire more than once;
// assertions index into the call history rather than relying on "last call".
func TestECSCpuAlarmRule_ArnAndActionParamsRenderFromRealPayloadShape(t *testing.T) {
	seedAccountTenantForTest(t)

	const clusterName = "my-prod-cluster"
	const serviceName = "checkout-service"
	sqsMessageBody := `{
		"version": "0",
		"id": "evt-id-cpu-alarm",
		"detail-type": "CloudWatch Alarm State Change",
		"source": "aws.cloudwatch",
		"account": "123456789012",
		"time": "2026-09-23T10:00:00Z",
		"region": "us-east-1",
		"resources": ["arn:aws:cloudwatch:us-east-1:123456789012:alarm:my-prod-cluster-checkout-service-HighCPUUtilization-Auto"],
		"detail": {
			"alarmName": "my-prod-cluster-checkout-service-HighCPUUtilization-Auto",
			"state": {"value": "ALARM", "reason": "Threshold Crossed"},
			"previousState": {"value": "OK"},
			"configuration": {
				"metrics": [
					{
						"id": "m1",
						"metricStat": {
							"stat": "Average",
							"period": 300,
							"metric": {
								"name": "CPUUtilization",
								"namespace": "AWS/ECS",
								"dimensions": {"ClusterName": "my-prod-cluster", "ServiceName": "checkout-service"}
							}
						},
						"returnData": true
					}
				]
			}
		}
	}`

	rules, err := GetEventRules("")
	require.NoError(t, err, "Failed to load event rules")

	dummyAPI := &dummyAwsProvider{}
	processor := NewTemplatedEventBridgeProcessor(rules, dummyAPI)
	dummyHandler := &dummyProcessedEventHandler{}
	pCtx := providers.NewCloudProviderContext(context.Background())

	processedEvent, _, err := processSQSMessageBodyForEventBridgeEvent(pCtx, sqsMessageBody, processor, dummyHandler)
	require.NoError(t, err)

	// The rule matched at all (namespace filter passed on the real payload shape).
	require.NotEmpty(t, processedEvent.EventId, "rule did not match — alarmNamespace filter likely failed")

	// get_ecs_service_details_on_alarm (aws_get_resource, service_name: ecs,
	// resource_type: service) doesn't call ListResources — it parses the rendered
	// resource_identifier ARN (arn:aws:ecs:<region>:<acct>:service/<cluster>/<service>)
	// via parseARN + split on "/", then calls GetECSServiceDetails(region, cluster,
	// service) directly. This is the FIRST GetECSServiceDetails call (action order
	// in the rule), so it isolates exactly the ARN this rule rendered.
	require.GreaterOrEqual(t, len(dummyAPI.getECSServiceDetailsCalls), 1,
		"expected get_ecs_service_details_on_alarm to reach GetECSServiceDetails — ARN parsing failed")
	arnParseCall := dummyAPI.getECSServiceDetailsCalls[0]
	assert.Equal(t, "us-east-1", arnParseCall.region)
	assert.Equal(t, clusterName, arnParseCall.clusterIdentifier,
		"cluster segment of arn:aws:ecs:<region>:<acct>:service/<cluster>/<service> must not be empty")
	assert.Equal(t, serviceName, arnParseCall.serviceIdentifier,
		"service segment of arn:aws:ecs:<region>:<acct>:service/<cluster>/<service> must not be empty")

	// get_related_ecs_metrics_on_alarm (aws_get_metric): namespace, metric_name and
	// dimensions all sourced from configuration.metrics[].metricStat.metric. This is
	// the FIRST QueryMetrices call (before the generic rule's own metric action).
	require.GreaterOrEqual(t, len(dummyAPI.queryMetricesCalls), 1)
	metricArgs := dummyAPI.queryMetricesCalls[0]
	assert.Equal(t, "AWS/ECS", metricArgs.MetricNamespace)
	assert.Equal(t, []string{"CPUUtilization"}, metricArgs.MetricNames)
	require.Len(t, metricArgs.Dimensions, 2, "expected ClusterName and ServiceName dimensions, neither empty")
	dimByName := map[string]string{}
	for _, d := range metricArgs.Dimensions {
		dimByName[d["Name"]] = d["Value"]
	}
	assert.Equal(t, clusterName, dimByName["ClusterName"])
	assert.Equal(t, serviceName, dimByName["ServiceName"])

	// get_ecs_service_logs_on_alarm (aws_get_log, auto_detect_log_group): cluster_name
	// and service_name params reach GetECSServiceDetails non-empty. This is the
	// SECOND GetECSServiceDetails call. (The action itself then depends on the dummy
	// provider's unrelated ECS metadata shape — this test only asserts the
	// alarm-dimension-derived params it was called with.)
	require.Len(t, dummyAPI.getECSServiceDetailsCalls, 2, "expected exactly 2 GetECSServiceDetails calls: aws_get_resource + aws_get_log auto-detect")
	logAutoDetectCall := dummyAPI.getECSServiceDetailsCalls[1]
	assert.Equal(t, clusterName, logAutoDetectCall.clusterIdentifier, "cluster_name param must not render empty")
	assert.Equal(t, serviceName, logAutoDetectCall.serviceIdentifier, "service_name param must not render empty")
}

// TestALBTargetAlarmRule_QueriesMetricsWithAlarmDimensions drives
// ECS_ALB_Target_5XX_Errors_Alarm through the real aws_runbook.yaml and checks
// that get_alb_metrics_on_5xx parses the alarm's own dimensions (LoadBalancer,
// TargetGroup) — from configuration.metrics[].metricStat.metric.dimensions,
// which is a map, not the array the action's dimensions param used to be
// rendered as JSON from — and queries CloudWatch with them as a native list.
func TestALBTargetAlarmRule_QueriesMetricsWithAlarmDimensions(t *testing.T) {
	seedAccountTenantForTest(t)

	const loadBalancer = "app/nb-demo-alb/3c4737067cb64e8e"
	const targetGroup = "targetgroup/nb-demo-tg/abc123def456"
	sqsMessageBody := `{
		"version": "0",
		"id": "evt-id-alb-5xx",
		"detail-type": "CloudWatch Alarm State Change",
		"source": "aws.cloudwatch",
		"account": "123456789012",
		"time": "2026-09-23T10:05:00Z",
		"region": "us-east-1",
		"resources": ["arn:aws:cloudwatch:us-east-1:123456789012:alarm:nb-demo-alb-target-5xx"],
		"detail": {
			"alarmName": "nb-demo-alb-target-5xx",
			"state": {"value": "ALARM", "reason": "Threshold Crossed"},
			"previousState": {"value": "OK"},
			"configuration": {
				"metrics": [
					{
						"id": "m1",
						"metricStat": {
							"stat": "Sum",
							"period": 60,
							"metric": {
								"name": "HTTPCode_Target_5XX_Count",
								"namespace": "AWS/ApplicationELB",
								"dimensions": {"LoadBalancer": "app/nb-demo-alb/3c4737067cb64e8e", "TargetGroup": "targetgroup/nb-demo-tg/abc123def456"}
							}
						},
						"returnData": true
					}
				]
			}
		}
	}`

	rules, err := GetEventRules("")
	require.NoError(t, err, "Failed to load event rules")

	dummyAPI := &dummyAwsProvider{}
	processor := NewTemplatedEventBridgeProcessor(rules, dummyAPI)
	dummyHandler := &dummyProcessedEventHandler{}
	pCtx := providers.NewCloudProviderContext(context.Background())

	processedEvent, _, err := processSQSMessageBodyForEventBridgeEvent(pCtx, sqsMessageBody, processor, dummyHandler)
	require.NoError(t, err)
	require.NotEmpty(t, processedEvent.EventId, "rule did not match — alarmNamespace/alarmMetricName filters likely failed")

	// get_alb_metrics_on_5xx (aws_get_metric): dimensions param is a native list
	// (Dimensions []map[string]string), parsed and passed through to QueryMetrices
	// with the alarm's own LoadBalancer/TargetGroup values. This is the FIRST
	// QueryMetrices call — the log action (get_ecs_service_logs_on_alb_5xx) runs
	// first in the rule but doesn't call QueryMetrices, and the catch-all
	// CloudWatch_Alarm_State_Change_Generic rule's own metric action (different
	// metric_name: HTTPCode_Target_5XX_Count) runs afterward as a separate rule.
	require.GreaterOrEqual(t, len(dummyAPI.queryMetricesCalls), 1)
	metricArgs := dummyAPI.queryMetricesCalls[0]
	assert.Equal(t, "AWS/ApplicationELB", metricArgs.MetricNamespace)
	assert.Equal(t, []string{"TargetResponseTime"}, metricArgs.MetricNames)
	require.Len(t, metricArgs.Dimensions, 2, "expected LoadBalancer and TargetGroup dimensions to parse from the alarm, neither empty")
	dimByName := map[string]string{}
	for _, d := range metricArgs.Dimensions {
		dimByName[d["Name"]] = d["Value"]
	}
	assert.Equal(t, loadBalancer, dimByName["LoadBalancer"])
	assert.Equal(t, targetGroup, dimByName["TargetGroup"])
}
