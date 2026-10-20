package budget

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestDiffBudgetConfig_Create(t *testing.T) {
	after := &BudgetConfig{
		MonthlyCostLimit:   floatPtr(500),
		MonthlyCostEnabled: true,
	}
	changes := DiffBudgetConfig(nil, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, BudgetTypeMonthlyCost, changes[0].BudgetType)
	assert.Equal(t, ChangeActionCreate, changes[0].Action)
	assert.Nil(t, changes[0].PrevValue)
	assert.Equal(t, map[string]any{"limit": 500.0, "enabled": true}, changes[0].NewValue)
}

func TestDiffBudgetConfig_Create_IgnoresDefaultedFieldsNeverTouched(t *testing.T) {
	// Creating a row that only sets daily_cost must not also report
	// monthly_cost as "created" just because MonthlyCostEnabled defaults to
	// true on every new row (applyAutoEnable) while its limit stays nil.
	after := &BudgetConfig{
		MonthlyCostEnabled: true, // untouched default, no limit ever provided
		DailyCostLimit:     floatPtr(50),
		DailyCostEnabled:   true,
	}
	changes := DiffBudgetConfig(nil, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, BudgetTypeDailyCost, changes[0].BudgetType)
	assert.Equal(t, ChangeActionCreate, changes[0].Action)
}

func TestDiffBudgetConfig_Delete_OnlyActiveTypes(t *testing.T) {
	// Round-2 fix: deleting a config where only monthly_cost was ever
	// configured must produce exactly one Delete entry, not five.
	before := &BudgetConfig{
		MonthlyCostLimit:   floatPtr(500),
		MonthlyCostEnabled: true,
		// Every other field left at its zero value — never configured.
	}
	changes := DiffBudgetConfig(before, nil)

	assert.Len(t, changes, 1)
	assert.Equal(t, BudgetTypeMonthlyCost, changes[0].BudgetType)
	assert.Equal(t, ChangeActionDelete, changes[0].Action)
	assert.Equal(t, map[string]any{"limit": 500.0, "enabled": true}, changes[0].PrevValue)
	assert.Nil(t, changes[0].NewValue)
}

func TestDiffBudgetConfig_Delete_AllConfiguredTypesAndBypass(t *testing.T) {
	before := &BudgetConfig{
		BudgetDisabled:      true,
		MonthlyCostLimit:    floatPtr(500),
		MonthlyCostEnabled:  true,
		DailyCostLimit:      floatPtr(50),
		DailyCostEnabled:    true,
		MonthlyCountLimit:   intPtr(1000),
		MonthlyCountEnabled: true,
		DailyCountLimit:     intPtr(100),
		DailyCountEnabled:   true,
	}
	changes := DiffBudgetConfig(before, nil)

	assert.Len(t, changes, 5)
	for _, c := range changes {
		if c.BudgetType == BudgetTypeBypass {
			assert.Equal(t, ChangeActionDisable, c.Action)
		} else {
			assert.Equal(t, ChangeActionDelete, c.Action)
		}
	}
}

func TestDiffBudgetConfig_ValueIncrease(t *testing.T) {
	before := &BudgetConfig{MonthlyCostLimit: floatPtr(100), MonthlyCostEnabled: true}
	after := &BudgetConfig{MonthlyCostLimit: floatPtr(200), MonthlyCostEnabled: true}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, ChangeActionIncrease, changes[0].Action)
}

func TestDiffBudgetConfig_ValueDecrease(t *testing.T) {
	before := &BudgetConfig{DailyCountLimit: intPtr(100), DailyCountEnabled: true}
	after := &BudgetConfig{DailyCountLimit: intPtr(50), DailyCountEnabled: true}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, ChangeActionDecrease, changes[0].Action)
}

func TestDiffBudgetConfig_EnableDisableOnly(t *testing.T) {
	before := &BudgetConfig{MonthlyCostLimit: floatPtr(100), MonthlyCostEnabled: true}
	after := &BudgetConfig{MonthlyCostLimit: floatPtr(100), MonthlyCostEnabled: false}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, ChangeActionDisable, changes[0].Action)
}

func TestDiffBudgetConfig_SimultaneousEnableAndValueChange_EnableDominates(t *testing.T) {
	// Round-2 decision: when both the enabled flag and the value change in
	// the same save, classify as Enable/Disable — the value delta still
	// rides along in Prev/NewValue.
	before := &BudgetConfig{MonthlyCostLimit: floatPtr(100), MonthlyCostEnabled: false}
	after := &BudgetConfig{MonthlyCostLimit: floatPtr(300), MonthlyCostEnabled: true}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, ChangeActionEnable, changes[0].Action)
	assert.Equal(t, map[string]any{"limit": 100.0, "enabled": false}, changes[0].PrevValue)
	assert.Equal(t, map[string]any{"limit": 300.0, "enabled": true}, changes[0].NewValue)
}

func TestDiffBudgetConfig_BypassToggle(t *testing.T) {
	before := &BudgetConfig{}
	after := &BudgetConfig{BudgetDisabled: true}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 1)
	assert.Equal(t, BudgetTypeBypass, changes[0].BudgetType)
	assert.Equal(t, ChangeActionEnable, changes[0].Action)
}

func TestDiffBudgetConfig_NoChange(t *testing.T) {
	cfg := &BudgetConfig{MonthlyCostLimit: floatPtr(100), MonthlyCostEnabled: true}
	changes := DiffBudgetConfig(cfg, cfg)
	assert.Empty(t, changes)
}

func TestDiffBudgetConfig_MultipleTypesChangedInOneSave(t *testing.T) {
	before := &BudgetConfig{
		MonthlyCostLimit:   floatPtr(100),
		MonthlyCostEnabled: true,
	}
	after := &BudgetConfig{
		MonthlyCostLimit:   floatPtr(200),
		MonthlyCostEnabled: true,
		DailyCostLimit:     floatPtr(50),
		DailyCostEnabled:   true,
	}
	changes := DiffBudgetConfig(before, after)

	assert.Len(t, changes, 2)
	byType := map[string]BudgetTypeChange{}
	for _, c := range changes {
		byType[c.BudgetType] = c
	}
	assert.Equal(t, ChangeActionIncrease, byType[BudgetTypeMonthlyCost].Action)
	assert.Equal(t, ChangeActionCreate, byType[BudgetTypeDailyCost].Action)
}
