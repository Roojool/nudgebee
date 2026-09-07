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
		// Spacing variation must not defeat the check - the caller normalises whitespace first.
		{"extra spacing", "aws  ec2   terminate-instances  --instance-ids i-abc"},
		// Case must not defeat it either.
		{"uppercase", "AWS EC2 TERMINATE-INSTANCES --instance-ids i-abc"},
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
