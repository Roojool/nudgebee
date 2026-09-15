package k8s_upgrade

import "testing"

func TestAWSRegionFlag(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"eu-west-1":          " --region eu-west-1",
		"us-gov-east-1":      " --region us-gov-east-1",
		"us-east-1 | jq .":   "",
		"us-east-1;rm -rf /": "",
	}
	for in, want := range cases {
		if got := awsRegionFlag(in); got != want {
			t.Errorf("awsRegionFlag(%q) = %q, want %q", in, got, want)
		}
	}
}
