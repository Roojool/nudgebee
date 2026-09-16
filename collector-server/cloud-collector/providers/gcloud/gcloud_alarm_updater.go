package gcloud

import (
	"fmt"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"nudgebee/collector/cloud/providers"
)

// UpdateGCPAlertPolicyThreshold changes the threshold, evaluation duration and/or the
// enabled switch of an existing GCP alert policy and writes back only those fields
// (update mask), so notification channels, documentation and other conditions stay as
// they are. It returns the values it replaced so a revert can restore them.
//
// policyName is the fully-qualified policy resource name
// (projects/<project>/alertPolicies/<id>).
func UpdateGCPAlertPolicyThreshold(ctx providers.CloudProviderContext, account providers.Account, policyName string, change providers.AlertRuleChange) (map[string]any, error) {
	if policyName == "" {
		return nil, fmt.Errorf("alert policy name is required")
	}

	session, err := getGcloudSessionFromAccount(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("failed to get gcloud session: %w", err)
	}

	client, err := monitoring.NewAlertPolicyClient(ctx.GetContext(), session.Opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create alert policy client: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			ctx.GetLogger().Warn("failed to close alert policy client", "error", cerr)
		}
	}()

	policy, err := client.GetAlertPolicy(ctx.GetContext(), &monitoringpb.GetAlertPolicyRequest{Name: policyName})
	if err != nil {
		return nil, fmt.Errorf("failed to get alert policy %q: %w", policyName, err)
	}

	previous, paths, err := applyGCPPolicyChange(policy, change)
	if err != nil {
		return nil, fmt.Errorf("alert policy %q: %w", policyName, err)
	}

	if _, err := client.UpdateAlertPolicy(ctx.GetContext(), &monitoringpb.UpdateAlertPolicyRequest{
		AlertPolicy: policy,
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: paths},
	}); err != nil {
		return nil, fmt.Errorf("failed to update alert policy %q: %w", policyName, err)
	}

	ctx.GetLogger().Info("updated GCP alert policy", "policy", policyName, "fields", paths)
	return previous, nil
}

// applyGCPPolicyChange mutates the fetched policy and returns the replaced values and
// the update-mask paths. Pure, so the guards are testable without GCP.
func applyGCPPolicyChange(policy *monitoringpb.AlertPolicy, change providers.AlertRuleChange) (map[string]any, []string, error) {
	threshold := change.Threshold
	durationSeconds := change.DurationMinutes * 60
	enabled := change.Enabled
	if r := change.Restore; len(r) > 0 {
		if v, ok := providers.RestoreNumber(r, "threshold"); ok {
			threshold = &v
		}
		if v, ok := providers.RestoreNumber(r, "duration_seconds"); ok {
			durationSeconds = int(v)
		}
		if v, ok := r["enabled"].(bool); ok {
			enabled = &v
		}
	}

	var conds []*monitoringpb.AlertPolicy_Condition_MetricThreshold
	for _, cond := range policy.GetConditions() {
		if mt := cond.GetConditionThreshold(); mt != nil {
			conds = append(conds, mt)
		}
	}

	previous := map[string]any{"enabled": policy.GetEnabled() == nil || policy.GetEnabled().GetValue()}
	if len(conds) > 0 {
		previous["threshold"] = conds[0].GetThresholdValue()
		previous["duration_seconds"] = float64(conds[0].GetDuration().AsDuration() / time.Second)
	}

	var paths []string
	if threshold != nil || durationSeconds > 0 {
		if len(conds) == 0 {
			return nil, nil, fmt.Errorf("%w: policy has no metric threshold condition", providers.ErrAlertRuleChangeUnsupported)
		}
		// One value for several conditions would silently change what each of
		// them means; only a policy whose conditions agree gets a single edit.
		for _, mt := range conds[1:] {
			if threshold != nil && mt.GetThresholdValue() != conds[0].GetThresholdValue() {
				return nil, nil, fmt.Errorf("%w: policy conditions use different thresholds", providers.ErrAlertRuleChangeUnsupported)
			}
			if durationSeconds > 0 && mt.GetDuration().AsDuration() != conds[0].GetDuration().AsDuration() {
				return nil, nil, fmt.Errorf("%w: policy conditions use different durations", providers.ErrAlertRuleChangeUnsupported)
			}
		}
		for _, mt := range conds {
			if threshold != nil {
				mt.ThresholdValue = *threshold
			}
			if durationSeconds > 0 {
				mt.Duration = durationpb.New(time.Duration(durationSeconds) * time.Second)
			}
		}
		paths = append(paths, "conditions")
	}
	if enabled != nil {
		policy.Enabled = wrapperspb.Bool(*enabled)
		paths = append(paths, "enabled")
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("%w: nothing to change", providers.ErrAlertRuleChangeUnsupported)
	}
	return previous, paths, nil
}
