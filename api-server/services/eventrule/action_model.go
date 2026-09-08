package eventrule

type AlertActionTemplate struct {
	Name        string         `json:"name"`
	Params      map[string]any `json:"params"`
	DisplayName string         `json:"display_name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	ActionName  string         `json:"action_name"`
	Source      *string        `json:"source"`
	AlertType   *string        `json:"alert_type"`
}

type ListActionsRequest struct {
	CloudAccountId string `json:"cloud_account_id"`
	Query          string `json:"query"`
	Source         string `json:"source"`
	AlertType      string `json:"alert_type"`
}

var PrometheusLabelCategoryMapping = map[string]string{
	"Deployment":  "deployment",
	"DaemonSet":   "daemonset",
	"StatefulSet": "statefulset",
	"ReplicaSet":  "replicaset",
	"Job":         "job_name",
	"Pod":         "pod",
	"HPA":         "horizontalpodautoscaler",
	"PVC":         "persistentvolumeclaim",
	"Node":        "node",
}
