package triage

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The decision is a pure function of the alerts active around the seed, so its
// rules are pinned here without a database. The replay of recorded incidents
// (incident_group_replay_test.go) proves the loading and the writes.

var decideNow = time.Date(2026, 10, 5, 22, 4, 19, 0, time.UTC)

// shopGraph is three services calling one database; order also calls the other two.
func shopGraph() *DependencyGraph {
	const (
		order, payment, inventory, database = "shop/order", "shop/payment", "shop/inventory", "shop/database"
	)
	return &DependencyGraph{
		Nodes: map[string]*ServiceNode{order: {}, payment: {}, inventory: {}, database: {}},
		Edges: map[string][]string{
			order:     {payment, inventory, database},
			payment:   {database},
			inventory: {database},
		},
		ReverseEdges: map[string][]string{
			payment:   {order},
			inventory: {order},
			database:  {order, payment, inventory},
		},
	}
}

func member(fp, subject string, start time.Time) alertMember {
	return alertMember{
		Fingerprint: fp, RepRow: "row-" + fp, Rows: []string{"row-" + fp},
		AggregationKey: fp, SubjectKey: "shop|" + subject, ServiceKey: "shop/" + subject,
		Subject: subject, Namespace: "shop", StartsAt: start, Recent: true, Open: true,
	}
}

func TestCollapseActiveRows(t *testing.T) {
	old := decideNow.Add(-40 * time.Minute)
	ended := decideNow.Add(-time.Minute)
	rows := []activeRow{
		// Newest start first, then earliest stored: the order the query returns.
		{ID: "pending-webhook", Fingerprint: strp("fp-pending"), AggregationKey: strp("ConnectionsPending"), SubjectName: strp("payment"), SubjectNamespace: strp("shop"), StartsAt: decideNow, CreatedAt: decideNow, Status: "FIRING"},
		{ID: "pending-agent", Fingerprint: strp("fp-pending"), AggregationKey: strp("ConnectionsPending"), SubjectName: strp("payment"), SubjectNamespace: strp("shop"), StartsAt: decideNow, CreatedAt: decideNow.Add(30 * time.Second), Status: "FIRING"},
		{ID: "own-copy", Fingerprint: strp("fp-seed"), StartsAt: decideNow, CreatedAt: decideNow.Add(-time.Second), Status: "FIRING"},
		{ID: "no-fingerprint", SubjectName: strp("cron"), SubjectNamespace: strp("shop"), StartsAt: decideNow.Add(-2 * time.Minute), CreatedAt: decideNow, Status: "FIRING"},
		{ID: "resolved-before", Fingerprint: strp("fp-gone"), SubjectName: strp("cache"), SubjectNamespace: strp("shop"), StartsAt: old, CreatedAt: old, Status: "CLOSED", EndsAt: &ended},
		{ID: "db-renotify", Fingerprint: strp("fp-db"), SubjectName: strp("database"), SubjectNamespace: strp("shop"), StartsAt: old, CreatedAt: decideNow.Add(-5 * time.Minute), Status: "FIRING"},
		{ID: "db-first", Fingerprint: strp("fp-db"), SubjectName: strp("database"), SubjectNamespace: strp("shop"), StartsAt: old, CreatedAt: old, Status: "FIRING"},
		{ID: "own-older-firing", Fingerprint: strp("fp-seed"), StartsAt: old, CreatedAt: old, Status: "FIRING"},
	}
	ownEarlier := decideNow.Add(-6 * time.Minute)
	// A flapper: fired ten minutes ago and again four minutes ago (both inside the
	// window), and an hour ago before that and never resolved.
	flap := func(id string, ago time.Duration) activeRow {
		at := decideNow.Add(-ago)
		return activeRow{ID: id, Fingerprint: strp("fp-flap"), SubjectName: strp("catalog"), SubjectNamespace: strp("shop"), StartsAt: at, CreatedAt: at, Status: "FIRING"}
	}
	rows = append(rows[:4:4], append([]activeRow{
		flap("flap-now", 4*time.Minute),
		{ID: "own-earlier-firing", Fingerprint: strp("fp-seed"), StartsAt: ownEarlier, CreatedAt: ownEarlier, Status: "CLOSED", EndsAt: &ownEarlier},
		flap("flap-before", 10*time.Minute),
	}, rows[4:]...)...)
	rows = append(rows, flap("flap-long-ago", time.Hour))
	// The query's order within one start is earliest stored first.
	rows[8], rows[9] = rows[9], rows[8]

	own, members := collapseActiveRows(rows, decideNow, "fp-seed")

	assert.Equal(t, []string{"own-copy"}, own.rows, "only another row of the same firing is the seed itself")
	assert.Equal(t, []string{"own-earlier-firing"}, own.others,
		"an earlier firing inside the window is kept apart; one from before the window is history")
	byFP := map[string]*alertMember{}
	for _, m := range members {
		byFP[m.Fingerprint] = m
	}
	require.Len(t, members, 5)

	flapper := byFP["fp-flap"]
	assert.Equal(t, "flap-before", flapper.RepRow, "its first firing inside the window stands for it, whichever firing is seen first")
	assert.Equal(t, decideNow.Add(-10*time.Minute), flapper.StartsAt)
	assert.Equal(t, []string{"flap-now", "flap-before"}, flapper.Rows,
		"the alert in this incident is its rows in the window; an hour-old firing still marked firing is not filed or consulted")

	pending := byFP["fp-pending"]
	assert.Equal(t, "pending-webhook", pending.RepRow, "the earliest-stored copy stands for the alert")
	assert.Equal(t, []string{"pending-webhook", "pending-agent"}, pending.Rows, "two ingest paths are one alert")
	assert.True(t, pending.Recent)

	db := byFP["fp-db"]
	assert.Equal(t, "db-first", db.RepRow)
	assert.Len(t, db.Rows, 2, "a re-notification is another row of the same firing")
	assert.False(t, db.Recent)
	assert.True(t, db.Open, "still firing when the seed started")

	assert.False(t, byFP["fp-gone"].Open, "resolved before the seed started")
	assert.Contains(t, byFP, "row:no-fingerprint", "a row without a fingerprint stands alone")

	t.Run("resolved after the seed started still counts as firing", func(t *testing.T) {
		after := decideNow.Add(2 * time.Minute)
		_, ms := collapseActiveRows([]activeRow{
			{ID: "db", Fingerprint: strp("fp-db"), SubjectName: strp("database"), SubjectNamespace: strp("shop"), StartsAt: old, CreatedAt: old, Status: "CLOSED", EndsAt: &after},
		}, decideNow, "fp-seed")
		require.Len(t, ms, 1)
		assert.True(t, ms[0].Open, "a seed triaged late must still see the alert that was firing when it began")
	})
}

func TestOwnRowsRepBefore(t *testing.T) {
	own := ownRows{rows: []string{"b", "a", "later"}, created: map[string]time.Time{
		"a": decideNow, "b": decideNow, "later": decideNow.Add(time.Minute),
	}}
	assert.Equal(t, "a", own.repBefore(decideNow.Add(30*time.Second), "seed"), "earliest stored, lowest id on a tie")
	assert.Equal(t, "", own.repBefore(decideNow.Add(-time.Second), "seed"), "the seed is the earliest: it stands for the alert itself")
	assert.Equal(t, "", ownRows{}.repBefore(decideNow, "seed"))
}

func TestRelateMembers(t *testing.T) {
	old := decideNow.Add(-40 * time.Minute)
	stale := func(m alertMember, open bool) *alertMember {
		m.StartsAt, m.Recent, m.Open = old, false, open
		return &m
	}
	recent := func(m alertMember) *alertMember { return &m }
	seed := member("seed", "payment", decideNow)

	relOf := func(related []relatedMember) map[string]relation {
		out := map[string]relation{}
		for _, m := range related {
			out[m.Fingerprint] = m.Relation
		}
		return out
	}

	t.Run("the three kinds of evidence", func(t *testing.T) {
		related, capped := relateMembers(shopGraph(), seed, false, []*alertMember{
			recent(member("same-subject", "payment", decideNow)),
			recent(member("neighbour", "order", decideNow)),
			recent(member("two-hops", "inventory", decideNow)),
			stale(member("db-still-down", "database", old), true),
			recent(member("elsewhere", "billing", decideNow)),
		})
		assert.False(t, capped)
		assert.Equal(t, map[string]relation{
			"same-subject":  relSameSubject,
			"neighbour":     relConnected,
			"two-hops":      relConnected,
			"db-still-down": relDependency,
		}, relOf(related), "an alert on a service with no path to the seed's stays out")
	})

	t.Run("an older alert counts only while firing, and only on something the seed depends on", func(t *testing.T) {
		related, _ := relateMembers(shopGraph(), seed, false, []*alertMember{
			stale(member("db-recovered", "database", old), false),
			stale(member("caller-still-alerting", "order", old), true),
			stale(member("same-subject-standing", "payment", old), true),
		})
		assert.Empty(t, related,
			"a resolved dependency, a standing alert on a caller, and a standing alert on the seed's own subject are not this incident")
	})

	t.Run("a change joins an alert still firing on its own object", func(t *testing.T) {
		standing := stale(member("db-down", "payment", old), true)
		related, _ := relateMembers(nil, seed, true, []*alertMember{standing})
		require.Len(t, related, 1)
		assert.Equal(t, relSameSubject, related[0].Relation)
	})

	t.Run("without a service map only the subject relates", func(t *testing.T) {
		related, _ := relateMembers(nil, seed, false, []*alertMember{
			recent(member("same-subject", "payment", decideNow)),
			recent(member("neighbour", "order", decideNow)),
		})
		assert.Equal(t, map[string]relation{"same-subject": relSameSubject}, relOf(related))
	})

	t.Run("subject cap", func(t *testing.T) {
		g := &DependencyGraph{Nodes: map[string]*ServiceNode{}, Edges: map[string][]string{}, ReverseEdges: map[string][]string{}}
		var ms []*alertMember
		for i := 0; i < incidentGroupSubjectCap+5; i++ {
			name := fmt.Sprintf("svc%02d", i)
			g.Edges["shop/"+name] = []string{"shop/payment"}
			g.ReverseEdges["shop/payment"] = append(g.ReverseEdges["shop/payment"], "shop/"+name)
			ms = append(ms, recent(member("fp-"+name, name, decideNow)))
		}
		related, capped := relateMembers(g, seed, false, ms)
		assert.True(t, capped)
		assert.Len(t, related, incidentGroupSubjectCap, "the cap stops adding rather than growing an estate-wide group")
	})
}

func TestCountDependents(t *testing.T) {
	seed := member("db-down", "database", decideNow)
	related := []relatedMember{
		{alertMember: member("order-pending", "order", decideNow), Relation: relConnected},
		{alertMember: member("payment-pending", "payment", decideNow), Relation: relConnected},
		{alertMember: member("inventory-pending", "inventory", decideNow), Relation: relConnected},
		{alertMember: member("db-config-change", "database", decideNow), Relation: relSameSubject},
	}
	got := countDependents(shopGraph(), seed, related)

	assert.Equal(t, 3, got["db-down"], "all three callers depend on the database")
	assert.Equal(t, 3, got["db-config-change"], "an alert on the same service has the same dependents")
	assert.Equal(t, 1, got["payment-pending"], "order calls payment")
	assert.Equal(t, 0, got["order-pending"], "nothing alerting calls order; the database does not depend on its caller")
	assert.Empty(t, countDependents(nil, seed, related), "no map, no counts")
}

func TestDecideGroup(t *testing.T) {
	seed := member("seed", "payment", decideNow)
	rel := func(m alertMember, r relation, groupHead string) relatedMember {
		return relatedMember{alertMember: m, Relation: r, GroupHead: groupHead}
	}
	base := func() decideInput {
		return decideInput{Seed: seed, SeedKey: seed.SubjectKey, TakeoverEnabled: true,
			DependedOnBy: map[string]int{}, Noisy: map[string]bool{}, HeadStarts: map[string]time.Time{}}
	}

	t.Run("nothing related", func(t *testing.T) {
		assert.Equal(t, outcomeNone, decideGroup(base()).Outcome)
	})

	t.Run("no group yet: the earliest of equals heads, the seed joins it", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{
			rel(member("crashloop", "payment", decideNow.Add(-5*time.Minute)), relSameSubject, ""),
			rel(member("oom", "payment", decideNow.Add(-8*time.Minute)), relSameSubject, ""),
		}
		d := decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-oom", d.HeadRow)
		assert.True(t, d.SeedIsChild)
		assert.Equal(t, []string{"row-crashloop"}, bringRows(d), "the other ungrouped alert is filed under the same head")
	})

	t.Run("no group yet: the seed is the one others depend on, so it heads and files them", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{rel(member("caller", "order", decideNow.Add(-4*time.Minute)), relConnected, "")}
		in.DependedOnBy["seed"] = 1
		d := decideGroup(in)
		assert.Equal(t, outcomeLead, d.Outcome)
		assert.Empty(t, d.HeadRow, "the seed row carries the lead")
		assert.False(t, d.SeedIsChild)
		assert.Equal(t, []string{"row-caller"}, bringRows(d),
			"an alert triaged before the seed existed is not triaged again, so the seed files it")
	})

	t.Run("an existing group is joined; a tie on dependents keeps its head", func(t *testing.T) {
		in := base()
		head := member("db-config-change", "payment", decideNow.Add(-20*time.Minute))
		in.Related = []relatedMember{
			rel(head, relSameSubject, "row-db-config-change"),
			rel(member("child", "order", decideNow.Add(-2*time.Minute)), relConnected, "row-db-config-change"),
		}
		in.DependedOnBy["seed"], in.DependedOnBy["db-config-change"] = 3, 3
		d := decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-db-config-change", d.HeadRow)
		assert.Contains(t, d.Reason, "same subject", "the strongest evidence speaks for the link")
		assert.Empty(t, d.Bring)
	})

	t.Run("the arriving alert replaces a head it outranks", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{
			rel(member("order-pending", "order", decideNow.Add(-4*time.Minute)), relConnected, "row-order-pending"),
			rel(member("inventory-slow", "inventory", decideNow.Add(-3*time.Minute)), relConnected, "row-order-pending"),
		}
		in.DependedOnBy["seed"], in.DependedOnBy["order-pending"] = 2, 1
		d := decideGroup(in)
		assert.Equal(t, outcomeTakeOver, d.Outcome)
		assert.Equal(t, "row-order-pending", d.MoveFrom)
		assert.False(t, d.SeedIsChild)
		assert.Contains(t, d.Reason, leaderTakeoverReasonPrefix)

		in.TakeoverEnabled = false
		off := decideGroup(in)
		assert.Equal(t, outcomeJoin, off.Outcome, "with the switch off the seed is filed under the head already there")
		assert.Equal(t, "row-order-pending", off.HeadRow)
	})

	t.Run("a quiet alert replaces a noisy head even with no more dependents", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{
			rel(member("queue-unroutable", "order", decideNow.Add(-4*time.Minute)), relConnected, "row-queue-unroutable"),
		}
		in.Noisy["queue-unroutable"] = true
		d := decideGroup(in)
		assert.Equal(t, outcomeTakeOver, d.Outcome, "a flapper does not stay the headline once a quieter related alert arrives")
		assert.Equal(t, "row-queue-unroutable", d.MoveFrom)

		in.Noisy["seed"] = true
		assert.Equal(t, outcomeJoin, decideGroup(in).Outcome, "two noisy alerts: nothing separates them, the head stays")

		in.Noisy["queue-unroutable"] = false
		in.DependedOnBy["seed"] = 3
		assert.Equal(t, outcomeJoin, decideGroup(in).Outcome, "a noisy alert never replaces a quiet head, however many depend on it")
	})

	t.Run("a later firing of the head's alert is one more row of the head, not its symptom", func(t *testing.T) {
		// The first firing started before the window and has resolved, so it is not
		// among the seed's own rows; a related alert is still filed under it.
		in := base()
		in.Related = []relatedMember{
			rel(member("caller", "order", decideNow.Add(-10*time.Minute)), relConnected, "row-seed-first-firing"),
			rel(member("other", "inventory", decideNow.Add(-2*time.Minute)), relConnected, ""),
		}
		in.HeadAlerts = map[string]string{"row-seed-first-firing": "seed"}
		d := decideGroup(in)
		assert.Equal(t, outcomeLead, d.Outcome)
		assert.Equal(t, "row-seed-first-firing", d.HeadRow)
		assert.False(t, d.SeedIsChild)
		assert.Equal(t, []string{"row-other"}, bringRows(d))
	})

	t.Run("a head from outside the active set has no rank and is not replaced", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{rel(member("child", "order", decideNow.Add(-2*time.Minute)), relConnected, "row-old-head")}
		in.DependedOnBy["seed"] = 5
		d := decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-old-head", d.HeadRow)
	})

	t.Run("an alert already placed stays where it is", func(t *testing.T) {
		in := base()
		in.OwnHead = "row-head"
		in.Related = []relatedMember{rel(member("head", "order", decideNow.Add(-4*time.Minute)), relConnected, "row-head")}
		in.DependedOnBy["seed"] = 9 // would outrank: an alert already filed must not move its head
		d := decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-head", d.HeadRow)
		assert.True(t, d.SeedIsChild)

		lead := base()
		lead.OwnLeads = "row-seed-first"
		ld := decideGroup(lead)
		assert.Equal(t, outcomeLead, ld.Outcome)
		assert.Equal(t, "row-seed-first", ld.HeadRow, "another row of the head's alert is filed under the row that leads")
		assert.False(t, ld.SeedIsChild, "and is not scored as a symptom of its own alert")
	})

	t.Run("a second copy of the heading alert points at the first", func(t *testing.T) {
		in := base()
		in.OwnRep = "row-seed-webhook"
		in.Related = []relatedMember{rel(member("caller", "order", decideNow), relConnected, "")}
		in.DependedOnBy["seed"] = 1
		d := decideGroup(in)
		assert.Equal(t, outcomeLead, d.Outcome)
		assert.Equal(t, "row-seed-webhook", d.HeadRow)
		assert.Equal(t, []string{"row-caller"}, bringRows(d))
	})

	t.Run("an alert brought along comes with every row it has in the incident", func(t *testing.T) {
		in := base()
		caller := member("caller", "order", decideNow.Add(-4*time.Minute))
		caller.Rows = []string{"row-caller", "row-caller-agent-copy", "row-caller-fired-again"}
		in.Related = []relatedMember{rel(caller, relConnected, "")}
		in.DependedOnBy["seed"] = 1
		d := decideGroup(in)
		assert.Equal(t, outcomeLead, d.Outcome)
		assert.Equal(t, []string{"row-caller", "row-caller-agent-copy", "row-caller-fired-again"}, bringRows(d),
			"none of them is triaged again, so leaving one out leaves it outside the incident for good")

		// When the brought alert heads, its other rows are filed under the row that leads.
		in.DependedOnBy["seed"], in.DependedOnBy["caller"] = 0, 1
		d = decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-caller", d.HeadRow)
		assert.Equal(t, []string{"row-caller-agent-copy", "row-caller-fired-again"}, bringRows(d))
	})

	t.Run("the seed's alert is ranked from, and headed by, its first free firing in the window", func(t *testing.T) {
		// The alert fired ten minutes before this firing; that row is stored but in
		// no group. A related alert started in between. Whichever of the alert's two
		// firings is triaged first, the alert ranks from the earlier and that row heads.
		in := base()
		in.Related = []relatedMember{rel(member("between", "payment", decideNow.Add(-5*time.Minute)), relSameSubject, "")}
		assert.Equal(t, "row-between", decideGroup(in).HeadRow, "ranked from this firing alone, the alert in between would head")

		first := decideNow.Add(-10 * time.Minute)
		in.OwnRep, in.Seed.StartsAt = "row-seed-first-firing", first
		in.HeadStarts["row-seed-first-firing"] = first
		d := decideGroup(in)
		assert.Equal(t, outcomeLead, d.Outcome)
		assert.Equal(t, "row-seed-first-firing", d.HeadRow)
		assert.Equal(t, first, d.HeadStart)
		assert.False(t, d.SeedIsChild)
		assert.Equal(t, []string{"row-between"}, bringRows(d))
	})

	t.Run("related alerts in two groups: join the better head's, never merge", func(t *testing.T) {
		in := base()
		in.Related = []relatedMember{
			rel(member("head-a", "order", decideNow.Add(-6*time.Minute)), relConnected, "row-head-a"),
			rel(member("head-b", "inventory", decideNow.Add(-9*time.Minute)), relConnected, "row-head-b"),
		}
		in.DependedOnBy["head-a"], in.DependedOnBy["head-b"], in.DependedOnBy["seed"] = 2, 1, 0
		d := decideGroup(in)
		assert.Equal(t, outcomeJoin, d.Outcome)
		assert.Equal(t, "row-head-a", d.HeadRow)
		assert.Empty(t, d.Bring, "the other group is left as it is")
		assert.Empty(t, d.MoveFrom)
	})
}

func bringRows(d groupDecision) []string {
	out := make([]string, 0, len(d.Bring))
	for _, b := range d.Bring {
		out = append(out, b.Row)
	}
	sort.Strings(out)
	return out
}

// simAlert is one alert in the in-memory replay below.
type simAlert struct {
	fp, subject string
	start       time.Duration
	noisy       bool
}

// simulate triages the alerts in the given order through the pure decision, all
// of them already stored (a burst released from a queue). It returns each alert's
// head row (its own row for an alert that heads or stands alone) and what its
// last triage told scoring: whether it is a symptom filed under another alert.
func simulate(t *testing.T, graph *DependencyGraph, alerts []simAlert, order []int) (map[string]string, map[string]bool) {
	t.Helper()
	parent := map[string]string{} // row -> head row
	leads := map[string]bool{}
	child := map[string]bool{}
	rowOf := func(fp string) string { return "row-" + fp }
	for _, idx := range order {
		a := alerts[idx]
		seed := member(a.fp, a.subject, decideNow.Add(a.start))
		var members []*alertMember
		for j, o := range alerts {
			if j == idx {
				continue
			}
			m := member(o.fp, o.subject, decideNow.Add(o.start))
			members = append(members, &m)
		}
		related, _ := relateMembers(graph, seed, false, members)
		links := groupLinks{parent: parent, leads: leads}
		in := decideInput{Seed: seed, SeedKey: seed.SubjectKey, Related: related, TakeoverEnabled: true,
			DependedOnBy: countDependents(graph, seed, related), Noisy: map[string]bool{}, HeadStarts: map[string]time.Time{}}
		for _, o := range alerts {
			in.Noisy[o.fp] = o.noisy
		}
		var seedFiled bool
		in.OwnHead, in.OwnLeads, seedFiled, child[a.fp] = ownPlacement(links, rowOf(a.fp), nil, nil)
		if seedFiled {
			continue
		}
		for i := range in.Related {
			head, lead := links.groupOf(in.Related[i].Rows)
			if head == "" {
				head = lead
			}
			in.Related[i].GroupHead = head
		}
		d := decideGroup(in)
		head := d.HeadRow
		switch d.Outcome {
		case outcomeNone:
			child[a.fp] = false
			continue
		case outcomeJoin:
			parent[rowOf(a.fp)] = head
			leads[head] = true
		case outcomeLead:
			if head == "" || head == rowOf(a.fp) {
				head = rowOf(a.fp)
			} else {
				parent[rowOf(a.fp)] = head
				leads[head] = true
			}
		case outcomeTakeOver:
			head = rowOf(a.fp)
			for child, p := range parent {
				if p == d.MoveFrom {
					parent[child] = head
				}
			}
			delete(leads, d.MoveFrom)
			parent[d.MoveFrom] = head
			leads[head] = true
		}
		for _, b := range d.Bring {
			if _, filed := parent[b.Row]; !filed && b.Row != head {
				parent[b.Row] = head
				leads[head] = true
			}
		}
		child[a.fp] = d.SeedIsChild && head != rowOf(a.fp)
	}
	out := map[string]string{}
	for _, a := range alerts {
		out[a.fp] = (groupLinks{parent: parent, leads: leads}).root(rowOf(a.fp))
		require.LessOrEqual(t, depthOf(parent, rowOf(a.fp)), 1, "links stay a one-level star")
	}
	return out, child
}

func depthOf(parent map[string]string, row string) int {
	d := 0
	for {
		p, ok := parent[row]
		if !ok || d > 5 {
			return d
		}
		row, d = p, d+1
	}
}

func permutations(n int) [][]int {
	var out [][]int
	var walk func(cur []int, used []bool)
	walk = func(cur []int, used []bool) {
		if len(cur) == n {
			out = append(out, append([]int{}, cur...))
			return
		}
		for i := 0; i < n; i++ {
			if !used[i] {
				used[i] = true
				walk(append(cur, i), used)
				used[i] = false
			}
		}
	}
	walk(nil, make([]bool, n))
	return out
}

// The answer must not depend on which alert is triaged first. Every order of
// triaging the same five alerts gives one incident headed by the database.
func TestDecideGroup_SameAnswerInEveryOrder(t *testing.T) {
	alerts := []simAlert{
		{fp: "order-pending", subject: "order"},
		{fp: "payment-pending", subject: "payment"}, // same second as the two beside it
		{fp: "inventory-pending", subject: "inventory"},
		{fp: "order-slow", subject: "order", start: 30 * time.Second},
		{fp: "db-down", subject: "database", start: 4 * time.Minute},
	}
	for _, order := range permutations(len(alerts)) {
		heads, _ := simulate(t, shopGraph(), alerts, order)
		for fp, head := range heads {
			require.Equal(t, "row-db-down", head, "order %v: %s", order, fp)
		}
	}
}

// Triage runs again on stored rows (a re-score), and an alert may already have
// been filed by a related alert's triage before its own runs. Triaging every
// alert a second and third time, in any order, moves nothing — and scoring is
// told "symptom" for exactly the alerts filed under another, never for the head.
func TestDecideGroup_TriagingAgainChangesNothing(t *testing.T) {
	alerts := []simAlert{
		{fp: "order-pending", subject: "order"},
		{fp: "payment-pending", subject: "payment"},
		{fp: "inventory-pending", subject: "inventory"},
		{fp: "db-down", subject: "database", start: 4 * time.Minute},
	}
	for _, order := range permutations(len(alerts)) {
		once, _ := simulate(t, shopGraph(), alerts, order)
		for _, again := range permutations(len(alerts)) {
			repeated := append(append(append([]int{}, order...), again...), order...)
			heads, child := simulate(t, shopGraph(), alerts, repeated)
			require.Equal(t, once, heads, "order %v then %v", order, again)
			for fp, head := range heads {
				require.Equal(t, head != "row-"+fp, child[fp], "order %v then %v: %s", order, again, fp)
			}
		}
	}
}

// A hub that fires all week must not head an incident it merely accompanies —
// whether it is triaged before or after the alert it sits beside.
func TestDecideGroup_NoisyHubNeverHeadsInEitherOrder(t *testing.T) {
	alerts := []simAlert{
		{fp: "queue-unroutable", subject: "database", noisy: true},
		{fp: "payment-not-ready", subject: "payment", start: 2 * time.Minute},
	}
	for _, order := range permutations(len(alerts)) {
		heads, _ := simulate(t, shopGraph(), alerts, order)
		assert.Equal(t, "row-payment-not-ready", heads["queue-unroutable"], "order %v", order)
		assert.Equal(t, "row-payment-not-ready", heads["payment-not-ready"], "order %v", order)
	}
}

// An alert on a service with no path to the others never joins them.
func TestDecideGroup_UnrelatedAlertStaysOut(t *testing.T) {
	alerts := []simAlert{
		{fp: "payment-pending", subject: "payment"},
		{fp: "db-down", subject: "database", start: time.Minute},
		{fp: "billing-disk", subject: "billing", start: time.Minute},
	}
	for _, order := range permutations(len(alerts)) {
		heads, _ := simulate(t, shopGraph(), alerts, order)
		assert.Equal(t, "row-billing-disk", heads["billing-disk"], "order %v", order)
		assert.Equal(t, "row-db-down", heads["payment-pending"], "order %v", order)
	}
}

func TestGroupLinks(t *testing.T) {
	links := groupLinks{
		parent: map[string]string{"child": "head", "old-chain-child": "old-head", "old-head": "head"},
		leads:  map[string]bool{"head": true, "old-head": true},
	}
	filed, leads := links.groupOf([]string{"copy-without-link", "child"})
	assert.Equal(t, "head", filed, "an alert is in a group if any of its rows is")
	assert.Empty(t, leads)

	filed, leads = links.groupOf([]string{"head"})
	assert.Empty(t, filed)
	assert.Equal(t, "head", leads)

	filed, _ = links.groupOf([]string{"old-chain-child"})
	assert.Equal(t, "head", filed, "a chain written before the lock existed resolves to its root")

	filed, leads = links.groupOf([]string{"nobody"})
	assert.Empty(t, filed)
	assert.Empty(t, leads)

	loop := groupLinks{parent: map[string]string{"a": "b", "b": "a"}}
	assert.NotPanics(t, func() { loop.root("a") }, "a cycle among old links must not loop")
}

func TestOwnPlacement(t *testing.T) {
	links := groupLinks{
		parent: map[string]string{
			"filed-row":       "other-alert",
			"copy-of-head":    "head-row",
			"copy-of-symptom": "other-alert",
			"second-firing":   "first-firing",
			"filed-earlier":   "first-firing",
		},
		leads: map[string]bool{"other-alert": true, "head-row": true, "first-firing": true},
	}
	// "first-firing" started before the window: it is not among the rows around
	// the seed, only its fingerprint says it is the same alert.
	sameAlert := func(row string) bool { return row == "first-firing" }
	cases := []struct {
		name             string
		seed             string
		others           []string
		wantHead         string
		wantLeads        string
		wantFiled, child bool
	}{
		{name: "first row of an alert nothing has placed", seed: "new-row"},
		{name: "a related alert's triage already filed this row: it stays, a symptom",
			seed: "filed-row", wantFiled: true, child: true},
		{name: "a re-scored head is the head, not a symptom of itself",
			seed: "head-row", others: []string{"copy-of-head"}, wantLeads: "head-row"},
		{name: "a re-scored head whose group has no other row of its own",
			seed: "other-alert", wantLeads: "other-alert"},
		{name: "a row already filed under another row of its own alert is one more row of the head",
			seed: "copy-of-head", others: []string{"head-row"}, wantFiled: true},
		{name: "a new row of an alert that heads a group follows the row that leads",
			seed: "new-row", others: []string{"head-row", "copy-of-head"}, wantLeads: "head-row"},
		{name: "a new row of an alert filed as a symptom follows it there",
			seed: "new-row", others: []string{"copy-of-symptom"}, wantHead: "other-alert"},
		{name: "a third firing follows the second to the first, which left the window: still the head's alert, not its symptom",
			seed: "third-firing", others: []string{"second-firing"}, wantLeads: "first-firing"},
		{name: "a row already filed under its alert's first firing from before the window is not a symptom",
			seed: "filed-earlier", wantFiled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head, leads, filed, child := ownPlacement(links, tc.seed, tc.others, sameAlert)
			assert.Equal(t, tc.wantHead, head, "own head")
			assert.Equal(t, tc.wantLeads, leads, "own leading row")
			assert.Equal(t, tc.wantFiled, filed, "seed row already filed")
			assert.Equal(t, tc.child, child, "seed is a symptom")
		})
	}
}

func TestEarlierFiringPlacement(t *testing.T) {
	links := groupLinks{
		parent: map[string]string{
			"second-firing":  "first-firing", // the alert heads its own group
			"filed-firing":   "database-down",
			"caller-pending": "first-firing",
		},
		leads: map[string]bool{"first-firing": true, "database-down": true},
	}
	sameAlert := func(row string) bool { return row == "first-firing" }
	none := map[string]bool{}

	head, leads := earlierFiringPlacement(links, []string{"second-firing"}, none, sameAlert)
	assert.Empty(t, head+leads, "nothing but the alert's own repeats holds that group: a flapper does not keep its incident open")

	head, leads = earlierFiringPlacement(links, []string{"second-firing"}, map[string]bool{"first-firing": true}, sameAlert)
	assert.Empty(t, head)
	assert.Equal(t, "first-firing", leads, "another alert fired beside it in the window: a further firing is one more row of the head")

	head, leads = earlierFiringPlacement(links, []string{"first-firing"}, map[string]bool{"first-firing": true}, nil)
	assert.Empty(t, head)
	assert.Equal(t, "first-firing", leads, "the earlier firing is itself the head")

	head, leads = earlierFiringPlacement(links, []string{"filed-firing"}, map[string]bool{"database-down": true}, sameAlert)
	assert.Equal(t, "database-down", head, "a replaced head firing again stays filed where its alert is — it does not arrive as new and take the lead back")
	assert.Empty(t, leads)

	head, leads = earlierFiringPlacement(links, []string{"filed-firing", "second-firing"}, map[string]bool{"first-firing": true}, sameAlert)
	assert.Equal(t, "first-firing", leads, "a group that has gone quiet is skipped for one that is live")
	assert.Empty(t, head)

	head, leads = earlierFiringPlacement(links, []string{"never-linked"}, map[string]bool{"first-firing": true}, sameAlert)
	assert.Empty(t, head+leads)
}

func TestFirstFreeFiring(t *testing.T) {
	at := func(minutes int) time.Time { return decideNow.Add(time.Duration(minutes) * time.Minute) }
	starts := map[string]time.Time{
		"later": at(3), "recent": at(-4), "recent-copy": at(-4), "earliest": at(-12), "filed": at(-14), "leading": at(-13),
	}
	links := groupLinks{parent: map[string]string{"filed": "some-head"}, leads: map[string]bool{"leading": true}}
	// Newest start first, earliest stored first within a start.
	others := []string{"later", "recent", "recent-copy", "earliest", "leading", "filed"}

	rep, start := firstFreeFiring(links, others, starts, decideNow)
	assert.Equal(t, "earliest", rep, "the earliest firing before the seed's that is in no group; a firing already in a group is not reused")
	assert.Equal(t, at(-12), start)

	rep, _ = firstFreeFiring(links, []string{"later", "recent", "recent-copy"}, starts, decideNow)
	assert.Equal(t, "recent", rep, "the earliest-stored row of that firing")

	rep, _ = firstFreeFiring(links, []string{"later", "leading", "filed"}, starts, decideNow)
	assert.Empty(t, rep, "a later firing does not stand for an earlier one, and placed firings are left where they are")
}
