package aws

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDescribeAlarms serves pages in order and records the requested AlarmTypes.
type fakeDescribeAlarms struct {
	pages      []*cloudwatch.DescribeAlarmsOutput
	errAt      int // page index that fails; -1 for none
	calls      int
	alarmTypes []cwtypes.AlarmType
}

func (f *fakeDescribeAlarms) DescribeAlarms(_ context.Context, in *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error) {
	f.alarmTypes = in.AlarmTypes
	i := f.calls
	f.calls++
	if i == f.errAt {
		return nil, errors.New("boom")
	}
	return f.pages[i], nil
}

func TestDescribeRegionAlarmRules_MetricAndCompositeAcrossPages(t *testing.T) {
	client := &fakeDescribeAlarms{
		errAt: -1,
		pages: []*cloudwatch.DescribeAlarmsOutput{
			{
				MetricAlarms: []cwtypes.MetricAlarm{{
					AlarmName: aws.String("orders-rds-cpu-high"),
					AlarmArn:  aws.String("arn:aws:cloudwatch:us-east-1:123456789012:alarm:orders-rds-cpu-high"),
					Namespace: aws.String("AWS/RDS"),
					Threshold: aws.Float64(80),
					Period:    aws.Int32(300),
				}},
				NextToken: aws.String("p2"),
			},
			{
				CompositeAlarms: []cwtypes.CompositeAlarm{{
					AlarmName: aws.String("orders-degraded"),
					AlarmRule: aws.String(`ALARM("orders-rds-cpu-high") AND ALARM("orders-alb-latency")`),
				}},
			},
		},
	}

	rules, err := describeRegionAlarmRules(context.Background(), slog.Default(), client, "us-east-1", "123456789012")
	require.NoError(t, err)
	require.Len(t, rules, 2)
	// api-server writes back through the ARN, so sync must store it.
	assert.Equal(t, "arn:aws:cloudwatch:us-east-1:123456789012:alarm:orders-rds-cpu-high", rules[0].ExternalID)

	assert.ElementsMatch(t, []cwtypes.AlarmType{cwtypes.AlarmTypeMetricAlarm, cwtypes.AlarmTypeCompositeAlarm}, client.alarmTypes,
		"composite alarms are only returned when requested explicitly")

	metric := rules[0]
	assert.Equal(t, "orders-rds-cpu-high", metric.Name)
	assert.Equal(t, "AWS_CloudWatch_Alarm", metric.Source)
	assert.Contains(t, metric.Expr, `"AlarmName":"orders-rds-cpu-high"`)
	assert.Equal(t, "80.000000", metric.Labels["aws_event_threshold"])

	composite := rules[1]
	assert.Equal(t, "orders-degraded", composite.Name)
	assert.Equal(t, "AWS_CloudWatch_Alarm", composite.Source)
	assert.Equal(t, "composite", composite.Labels["aws_alarm_type"])
	assert.Contains(t, composite.Expr, "AlarmRule")
	assert.NotEmpty(t, composite.Expr)
}

func TestDescribeRegionAlarmRules_ErrorDropsPartialRegion(t *testing.T) {
	client := &fakeDescribeAlarms{
		errAt: 1,
		pages: []*cloudwatch.DescribeAlarmsOutput{
			{MetricAlarms: []cwtypes.MetricAlarm{{AlarmName: aws.String("a")}}, NextToken: aws.String("p2")},
		},
	}

	rules, err := describeRegionAlarmRules(context.Background(), slog.Default(), client, "us-east-1", "123456789012")
	require.Error(t, err)
	assert.Nil(t, rules, "a region is stored whole or not at all")
}

func TestSkipAlarmRegion(t *testing.T) {
	// The shape Janus hit daily: DescribeAlarms in me-south-1 failing at dial.
	dialFailure := &url.Error{Op: "Post", URL: "https://monitoring.me-south-1.amazonaws.com/", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}}

	assert.True(t, skipAlarmRegion(slog.Default(), dialFailure, "me-south-1"))
	assert.False(t, skipAlarmRegion(slog.Default(), errors.New("AccessDenied"), "us-east-1"),
		"a real failure must still be reported")
}
