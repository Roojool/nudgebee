package cloud

type CloudExecuteCliCommandRequest struct {
	AccountID string `json:"account_id" validate:"required"`
	TenantID  string `json:"tenant_id" validate:"required"`
	UserID    string `json:"user_id" validate:"required"`
	Command   string `json:"command" validate:"required"`
}

type CliExecutionResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	OutputTruncated bool   `json:"output_truncated"`
}
