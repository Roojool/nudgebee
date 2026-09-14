// Not for OSS

// Ids and copy this module asserts against, all read from the components that render
// the Infra > Cloud > AWS > RDS surface. Kept here so a rename in app/src is one edit.
//
//   app/src/components/cloudaccount/rds/Instances.tsx  InstancesView
//   app/src/components/cloudaccount/rds/Summary.tsx    RdsSummary

// InstancesView declares this at Instances.tsx:759 and passes it to CloudAccountTable,
// which forwards it to CustomTable as `id`. CustomTable renders it on the <table>,
// `${id}-body` on the <tbody> and `${id}-no-data` on the empty panel's <h2>.
export const INSTANCES_TABLE = "rdsOptimizeInstancesTable";

// INSTANCE_HEADER in Instances.tsx:33-43, verbatim. The ninth column is deliberately
// unnamed — it holds the per-row actions menu — so only the eight labelled ones are
// asserted. Kept as the component declares them; renderedColumnLabel below applies what
// CustomTable does to them on the way to the DOM.
export const INSTANCE_COLUMNS = [
  "Instance Name",
  "State",
  "Engine",
  "vCPU / Memory",
  "Launch Time",
  "Storage",
  "Tags",
  "Storage Type",
];

// Zero-based index of the State column within INSTANCE_COLUMNS. RDS leads with Instance
// Name and puts State second, where EC2 leads with Instance ID and puts State fifth —
// the two headers are not interchangeable.
export const STATE_COLUMN = 1;

// CustomTable renders every header through MUI's capitalize() (CustomTable.jsx:27, 1264),
// which upper-cases the first character and leaves the rest alone. RDS is the first
// module where that is visible: 'vCPU / Memory' reaches the DOM as 'VCPU / Memory', so
// an assertion against the declared name misses the column while the seven headers that
// already start upper-case match either way. Proved by run 34760405148, which failed on
// that column alone.
export function renderedColumnLabel(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1);
}

// The `id` each toolbar widget is given in InstancesView (Instances.tsx:925-981).
// SearchInput puts its id straight on the <input>; FilterDropdown builds its trigger id
// as `auto-complete-${toKebabCase(id)}` (ds/FilterDropdown.jsx:1074-1081), and these are
// already kebab-case so they kebab to themselves.
export const SEARCH_INPUT_ID = "rds-instances-search";
export const SEARCH_PLACEHOLDER = "Search By Instance Name";
export const FILTER = {
  state: "rds-filter-state",
  region: "rds-filter-region",
  tagKey: "rds-filter-tag-key",
  tagValue: "rds-filter-tag-value",
} as const;

// FilterDropdown renders `label` as the trigger's leading text.
export const FILTER_LABEL: Record<keyof typeof FILTER, string> = {
  state: "State",
  region: "Region",
  tagKey: "Tag Key",
  tagValue: "Tag Value",
};

// The tabs CustomTable builds from InstancesView's `expandable.tabs`
// (Instances.tsx:996-1062), in order. RDS carries Performance Insights and Logs where
// EC2 carries Vulnerabilities, so this list is module-specific.
export const DRILLDOWN_TABS = ["Details", "Monitoring", "Events", "Performance Insights", "Logs", "Action History"];

// RDS_ACTIONS in app/src/components/cloudaccount/resourceActions.ts:102-132, reached via
// ACTION_CATALOGS.AmazonRDS. Ordered so a healthy instance resolves to `reboot` first;
// the suite only ever opens an action's confirmation and cancels it.
export const LIFECYCLE_ACTIONS = ["reboot", "start", "stop"];

export const LIFECYCLE_ACTION_LABEL: Record<string, string> = {
  reboot: "Reboot Instance",
  start: "Start Instance",
  stop: "Stop Instance",
};

// `stop` is the one RDS action declared confirmationType: 'strict'
// (resourceActions.ts:117), so its dialog additionally renders the typed-confirmation
// input. The other two open the standard footer only.
export const STRICT_CONFIRM_ACTIONS = ["stop"];
