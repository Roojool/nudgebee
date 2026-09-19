// Package changedetect finds cloud accounts whose inventory changed since the
// last check and asks the knowledge graph to catch up. Deleted resources,
// resources of creatable types and rolled-out Kubernetes workloads become
// per-resource refs; anything else (a new EC2 instance, a re-activated row, more
// changes than a per-resource update is worth) becomes an account-scoped rebuild.
//
// It runs from a 10-minute cron and compares each account's inventory against
// watermarks it saved last time (knowledge_graph_inventory_marks). Every mark is
// only ever compared with the inventory column it was read from — never with a
// timestamp from another table, because timestamp and timestamptz columns do not
// agree on every environment.
package changedetect

import "time"

// Marks are the watermarks saved for one account. A nil field means "never
// probed", which is how the first sighting of an account is recognised.
type Marks struct {
	AccountID string
	TenantID  string

	CloudActiveCount *int
	CloudCreatedMark *time.Time // cloud_resourses.created_at (timestamp)

	K8sActiveCount  *int
	K8sCreatedMark  *time.Time // k8s_workloads.creation_time (timestamp)
	K8sDeployedMark *time.Time // k8s_workloads.last_deployed_time (timestamptz)

	NodeCount       *int
	NodeCreatedMark *time.Time // k8s_nodes.node_creation_time (timestamp)

	// ProbedAt is when this account was last probed successfully (the row's
	// updated_at). Read only; saving stamps the current time.
	ProbedAt time.Time
}

// CloudProbe is one reading of a cloud account's active inventory.
type CloudProbe struct {
	ActiveCount int
	CreatedMax  *time.Time
}

// K8sProbe is one reading of a Kubernetes account's active inventory.
type K8sProbe struct {
	WorkloadCount int
	CreatedMax    *time.Time
	DeployedMax   *time.Time
	NodeCount     int
	NodeCreated   *time.Time
}

// CloudDecision is what a cloud probe implies before the new rows are read.
type CloudDecision struct {
	First bool
	// CheckCreatedAfter is set when rows were created since the mark; the caller
	// reads them, because what they mean depends on the resource types.
	CheckCreatedAfter *time.Time
}

// DecideCloud compares a cloud probe with the saved marks.
func DecideCloud(prev *Marks, cur CloudProbe) CloudDecision {
	if prev == nil || prev.CloudActiveCount == nil {
		return CloudDecision{First: true}
	}
	if advanced(prev.CloudCreatedMark, cur.CreatedMax) {
		return CloudDecision{CheckCreatedAfter: markOrZero(prev.CloudCreatedMark)}
	}
	return CloudDecision{}
}

// CountChange classifies how an active count moved once the rows created since
// the last mark are accounted for.
type CountChange int

const (
	// CountExplained: the count moved exactly by the new rows.
	CountExplained CountChange = iota
	// CountRemoved: fewer active rows than expected; resources were deleted.
	CountRemoved
	// CountUnexplained: more active rows than expected; rows that existed before
	// the mark became active again. Those cannot be named, so the account rebuilds.
	CountUnexplained
)

// DecideCountChange compares the current active count with the previous count
// plus the rows created since the mark. A deletion and a creation in the same
// window leave the raw count unchanged but still show up here as a removal.
func DecideCountChange(prevCount, curCount, createdSinceMark int) CountChange {
	expected := prevCount + createdSinceMark
	switch {
	case curCount < expected:
		return CountRemoved
	case curCount > expected:
		return CountUnexplained
	default:
		return CountExplained
	}
}

// K8sDecision is what a Kubernetes probe implies.
type K8sDecision struct {
	First bool
	// Rebuild: workloads or nodes were created, or came back. New Kubernetes
	// resources get their relay-derived and cross-resource edges only from the
	// account build.
	Rebuild bool
	// WorkloadsRemoved and NodesRemoved: fewer active rows than last time; the
	// caller names the removed ones and sends delete refs.
	WorkloadsRemoved bool
	NodesRemoved     bool
	// RefsDeployedAfter is set when workloads were rolled out since the mark;
	// the caller turns them into per-resource upsert refs.
	RefsDeployedAfter *time.Time
}

// DecideK8s compares a Kubernetes probe with the saved marks.
func DecideK8s(prev *Marks, cur K8sProbe) K8sDecision {
	if prev == nil || prev.K8sActiveCount == nil || prev.NodeCount == nil {
		return K8sDecision{First: true}
	}
	var d K8sDecision
	workloads := decideK8sSet(*prev.K8sActiveCount, cur.WorkloadCount, advanced(prev.K8sCreatedMark, cur.CreatedMax))
	nodes := decideK8sSet(*prev.NodeCount, cur.NodeCount, advanced(prev.NodeCreatedMark, cur.NodeCreated))
	d.Rebuild = workloads == k8sSetAdded || nodes == k8sSetAdded
	d.WorkloadsRemoved = workloads == k8sSetRemoved
	d.NodesRemoved = nodes == k8sSetRemoved
	if advanced(prev.K8sDeployedMark, cur.DeployedMax) {
		d.RefsDeployedAfter = markOrZero(prev.K8sDeployedMark)
	}
	return d
}

type k8sSetChange int

const (
	k8sSetUnchanged k8sSetChange = iota
	k8sSetRemoved
	k8sSetAdded
)

// decideK8sSet classifies one Kubernetes inventory set (workloads or nodes). A
// new creation time means something was added, whatever the count did.
func decideK8sSet(prevCount, curCount int, createdAdvanced bool) k8sSetChange {
	switch {
	case createdAdvanced || curCount > prevCount:
		return k8sSetAdded
	case curCount < prevCount:
		return k8sSetRemoved
	default:
		return k8sSetUnchanged
	}
}

// advanced reports whether the inventory's max moved past the mark. A max that
// appears where there was none counts; a max that disappears (every row
// deleted) does not, because the count already reports that.
func advanced(mark, current *time.Time) bool {
	if current == nil {
		return false
	}
	return mark == nil || current.After(*mark)
}

func markOrZero(mark *time.Time) *time.Time {
	if mark != nil {
		return mark
	}
	zero := time.Time{}
	return &zero
}

// laterMark keeps a watermark from moving backwards. The settled rollout max can
// drop (the newest settled workload was rolled out again and is inside the
// settle window); saving that lower value would make the next run re-emit a ref
// for every workload rolled out since.
func laterMark(saved, current *time.Time) *time.Time {
	if saved != nil && (current == nil || saved.After(*current)) {
		return saved
	}
	return current
}
