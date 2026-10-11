// Not for OSS

// Ids and copy this module asserts against, all read from the components that render
// Infra > Cloud > AWS > Services. Kept here so a rename in app/src is one edit.
//
//   app/src/components/cloudaccount/CloudAccountServices.tsx   CloudAccountServices, CloudAccountResources, ResourceDetails
//   app/src/components/cloudaccount/ServiceRecommendations.tsx ServiceRecommendations
//   app/src/components/cloudaccount/CostChart.tsx              TotalCostChart

// ListingLayout puts `id` on the wrapping DS Card (CloudAccountServices.tsx:372). The
// same id is reused by the EC2/RDS/S3/ECS instance views, but only one of them is
// ever mounted at a time, so on the Services tab it still resolves uniquely.
export const LISTING_ROOT = "right-sizing";

// CloudAccountTable forwards `id` to CustomTable, which renders it on the <table>,
// `${id}-body` on the <tbody> and `${id}-no-data` on the empty panel's <h2>.
export const SERVICES_TABLE = "servicesTable";
export const RESOURCES_ROOT = "service-resource-listing";
export const RESOURCES_TABLE = "service-resource-listing-table";
// ServiceRecommendations gives BOTH its ListingLayout card and its table this id.
export const RECOMMENDATIONS_TABLE = "service-recommendations-table";
// TotalCostChart's defaults (CostChart.tsx:27-29) — the Services drill-down passes neither.
export const COST_TREND_ROOT = "graph-section";
export const COST_CANVAS = "cloudUtilizationCostChartId";

// servicesHeader in CloudAccountServices.tsx. The seventh column is unnamed — it holds
// the per-row actions menu — so only the six labelled ones are asserted.
export const SERVICE_COLUMNS = ["Service Name", "Resources", "Recommendations", "Total Cost", "% Change", "Estimated Savings"];
// RESOURCES_HEADER and TABLE_COLUMNS in the two drill-down components.
export const RESOURCE_COLUMNS = ["Resource Name", "Type", "Region", "Tags", "Recommendations", "Total Cost", "Estimated Savings"];
export const RECOMMENDATION_COLUMNS = ["Severity", "Category", "Rule Name", "Instance", "Recommendation", "Savings"];

// Zero-based positions within a service row's data cells. CustomTable appends the
// expand-toggle cell AFTER the data cells (CustomTable.jsx:462-490), so these indices
// are unaffected by the row being expandable.
export const COLUMN = { serviceName: 0, resources: 1, recommendations: 2, totalCost: 3 } as const;

// The `id` each toolbar FilterDropdown is given. FilterDropdown builds its trigger id
// as `auto-complete-${toKebabCase(id)}` (FilterDropdown.jsx:1074-1081); toKebabCase
// only lowercases and joins on whitespace/underscores, so these kebab to themselves.
export const FILTER = {
  serviceName: "services-filter-service-name",
  region: "services-filter-region",
  tagKey: "services-filter-tag-key",
} as const;

export const FILTER_LABEL: Record<keyof typeof FILTER, string> = {
  serviceName: "Service Name",
  region: "Region",
  tagKey: "Tag Key",
};

export const RESOURCE_FILTER = {
  type: "resources-filter-type",
  region: "resources-filter-region",
  tagKey: "resources-filter-tag-key",
} as const;

export const RESOURCE_FILTER_LABEL: Record<keyof typeof RESOURCE_FILTER, string> = {
  type: "Type",
  region: "Region",
  tagKey: "Tag Key",
};

// The tabs CustomTable builds from the listing's `expandable.tabs`, in order.
export const DRILLDOWN_TABS = ["Resources", "Cost Trend", "Recommendations"];

// Header name → the order_by column the listing sends (CloudAccountServices.tsx:250-255).
export const SORT_FIELD: Record<string, string> = {
  "Total Cost": "sum_spend_amount",
  Recommendations: "count_recommendation",
  "Estimated Savings": "sum_recommendation_estimated_savings",
};

// Every listing on this surface — services, resources, the filter-option lookups —
// goes through the same operation (api1/resources/index.ts:255-258); the helper tells
// them apart by the inlined group_by.
export const OP_LIST_GROUPINGS = "k8s_resource_groupings";

// MENU_ITEMS in cloudaccount/common.tsx — all four shipped disabled.
export const ROW_ACTIONS = [
  { id: "start-instance", label: "Start Instance" },
  { id: "stop-instance", label: "Stop Instance" },
  { id: "terminate-instance", label: "Terminate Instance" },
  { id: "add-alarms", label: "Add Alarms" },
];

// CloudAccountResources swaps its generic table for ECSInstances on these names
// (CloudAccountServices.tsx:796), so the Resources drill-down cases avoid them.
export const ECS_SERVICE_NAMES = ["ecs", "AmazonECS", "Amazon Elastic Container Service"];

// A TIME_PICK_SHORTCUTS entry (data/constants.ts) whose window is a whole calendar
// month, so the request it produces can be checked against a date computed here.
export const DATE_SHORTCUT = "Last Month";
