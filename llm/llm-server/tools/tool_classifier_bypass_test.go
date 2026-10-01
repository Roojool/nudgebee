package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nudgebee/llm/security"
	"nudgebee/llm/tools/core"
)

// TestStaticClassifiersDoNotReadMutationsAsRead runs the bypasses reported in
// #39880 (C3 shell syntax, C4 help flags, C5 escaped newlines) through each
// tool's static classifier. None of them may come back as read; "" sends the
// command on to the LLM classifier.
func TestStaticClassifiersDoNotReadMutationsAsRead(t *testing.T) {
	ctx := security.NewRequestContext(context.Background(), nil, nil, nil, nil)
	type classifier interface {
		InferToolRequestType(*security.RequestContext, string, string) (core.ToolRequestType, error)
	}
	cases := []struct {
		name    string
		tool    classifier
		command string
	}{
		// C3: the command runs through a shell.
		{"aws chain", AwsCliTool{}, "aws ec2 describe-instances; aws ec2 terminate-instances --instance-ids i-1"},
		{"aws and", AwsCliTool{}, "aws s3 ls && aws s3 rb s3://bucket --force"},
		{"aws substitution", AwsCliTool{}, "aws s3 ls $(aws s3 rb s3://bucket --force)"},
		{"aws newline", AwsCliTool{}, "aws s3 ls\naws s3 rb s3://bucket --force"},
		{"az chain", AzureCliTool{}, "az vm list && az vm delete -n vm1 -g rg --yes"},
		{"gcloud chain", GcpCliTool{}, "gcloud compute instances list; gcloud compute instances delete vm1 --quiet"},
		{"bq chain", GcpCliTool{}, "bq query 'select 1'; bq rm -f -t ds.table"},
		// C4: a help flag that is not a help request.
		{"aws commented help", AwsCliTool{}, "aws ec2 terminate-instances --instance-ids i-1 # --help"},
		{"aws trailing -h after flags", AwsCliTool{}, "aws ec2 terminate-instances --instance-ids i-1 -h"},
		{"aws ssm payload -h", AwsCliTool{}, `aws ssm send-command --document-name AWS-RunShellScript --parameters commands=["rm -rf /data -h"]`},
		{"gcloud ssh command -h", GcpCliTool{}, `gcloud compute ssh vm1 --command "rm -rf /data" -h`},
		{"az run-command -h", AzureCliTool{}, `az vm run-command invoke -g rg -n vm1 --command-id RunShellScript --scripts "rm -rf /data" -h`},
		{"kubectl help as flag value", KubectlExecuteTool{}, "kubectl delete pod x --cascade -h"},
		{"kubectl help after exec separator", KubectlExecuteTool{}, "kubectl exec pod -- rm -rf /data -h"},
		{"helm help as flag value", HelmExecuteTool{}, "helm uninstall rel --description -h"},
		{"argocd help after flag", ArgoCDExecuteTool{}, "argocd app delete x --cascade -h"},
		{"redis trailing help is a key", RedisExecuteTool{}, "redis-cli DEL session --help"},
		{"rabbitmq-api trailing -h goes to curl", RabbitExecuteTool{}, "rabbitmq-api DELETE /api/queues/%2F/q1 -h"},
		// C5: gh/glab turn a literal \n into a newline before running.
		{"gh escaped newline", GithubCliTool{}, `gh pr list \n rm -rf ~`},
		{"glab escaped newline", GitlabCliTool{}, `glab mr list \n rm -rf ~`},
		// C6: RabbitMQ forms that write while looking like a read.
		{"rabbitmq-api method override", RabbitExecuteTool{}, "rabbitmq-api GET /api/queues/%2F/q1 -X DELETE"},
		{"rabbitmq-api extra url", RabbitExecuteTool{}, "rabbitmq-api GET /api/overview http://collector.example.com/"},
		{"rabbitmqadmin get ack without requeue", RabbitExecuteTool{}, "rabbitmqadmin get queue=q1 ackmode=ack_requeue_false"},
		{"rabbitmqadmin get reject without requeue", RabbitExecuteTool{}, "rabbitmqadmin get queue=q1 count=5 ackmode=reject_requeue_false"},
		{"rabbitmqadmin get requeue false", RabbitExecuteTool{}, "rabbitmqadmin get queue=task_queue requeue=false"},
		{"rabbitmqadmin get ack-mode flag", RabbitExecuteTool{}, "rabbitmqadmin get --queue q1 --ack-mode ack_requeue_false"},
		{"rabbitmqadmin get flag before ack-mode", RabbitExecuteTool{}, "rabbitmqadmin get --queue --ack-mode ack_requeue_false"},
		{"rabbitmqadmin get flag before requeue setting", RabbitExecuteTool{}, "rabbitmqadmin get --verbose requeue=false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": tc.command})
			require.NoError(t, err)
			got, err := tc.tool.InferToolRequestType(ctx, "", string(input))
			require.NoError(t, err)
			assert.NotEqual(t, core.ToolRequestTypeRead, got, tc.command)
		})
	}
}

// TestStaticClassifiersStillReadPlainReads guards the other direction: genuine
// help requests and reads keep their static answer.
func TestStaticClassifiersStillReadPlainReads(t *testing.T) {
	ctx := security.NewRequestContext(context.Background(), nil, nil, nil, nil)
	type classifier interface {
		InferToolRequestType(*security.RequestContext, string, string) (core.ToolRequestType, error)
	}
	cases := []struct {
		name    string
		tool    classifier
		command string
	}{
		{"aws describe", AwsCliTool{}, "aws ec2 describe-instances --region us-east-1"},
		{"aws quoted query", AwsCliTool{}, `aws ec2 describe-instances --query 'Reservations[].Instances[].{ID:InstanceId}'`},
		{"aws version", AwsCliTool{}, "aws --version"},
		{"az help", AzureCliTool{}, "az vm delete --help"},
		{"gcloud help", GcpCliTool{}, "gcloud compute instances delete -h"},
		{"kubectl help", KubectlExecuteTool{}, "kubectl delete pod x -h"},
		{"kubectl get", KubectlExecuteTool{}, "kubectl get pods -n prod -o wide"},
		{"kubectl quoted hash", KubectlExecuteTool{}, `kubectl get pods -o jsonpath='{.items[*].metadata.name} #x'`},
		{"helm help", HelmExecuteTool{}, "helm uninstall --help"},
		{"argocd help", ArgoCDExecuteTool{}, "argocd app delete -h"},
		{"gh list", GithubCliTool{}, "gh pr list --state open"},
		{"gh quoted newline", GithubCliTool{}, `gh pr view 12 --comments --jq ".body\n"`},
		{"redis help command", RedisExecuteTool{}, "redis-cli help"},
		{"redis bare command", RedisExecuteTool{}, "PING"},
		{"redis info", RedisExecuteTool{}, "redis-cli INFO memory"},
		{"rabbitmq-api plain get", RabbitExecuteTool{}, "rabbitmq-api GET /api/queues"},
		{"rabbitmqadmin list", RabbitExecuteTool{}, "rabbitmqadmin list queues name consumers"},
		{"rabbitmqadmin get requeues by default", RabbitExecuteTool{}, "rabbitmqadmin get queue=q1 count=1"},
		{"rabbitmqadmin get explicit requeue", RabbitExecuteTool{}, "rabbitmqadmin get queue=q1 ackmode=ack_requeue_true"},
		{"rabbitmqadmin get queue lookup", RabbitExecuteTool{}, "rabbitmqadmin get queue name=q1 vhost=/"},
		{"rabbitmqadmin get queue named requeue", RabbitExecuteTool{}, "rabbitmqadmin get --queue requeue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": tc.command})
			require.NoError(t, err)
			got, err := tc.tool.InferToolRequestType(ctx, "", string(input))
			require.NoError(t, err)
			assert.Equal(t, core.ToolRequestTypeRead, got, tc.command)
		})
	}
}

// TestRedisClassifiesTheCommandItRuns checks that the Redis tool runs the
// string its classifier read. A redis-cli placed after another command used to
// make the tool run the input as given, so the shell ran that other command.
func TestRedisClassifiesTheCommandItRuns(t *testing.T) {
	assert.Equal(t, "redis-cli INFO", redisCommandAsRun("redis-cli INFO"))
	assert.Equal(t, "redis-cli PING", redisCommandAsRun("  PING "))
	assert.Equal(t, "redis-cli command bash -c 'id' redis-cli", redisCommandAsRun("command bash -c 'id' redis-cli"))
	assert.Equal(t, "redis-cli /tmp/x/redis-cli INFO", redisCommandAsRun("/tmp/x/redis-cli INFO"))
}

// TestRedisCallParsesPaddedJSON: JSON input with leading whitespace is still
// JSON to Call. Read as a raw command instead, its later lines would run as
// shell commands the classifier never saw.
func TestRedisCallParsesPaddedJSON(t *testing.T) {
	_, err := RedisExecuteTool{}.Call(core.NbToolContext{Ctx: security.NewRequestContext(context.Background(), nil, nil, nil, nil)},
		core.NBToolCallRequest{Command: " \n{\"x\":1}"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is empty", "padded JSON without a command must be rejected, not run as text")
}

// TestRabbitArgsInputIsClassified covers the {"args": ...} input shape most
// RabbitMQ calls use.
func TestRabbitArgsInputIsClassified(t *testing.T) {
	ctx := security.NewRequestContext(context.Background(), nil, nil, nil, nil)
	got, err := RabbitExecuteTool{}.InferToolRequestType(ctx, "", `{"args": "get queue=q1 ackmode=ack_requeue_false", "instance": "rabbit-test"}`)
	require.NoError(t, err)
	assert.Equal(t, core.ToolRequestTypeUpdate, got)
	got, err = RabbitExecuteTool{}.InferToolRequestType(ctx, "", `{"args": "list queues", "instance": "rabbit-test"}`)
	require.NoError(t, err)
	assert.Equal(t, core.ToolRequestTypeRead, got)
}
