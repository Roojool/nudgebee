package api

import (
	"fmt"
	"testing"

	"nudgebee/services/triage"
)

func TestCollapseCoFiring(t *testing.T) {
	row := func(id, subject, agg, source string, dt int) windowRow {
		return windowRow{
			id:     triage.AlertIdentity{ID: id, SubjectName: subject, AggregationKey: agg, TsOffsetS: dt},
			title:  id,
			source: source,
		}
	}
	wr := []windowRow{
		row("term-1", "i-0650", "EC2 State-change", "AWS_EventBridge", -2),
		row("term-2", "i-0650", "EC2 State-change", "AWS_CloudWatch_Alarm", 20),
		row("alb", "orders-alb", "alb-5xx", "AWS_EventBridge", 90),
	}
	coFiring := []triage.AlertIdentity{wr[0].id, wr[1].id, wr[2].id}

	got := collapseCoFiring(wr, coFiring, nil)

	if len(got) != 2 {
		t.Fatalf("expected 2 collapsed items, got %d: %+v", len(got), got)
	}
	if got[0].EventID != "term-1" || got[0].OccurrenceCount != 2 || len(got[0].Sources) != 2 {
		t.Errorf("first item should be the nearest firing with both copies folded in, got %+v", got[0])
	}
	for _, item := range got {
		if item.Relation != triage.RelationCoFiring {
			t.Errorf("relation = %q, want %q", item.Relation, triage.RelationCoFiring)
		}
	}
}

func TestCollapseCoFiringCapsDistinctItems(t *testing.T) {
	var wr []windowRow
	var coFiring []triage.AlertIdentity
	for i := 0; i < triage.MaxCoFiring+4; i++ {
		r := windowRow{id: triage.AlertIdentity{ID: fmt.Sprintf("e%d", i), SubjectName: fmt.Sprintf("svc-%d", i), AggregationKey: "down", TsOffsetS: i}}
		wr = append(wr, r)
		coFiring = append(coFiring, r.id)
	}

	if got := collapseCoFiring(wr, coFiring, nil); len(got) != triage.MaxCoFiring {
		t.Errorf("expected %d items, got %d", triage.MaxCoFiring, len(got))
	}
}

func TestCollapseCoFiringKeepsDistinctConfigChanges(t *testing.T) {
	change := func(id, object string) windowRow {
		return windowRow{id: triage.AlertIdentity{ID: id, SubjectOwner: "billing", SubjectName: object, IsConfigChange: true, TsOffsetS: -60}}
	}
	wr := []windowRow{change("cm-a", "billing-config"), change("cm-b", "billing-secrets"), change("cm-a2", "billing-config")}
	coFiring := []triage.AlertIdentity{wr[0].id, wr[1].id, wr[2].id}

	got := collapseCoFiring(wr, coFiring, nil)

	if len(got) != 2 {
		t.Fatalf("expected edits to two different objects to stay separate, got %d: %+v", len(got), got)
	}
	if got[0].OccurrenceCount != 2 {
		t.Errorf("repeat edits to one object should still fold, got %+v", got[0])
	}
}
