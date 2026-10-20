import { safeJSONParse } from 'src/utils/common';

// The card answers one question: is something else on this machine to blame for
// my pod? Everything here exists to get the reader to that answer without
// scrolling two 15-row tables — see #39083.

// Which metric leads. The enricher runs on OOM kills AND on the contention
// alerts (throttling, never-ready, stuck waiting), and for those memory is the
// half that looks fine while CPU is the story. Showing both at equal weight, as
// the card used to, means the reader scans 30 rows to find the relevant 3.
const CPU_LED_EVENTS = new Set(['CPUThrottlingHigh', 'KubePodNotReady', 'KubeContainerWaiting']);

export const primaryMetricFor = (aggregationKey) => (CPU_LED_EVENTS.has(aggregationKey) ? 'cpu' : 'memory');

// How many ranked rows show before "Show all". The subject and any co-killed
// pods are always visible on top of these — they are the reason to open the card.
export const DEFAULT_VISIBLE_ROWS = 3;

export const parseNoisyNeighbours = (row) => {
  const evidences = Array.isArray(row?.evidences) ? row.evidences : [];
  const card = evidences.find((item) => item?.type === 'json' && item?.data && safeJSONParse(item.data)?.name === 'noisy_neighbours');
  const data = card ? safeJSONParse(card.data)?.data : null;
  if (!data) {
    return null;
  }
  return {
    nodeName: data.node_name,
    podsOnNode: data.pods_on_node,
    subjectPod: data.subject_pod,
    // Absent on cards written before this field existed. `undefined` means "the
    // enricher did not say", which must not render as "the pod was not measured".
    subjectMeasured: data.subject_measured,
    memory: {
      used: data.memory_used,
      allocatable: data.memory_allocatable,
      requested: data.memory_requested,
      // Sum of every container's limit on the node. Over allocatable the node is
      // oversubscribed — the pods are collectively allowed more than it has — and
      // that is the condition that ends in a kill. `kubectl describe node` reports
      // requests and limits side by side for the same reason.
      limits: data.memory_limits,
    },
    // Omitted entirely by providers that cannot measure CPU. Absent and zero mean
    // different things, so the caller checks for the key, not for a falsy value.
    cpu: data.cpu_allocatable === undefined ? null : { used: data.cpu_used, allocatable: data.cpu_allocatable },
    neighbours: Array.isArray(data.neighbours) ? data.neighbours : [],
    cpuNeighbours: Array.isArray(data.cpu_neighbours) ? data.cpu_neighbours : [],
    coKilled: Array.isArray(data.co_killed) ? data.co_killed : [],
  };
};

// "2s before" / "1m 5s after" — the phrasing that turns two rows into one story.
export const formatOffset = (seconds) => {
  if (seconds === 0) {
    return 'at the same moment';
  }
  const direction = seconds < 0 ? 'before' : 'after';
  const magnitude = Math.abs(seconds);
  if (magnitude < 60) {
    return `${magnitude}s ${direction}`;
  }
  const rem = magnitude % 60;
  return rem ? `${Math.floor(magnitude / 60)}m ${rem}s ${direction}` : `${Math.floor(magnitude / 60)}m ${direction}`;
};

const podKey = (namespace, podName) => `${namespace}/${podName}`;

// The per-row findings, as short chips instead of the bullet sentences that used
// to repeat under every row — two extra lines on nearly all 15 of them, which was
// the bulk of the card's height. The full sentence rides along for the tooltip.
// Ordered most actionable first: the card shows only the first one as a chip and
// puts the rest in its tooltip. Two chips on every row, saying what the Request
// and Limit columns already said with a dash, was most of the old card's height.
const memoryWarnings = (entry) => {
  const warnings = [];
  const container = entry.name || 'container';
  if (entry.memory_requested && entry.memory_used && entry.memory_used > entry.memory_requested) {
    const over = ((entry.memory_used - entry.memory_requested) / entry.memory_requested) * 100;
    warnings.push({
      label: `${over.toFixed(0)}% over request`,
      detail: `Container ${container} is using ${over.toFixed(2)}% more memory than it requested`,
    });
  }
  // No limit is the one that explains an OOM kill: nothing capped this container,
  // so it grew until the node had nothing left.
  if (!entry.memory_limit) {
    warnings.push({ label: 'unbounded', detail: `Container ${container} has no memory limit — nothing caps how far it can grow` });
  }
  if (!entry.memory_requested) {
    warnings.push({ label: 'no request', detail: `Container ${container} has no memory request, so the scheduler reserved nothing for it` });
  }
  return warnings;
};

const cpuWarnings = (entry) => {
  const warnings = [];
  const container = entry.name || 'container';
  if (entry.cpu_requested && entry.cpu_used && entry.cpu_used > entry.cpu_requested) {
    const over = ((entry.cpu_used - entry.cpu_requested) / entry.cpu_requested) * 100;
    warnings.push({
      label: `${over.toFixed(0)}% over request`,
      detail: `Container ${container} is using ${over.toFixed(2)}% more CPU than it requested`,
    });
  }
  if (!entry.cpu_requested) {
    warnings.push({ label: 'no request', detail: `Container ${container} has no CPU request, so the scheduler reserved nothing for it` });
  }
  return warnings;
};

// Builds the rows for one metric, ranked, with the subject and the co-killed
// pods folded in.
//
// Co-killed pods are added as rows even when they carry no usage reading. That is
// the case the issue is about: a pod killed between two scrapes, or scheduled onto
// the node moments before, has no series to rank — so the only list that ever
// mentioned it was the kernel messages further down the page.
export const buildMetricRows = ({ entries, coKilled, metric, allocatable }) => {
  const isMemory = metric === 'memory';
  const valueOf = (entry) => (isMemory ? entry.memory_used : entry.cpu_used);
  const coKilledByPod = new Map(coKilled.map((k) => [podKey(k.namespace, k.pod_name), k]));

  const rows = entries.map((entry) => {
    const value = valueOf(entry);
    const co = coKilledByPod.get(podKey(entry.namespace, entry.pod_name));
    return {
      key: `${podKey(entry.namespace, entry.pod_name)}/${entry.name || ''}`,
      podName: entry.pod_name,
      namespace: entry.namespace,
      container: entry.name,
      value,
      nodePct: allocatable > 0 && value !== undefined ? (value / allocatable) * 100 : null,
      request: isMemory ? entry.memory_requested : entry.cpu_requested,
      limit: isMemory ? entry.memory_limit : entry.cpu_limit,
      isSubject: Boolean(entry.is_subject),
      coKilledOffset: co ? co.offset_seconds : null,
      eventId: co ? co.event_id : null,
      warnings: isMemory ? memoryWarnings(entry) : cpuWarnings(entry),
      measured: true,
    };
  });

  const present = new Set(rows.map((r) => podKey(r.namespace, r.podName)));
  coKilled.forEach((k) => {
    if (present.has(podKey(k.namespace, k.pod_name))) {
      return;
    }
    rows.push({
      key: `co-killed/${podKey(k.namespace, k.pod_name)}`,
      podName: k.pod_name,
      namespace: k.namespace,
      // That kill's own event, so the reader can open it rather than search for
      // it. Naming the pod is the fix; being able to check it is what makes the
      // "one machine" claim verifiable.
      eventId: k.event_id,
      container: null,
      value: undefined,
      nodePct: null,
      request: undefined,
      limit: undefined,
      isSubject: false,
      coKilledOffset: k.offset_seconds,
      warnings: [],
      // No usage reading for this pod. Rendered as "not sampled", never as zero.
      measured: false,
    });
  });

  // Ranked by usage; unmeasured rows sort last rather than as zero.
  rows.sort((a, b) => (b.value ?? -1) - (a.value ?? -1));
  return rows;
};

// Which rows show before the reader asks for more: everything that earns its
// place (the subject, anything the node also killed) plus the top few consumers.
export const partitionRows = (rows, visibleCount = DEFAULT_VISIBLE_ROWS) => {
  const pinned = [];
  const ranked = [];
  rows.forEach((row) => {
    if (row.isSubject || row.coKilledOffset !== null) {
      pinned.push(row);
    } else {
      ranked.push(row);
    }
  });
  return { visible: [...pinned, ...ranked.slice(0, visibleCount)], hidden: ranked.slice(visibleCount) };
};
