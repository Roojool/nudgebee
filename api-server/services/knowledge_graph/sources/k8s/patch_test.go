package k8s

import "testing"

func TestDetectedWorkloadKinds(t *testing.T) {
	got := detectedWorkloadKinds([]string{"Deployment", "Pod", "Job", "CronJob"})
	if len(got) != 2 || got[0] != "Deployment" || got[1] != "CronJob" {
		t.Errorf("got %v, want [Deployment CronJob]: pods and jobs churn and are not counted", got)
	}
}
