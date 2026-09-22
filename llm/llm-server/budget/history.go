package budget

// Budget type identifiers used in change-history entries.
const (
	BudgetTypeMonthlyCost  = "monthly_cost"
	BudgetTypeDailyCost    = "daily_cost"
	BudgetTypeMonthlyCount = "monthly_count"
	BudgetTypeDailyCount   = "daily_count"
	BudgetTypeBypass       = "bypass"
)

// Change actions for budget change-history entries.
const (
	ChangeActionCreate   = "CREATE"
	ChangeActionDelete   = "DELETE"
	ChangeActionIncrease = "INCREASE"
	ChangeActionDecrease = "DECREASE"
	ChangeActionEnable   = "ENABLE"
	ChangeActionDisable  = "DISABLE"
)

// BudgetTypeChange describes one classified change to a single budget type
// within one upsert/delete call.
type BudgetTypeChange struct {
	BudgetType string
	Action     string
	PrevValue  map[string]any
	NewValue   map[string]any
}

// DiffBudgetConfig compares before/after BudgetConfig rows (either may be nil —
// nil before means the row was just created, nil after means it was deleted)
// and returns one BudgetTypeChange per budget type that actually changed.
//
// A type with a nil limit on both sides is treated as never configured, even
// if its `*_enabled` flag differs (e.g. MonthlyCostEnabled defaults to true on
// every new row) — nothing user-visible changed, so no entry is emitted.
func DiffBudgetConfig(before, after *BudgetConfig) []BudgetTypeChange {
	if before == nil {
		before = &BudgetConfig{}
	}
	if after == nil {
		after = &BudgetConfig{}
	}

	var changes []BudgetTypeChange
	if c := diffNumericType(BudgetTypeMonthlyCost, before.MonthlyCostLimit, before.MonthlyCostEnabled, after.MonthlyCostLimit, after.MonthlyCostEnabled); c != nil {
		changes = append(changes, *c)
	}
	if c := diffNumericType(BudgetTypeDailyCost, before.DailyCostLimit, before.DailyCostEnabled, after.DailyCostLimit, after.DailyCostEnabled); c != nil {
		changes = append(changes, *c)
	}
	if c := diffNumericType(BudgetTypeMonthlyCount, before.MonthlyCountLimit, before.MonthlyCountEnabled, after.MonthlyCountLimit, after.MonthlyCountEnabled); c != nil {
		changes = append(changes, *c)
	}
	if c := diffNumericType(BudgetTypeDailyCount, before.DailyCountLimit, before.DailyCountEnabled, after.DailyCountLimit, after.DailyCountEnabled); c != nil {
		changes = append(changes, *c)
	}
	if c := diffBypass(before.BudgetDisabled, after.BudgetDisabled); c != nil {
		changes = append(changes, *c)
	}
	return changes
}

// diffNumericType classifies the change to one numeric-limit budget type (USD
// cost or integer count). Enabled-flag transitions are checked before value
// transitions so that a save which flips both (e.g. enabling a type while
// also setting its first value) classifies as Enable/Disable, not
// Increase/Decrease — the value delta still rides along in PrevValue/NewValue
// either way.
func diffNumericType[T int | float64](budgetType string, beforeLimit *T, beforeEnabled bool, afterLimit *T, afterEnabled bool) *BudgetTypeChange {
	switch {
	case beforeLimit == nil && afterLimit == nil:
		return nil
	case beforeLimit == nil && afterLimit != nil:
		return &BudgetTypeChange{BudgetType: budgetType, Action: ChangeActionCreate, NewValue: numericValue(*afterLimit, afterEnabled)}
	case beforeLimit != nil && afterLimit == nil:
		return &BudgetTypeChange{BudgetType: budgetType, Action: ChangeActionDelete, PrevValue: numericValue(*beforeLimit, beforeEnabled)}
	case beforeEnabled != afterEnabled:
		action := ChangeActionEnable
		if !afterEnabled {
			action = ChangeActionDisable
		}
		return &BudgetTypeChange{BudgetType: budgetType, Action: action, PrevValue: numericValue(*beforeLimit, beforeEnabled), NewValue: numericValue(*afterLimit, afterEnabled)}
	case *beforeLimit != *afterLimit:
		action := ChangeActionIncrease
		if *afterLimit < *beforeLimit {
			action = ChangeActionDecrease
		}
		return &BudgetTypeChange{BudgetType: budgetType, Action: action, PrevValue: numericValue(*beforeLimit, beforeEnabled), NewValue: numericValue(*afterLimit, afterEnabled)}
	default:
		return nil
	}
}

// diffBypass classifies a change to the super-admin "disable all budget
// checks" toggle. It only has Enable/Disable actions — there is no
// create/delete concept for a boolean that always exists on the row.
func diffBypass(before, after bool) *BudgetTypeChange {
	if before == after {
		return nil
	}
	action := ChangeActionEnable
	if !after {
		action = ChangeActionDisable
	}
	return &BudgetTypeChange{
		BudgetType: BudgetTypeBypass,
		Action:     action,
		PrevValue:  map[string]any{"enabled": before},
		NewValue:   map[string]any{"enabled": after},
	}
}

func numericValue[T int | float64](limit T, enabled bool) map[string]any {
	return map[string]any{"limit": limit, "enabled": enabled}
}
