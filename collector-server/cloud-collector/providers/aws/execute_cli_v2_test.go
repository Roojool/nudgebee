package aws

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAwsV2Command(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantErr string
	}{
		{name: "ordinary read", command: "aws ec2 describe-instances"},
		{name: "binary format global option", command: "aws --cli-binary-format raw-in-base64-out lambda invoke --function-name example output.json"},
		{name: "caller identity remains available", command: "aws sts get-caller-identity"},
		{name: "assume role blocked", command: "aws sts assume-role --role-arn arn:example", wantErr: "sts assume-role"},
		{name: "assume role blocked behind boolean global flag", command: "aws --no-cli-pager sts assume-role --role-arn arn:example", wantErr: "sts assume-role"},
		{name: "assume role blocked behind valued global flag", command: "aws sts --region us-east-1 assume-role --role-arn arn:example", wantErr: "sts assume-role"},
		{name: "session token blocked", command: "aws sts get-session-token", wantErr: "sts get-session-token"},
		{name: "configure blocked", command: "aws configure set region us-east-1", wantErr: "configure"},
		{name: "sso blocked behind global flag", command: "aws --region us-east-1 sso login", wantErr: "sso"},
		{name: "wrong executable", command: "sh -c 'aws s3 ls'", wantErr: "must start with aws"},
		{name: "missing operation", command: "aws", wantErr: "must start with aws"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAwsV2Command(tc.command)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
