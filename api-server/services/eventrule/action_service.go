package eventrule

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"nudgebee/services/common"
	"nudgebee/services/internal/database"
	"nudgebee/services/security"
	"strings"

	"github.com/jmoiron/sqlx"
)

// proxyActionIntegrationTypes maps proxy action names to the integration types
// that should appear in the datasource_id dropdown.
var proxyActionIntegrationTypes = map[string][]string{
	"proxy_db_query":    {"postgresql", "mysql", "clickhouse", "mssql", "oracle"},
	"proxy_ssh_command": {"ssh"},
}

//go:embed event_actions_template.json
var embeddedJSONContent []byte

func readJSONFile() ([]AlertActionTemplate, error) {
	var config []AlertActionTemplate
	err := common.UnmarshalJson(embeddedJSONContent, &config)
	return config, err
}

func LoadEventActions(context *security.RequestContext) error {
	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		context.GetLogger().Error("Error getting database manager", "error", err)
		return err
	}

	actions, err := readJSONFile()
	if err != nil {
		context.GetLogger().Error("Error loading event actions", "error", err)
	}
	for _, action := range actions {
		if action.Source == nil || *action.Source == "" {
			defaultSource := "prometheus"
			action.Source = &defaultSource
		}

		params, err := common.MarshalJson(action.Params)
		if err != nil {
			context.GetLogger().Error("Error marshalling params", "error", err)
			continue
		}

		_, err = dbms.Db.Exec(`INSERT INTO agent_playbook_action (name, display_name, description, category, params, action_name, source, alert_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (name) DO
			UPDATE SET
				description = EXCLUDED.description,
				params = EXCLUDED.params,
				action_name = EXCLUDED.action_name,
				display_name = EXCLUDED.display_name,
				category = EXCLUDED.category,
				source = EXCLUDED.source,
				alert_type = EXCLUDED.alert_type`,
			action.Name, action.DisplayName, action.Description, action.Category, params, action.ActionName, *action.Source, action.AlertType)
		if err != nil {
			context.GetLogger().Error("Error inserting playbook action", "error", err)
			return err
		}
	}
	return nil
}

// PromQLLabelNames runs one instant PromQL query against the account's metrics
// backend and returns the label NAMES carried by the series it matched. It is
// supplied by the caller rather than called directly here because resolving the
// account's metrics provider lives in the observability package, which this
// package cannot import (observability imports eventrule).
//
// A nil slice with a nil error is not a valid result: returning no error must
// mean "the backend answered", because ListAction uses exactly that distinction
// to decide whether to narrow the action list or fall back to nudgebee-source
// actions only.
type PromQLLabelNames func(sc *security.RequestContext, accountID, query string) ([]string, error)

func ListAction(context *security.RequestContext, request ListActionsRequest, labelNames PromQLLabelNames) ([]AlertActionTemplate, error) {
	source := request.Source
	if source == "" {
		source = "prometheus"
	}

	var labels []string
	if source == "prometheus" && labelNames != nil {
		// topk(1, ...) keeps the probe cheap: we only need the label names of one
		// matching series, not the data. Some expressions are not valid inside
		// topk, so fall back to the query as written when the wrapped form matches
		// nothing -- the same reason the previous relay-based probe kept a
		// bare-query fallback.
		found, err := labelNames(context, request.CloudAccountId, fmt.Sprintf("topk(1, %s)", request.Query))
		if err != nil || len(found) == 0 {
			if bare, bareErr := labelNames(context, request.CloudAccountId, request.Query); bareErr == nil {
				found, err = bare, nil
			}
		}
		if err != nil {
			context.GetLogger().Warn("prometheus query failed, will return non-prometheus actions only", "error", err)
		} else {
			labels = found
		}
	}

	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		context.GetLogger().Error("Error getting database manager", "error", err)
		return nil, err
	}

	alertType := request.AlertType
	if alertType == "" {
		alertType = "metric"
	}

	var rows *sqlx.Rows
	if source == "prometheus" {
		if labels != nil {
			rows, err = dbms.Db.Queryx(`
				SELECT name, display_name, description, category, params, action_name
				FROM agent_playbook_action
				WHERE (category = $1 OR category = 'All' OR source = 'nudgebee')
				  AND (alert_type IS NULL OR alert_type = $2)`,
				extractQueryTypeFromLabels(labels), alertType)
		} else {
			// Prometheus query failed (no k8s agent) — return nudgebee-source actions only
			rows, err = dbms.Db.Queryx(`
				SELECT name, display_name, description, category, params, action_name
				FROM agent_playbook_action
				WHERE source = 'nudgebee'
				  AND (alert_type IS NULL OR alert_type = $1)`,
				alertType)
		}
	} else {
		rows, err = dbms.Db.Queryx(`
			SELECT name, display_name, description, category, params, action_name
			FROM agent_playbook_action
			WHERE (source = $1 OR source = 'nudgebee' OR category = 'All')
			  AND (alert_type IS NULL OR alert_type = $2)`,
			source, alertType)
	}

	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Error("Error closing rows", "error", err)
		}
	}()

	actionTemplate := make([]AlertActionTemplate, 0)
	for rows.Next() {
		var action AlertActionTemplate
		var params []byte
		var actionName sql.NullString

		err = rows.Scan(&action.Name, &action.DisplayName, &action.Description, &action.Category, &params, &actionName)
		if err != nil {
			context.GetLogger().Error("failed to scan row", "error", err)
			return nil, err
		}
		if actionName.Valid {
			action.ActionName = actionName.String
		}
		if err := common.UnmarshalJson(params, &action.Params); err != nil {
			return nil, err
		}
		actionTemplate = append(actionTemplate, action)
	}
	if err := rows.Err(); err != nil {
		context.GetLogger().Error("eventrule: error iterating action template rows", "error", err)
		return nil, err
	}

	// Enrich proxy actions with datasource dropdown options (tenant-wide)
	if tenantID := context.GetSecurityContext().GetTenantId(); tenantID != "" {
		enrichProxyActionDatasources(context, tenantID, actionTemplate)
		enrichCloudCliAccountDropdown(context, tenantID, actionTemplate)
	}

	return actionTemplate, nil
}

// enrichProxyActionDatasources injects possible_values into datasource_id params
// for proxy actions so the frontend renders a dropdown instead of a text field.
// Queries tenant-wide so integrations from any account are available.
func enrichProxyActionDatasources(context *security.RequestContext, tenantID string, templates []AlertActionTemplate) {
	// Collect all unique integration types needed across proxy actions present in templates
	typesNeeded := make(map[string]bool)
	for _, tmpl := range templates {
		if types, ok := proxyActionIntegrationTypes[tmpl.ActionName]; ok {
			for _, t := range types {
				typesNeeded[t] = true
			}
		}
	}
	if len(typesNeeded) == 0 {
		return
	}

	allTypes := make([]string, 0, len(typesNeeded))
	for t := range typesNeeded {
		allTypes = append(allTypes, t)
	}

	integrations, err := listIntegrationsForDropdown(tenantID, allTypes)
	if err != nil {
		context.GetLogger().Warn("failed to list integrations for proxy action dropdown", "error", err)
		return
	}
	if len(integrations) == 0 {
		return
	}

	// Build per-action-type filtered lists
	integrationsByType := make(map[string][]map[string]any)
	for _, item := range integrations {
		intType, _ := item["type"].(string)
		for actionName, types := range proxyActionIntegrationTypes {
			for _, t := range types {
				if t == intType {
					integrationsByType[actionName] = append(integrationsByType[actionName], map[string]any{
						"label": item["label"],
						"value": item["value"],
					})
				}
			}
		}
	}

	// Inject possible_values into datasource_id params
	for i, tmpl := range templates {
		options, ok := integrationsByType[tmpl.ActionName]
		if !ok {
			continue
		}
		if dsParam, ok := tmpl.Params["datasource_id"].(map[string]any); ok {
			dsParam["possible_values"] = options
			templates[i].Params["datasource_id"] = dsParam
		}
	}
}

// enrichCloudCliAccountDropdown injects possible_values into account_id param
// for the cloud_cli action so the frontend renders a dropdown of cloud accounts.
func enrichCloudCliAccountDropdown(context *security.RequestContext, tenantID string, templates []AlertActionTemplate) {
	// Find the cloud_cli template
	idx := -1
	for i, tmpl := range templates {
		if tmpl.ActionName == "cloud_cli" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}

	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		context.GetLogger().Warn("failed to get db for cloud account dropdown", "error", err)
		return
	}

	rows, err := dbms.Db.Queryx(
		"SELECT id, account_name, cloud_provider FROM cloud_accounts WHERE tenant = $1 AND status = 'active' ORDER BY account_name",
		tenantID,
	)
	if err != nil {
		context.GetLogger().Warn("failed to list cloud accounts for dropdown", "error", err)
		return
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.Error("failed to close rows", "error", err)
		}
	}()

	var options []map[string]any
	for rows.Next() {
		var id, name, provider string
		if err := rows.Scan(&id, &name, &provider); err != nil {
			continue
		}
		options = append(options, map[string]any{
			"label": fmt.Sprintf("%s (%s)", name, provider),
			"value": id,
		})
	}
	if err := rows.Err(); err != nil {
		context.GetLogger().Warn("eventrule: error iterating cloud account dropdown rows", "error", err)
	}

	if len(options) == 0 {
		return
	}

	if acctParam, ok := templates[idx].Params["account_id"].(map[string]any); ok {
		acctParam["possible_values"] = options
		templates[idx].Params["account_id"] = acctParam
	}
}

func extractQueryTypeFromLabels(labels []string) string {
	for _, label := range labels {
		for key, value := range PrometheusLabelCategoryMapping {
			if label == value {
				return key
			}
		}
	}
	return "All"
}

// listIntegrationsForDropdown queries enabled integrations of the given types
// for a tenant, returning [{label, value, type}] for dropdown rendering.
// Queries tenant-wide so integrations from any account in the tenant are available
// (e.g. a DB integration on a GKE account can be used for a GCP cloud alert).
func listIntegrationsForDropdown(tenantID string, types []string) ([]map[string]any, error) {
	if len(types) == 0 {
		return nil, nil
	}

	dbms, err := database.GetDatabaseManager(database.Metastore)
	if err != nil {
		return nil, err
	}

	queryArgs := []any{tenantID}
	placeholders := make([]string, len(types))
	for i, t := range types {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		queryArgs = append(queryArgs, t)
	}

	rows, err := dbms.Db.Queryx(fmt.Sprintf(`
		SELECT DISTINCT i.id::text, i.type::text, i.name::text,
			COALESCE(ca.account_name, '') AS account_name
		FROM integrations i
		LEFT JOIN integrations_cloud_accounts ica ON i.id = ica.integration_id
		LEFT JOIN cloud_accounts ca ON ica.cloud_account_id = ca.id
		WHERE i.tenant_id = $1
		AND i.status = 'enabled'
		AND i.type IN (%s)
	`, strings.Join(placeholders, ", ")), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			slog.Error("failed to close integration rows", "error", cerr)
		}
	}()

	var items []map[string]any
	for rows.Next() {
		var id, intType, name, accountName string
		if err := rows.Scan(&id, &intType, &name, &accountName); err != nil {
			continue
		}
		label := fmt.Sprintf("%s (%s)", name, intType)
		if accountName != "" {
			label = fmt.Sprintf("%s (%s - %s)", name, intType, accountName)
		}
		items = append(items, map[string]any{
			"label": label,
			"value": id,
			"type":  intType,
		})
	}
	if err := rows.Err(); err != nil {
		slog.Error("eventrule: error iterating integration dropdown rows", "error", err)
		return nil, err
	}
	return items, nil
}
