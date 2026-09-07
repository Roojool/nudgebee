package tools

import "testing"

// Enabling remediation for cloud events means a generated command runs against real infrastructure
// with the account's own credentials. On Kubernetes the worst cases are recoverable — a deleted pod
// is rescheduled — so that list is deliberately permissive and leans on RBAC. Cloud is not
// symmetric: a terminated instance is gone, with its instance-store data and its identity. These
// tests pin the asymmetry, because the failure mode is unrecoverable and silent to review.
func TestValidateCommandSafetyBlocksIrreversibleCloudOperations(t *testing.T) {
	blocked := []struct {
		name    string
		command string
	}{
		{"terminate ec2", "aws ec2 terminate-instances --instance-ids i-0dcee3621b8456783 --region us-east-1"},
		{"delete rds", "aws rds delete-db-instance --db-instance-identifier prod-db --skip-final-snapshot"},
		{"delete eks cluster", "aws eks delete-cluster --name prod"},
		{"delete bucket", "aws s3api delete-bucket --bucket customer-backups"},
		{"delete volume", "aws ec2 delete-volume --volume-id vol-08386fdad869deb36"},
		{"delete stack", "aws cloudformation delete-stack --stack-name nudgebee-scenario-lab"},
		{"schedule kms deletion", "aws kms schedule-key-deletion --key-id abc --pending-window-in-days 7"},
		{"azure vm delete", "az vm delete --name prod-vm --resource-group rg"},
		{"gcloud instance delete", "gcloud compute instances delete prod-vm --zone us-central1-a"},
		{"s3 rb shorthand", "aws s3 rb s3://customer-backups --force"},
		{"efs", "aws efs delete-file-system --file-system-id fs-abc"},
		{"dynamodb", "aws dynamodb delete-table --table-name orders"},
		{"azure storage", "az storage account delete --name prodstore --resource-group rg"},
		{"azure aks", "az aks delete --name prod --resource-group rg"},
		{"gcp disks", "gcloud compute disks delete data-1 --zone us-central1-a"},
		{"gcp buckets", "gcloud storage buckets delete gs://customer-backups"},
		{"gsutil recursive", "gsutil rm -r gs://customer-backups"},
		{"gsutil single object", "gsutil rm gs://customer-backups/key"},
		// Spacing variation must not defeat the check - the caller normalises whitespace first.
		{"extra spacing", "aws  ec2   terminate-instances  --instance-ids i-abc"},
		// Case must not defeat it either.
		{"uppercase", "AWS EC2 TERMINATE-INSTANCES --instance-ids i-abc"},
		// Prefixes that do not change what actually runs. Each of these bypassed the guard when it
		// keyed on fields[0]: an assignment, a wrapper program and an absolute path are all still
		// the same aws invocation. They currently fail for an unrelated reason - the credential
		// router keys on fields[0] too, so they run unauthenticated - and relying on two functions
		// coincidentally agreeing is not a safety property.
		{"env assignment prefix", "AWS_PROFILE=prod aws ec2 terminate-instances --instance-ids i-abc"},
		{"two env assignments", "AWS_PROFILE=prod AWS_REGION=us-east-1 aws ec2 terminate-instances --instance-ids i-abc"},
		{"sudo prefix", "sudo aws ec2 terminate-instances --instance-ids i-abc"},
		{"absolute path", "/usr/local/bin/aws ec2 terminate-instances --instance-ids i-abc"},
		{"time wrapper", "time aws ec2 terminate-instances --instance-ids i-abc"},
		{"env wrapper", "env aws ec2 terminate-instances --instance-ids i-abc"},
		{"combined", "sudo AWS_PROFILE=prod /usr/bin/aws ec2 terminate-instances --instance-ids i-abc"},
	}

	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateCommandSafety(tc.command); err == nil {
				t.Fatalf("expected %q to be blocked, it was allowed", tc.command)
			}
		})
	}
}

func TestValidateCommandSafetyAllowsReversibleCloudOperations(t *testing.T) {
	// The point of the feature is that these run. A block list that also catches the remediations
	// themselves would leave the panel generating plans that can never execute, which is worse than
	// not shipping it: the operator is told there is a fix and then cannot apply it.
	allowed := []string{
		"aws ssm send-command --instance-ids i-abc --document-name AWS-RunShellScript --region us-east-1",
		"aws ec2 modify-instance-attribute --instance-id i-abc --instance-type t3.large --region us-east-1",
		"aws autoscaling set-desired-capacity --auto-scaling-group-name web --desired-capacity 4",
		"aws cloudwatch put-metric-alarm --alarm-name cpu-high --threshold 85 --region us-east-1",
		"aws ec2 modify-volume --volume-id vol-abc --size 100 --region us-east-1",
		"aws ec2 describe-instances --instance-ids i-abc --region us-east-1",
		"aws rds describe-db-instances --db-instance-identifier prod-db",
		// A resource NAMED like a blocked operation is not that operation. Blocking a read because
		// a tag value contains "delete-volume" teaches people the guard is noise and to route
		// around it, which costs more than it protects.
		"aws ec2 describe-instances --filters Name=tag:Name,Values=delete-volume --region us-east-1",
		"aws s3api list-objects --bucket delete-bucket-archive",
		"aws ec2 describe-volumes --filters Name=tag:Purpose,Values=terminate-instances-test",
		"aws cloudformation describe-stacks --stack-name delete-stack-runner",
		"gsutil ls gs://customer-backups",
		"gsutil cp report.csv gs://customer-backups/",
	}

	for _, cmd := range allowed {
		if err := ValidateCommandSafety(cmd); err != nil {
			t.Errorf("expected %q to be allowed, got %v", cmd, err)
		}
	}
}

func TestValidateCommandSafetyCloudListDoesNotAffectOtherCommands(t *testing.T) {
	// The cloud list is scoped to cloud CLI invocations on purpose. A kubectl command that happens to
	// name a resource called "delete-volume", or a shell command mentioning one, is not a cloud
	// operation and must not be blocked by a substring match.
	allowed := []string{
		"kubectl delete pod delete-volume-runner -n default",
		"kubectl get deployment terminate-instances-worker -n prod",
		"systemctl restart postgresql",
	}

	for _, cmd := range allowed {
		if err := ValidateCommandSafety(cmd); err != nil {
			t.Errorf("expected non-cloud command %q to be unaffected, got %v", cmd, err)
		}
	}
}

func TestValidateCommandSafetyStillBlocksShellCatastrophes(t *testing.T) {
	// Regression guard: the cloud branch was added after these, and returning early from it would
	// silently drop the original protections.
	for _, cmd := range []string{"rm -rf /", "dd if=/dev/zero of=/dev/sda", ":(){ :|:& };:"} {
		if err := ValidateCommandSafety(cmd); err == nil {
			t.Errorf("expected %q to remain blocked", cmd)
		}
	}
}
