package agents

import (
	"fmt"
	"nudgebee/llm/agents/aws"
	"nudgebee/llm/common"
	"strings"
	"sync"
)

var accountDebugAgentMap = map[string]string{}
var accountDebugAgentMapMutex sync.RWMutex

func GetDebugAgentName(accountId string) string {
	accountDebugAgentMapMutex.RLock()
	plannerAgent := accountDebugAgentMap[accountId]
	accountDebugAgentMapMutex.RUnlock()

	defaultAgentName := AgentK8sOrchestratorName

	if plannerAgent == "" {
		plannerAgent = defaultAgentName
		// use DB to get account type
		dbms, err := common.GetDatabaseManager(common.Metastore)
		if err != nil {
			return defaultAgentName
		}
		rows, err := dbms.Query("select cloud_provider, coalesce(account_type, '') from cloud_accounts where id = $1", accountId)
		if err != nil {
			return defaultAgentName
		}
		defer func() {
			if err := rows.Close(); err != nil {
				// Log the error, but don't return it as it's a defer call
				fmt.Printf("Error closing rows: %v\n", err)
			}
		}()
		for rows.Next() {
			var cloudProvider, accountType string
			err = rows.Scan(&cloudProvider, &accountType)
			if err != nil {
				return defaultAgentName
			}
			planner := debugAgentForAccount(cloudProvider, accountType)
			accountDebugAgentMapMutex.Lock()
			accountDebugAgentMap[accountId] = planner
			accountDebugAgentMapMutex.Unlock()
			plannerAgent = planner
		}
	}
	if plannerAgent == "" {
		plannerAgent = defaultAgentName
	}
	return plannerAgent
}

func debugAgentForAccount(cloudProvider, accountType string) string {
	if strings.EqualFold(accountType, "vm") {
		return AgentVMOrchestratorName
	}
	switch {
	case strings.EqualFold(cloudProvider, "aws"):
		return aws.AgentAwsOrchestratorName
	case strings.EqualFold(cloudProvider, "gcp"):
		return AgentGcpOrchestratorName
	case strings.EqualFold(cloudProvider, "azure"):
		return AgentAzureOrchestratorName
	default:
		return AgentK8sOrchestratorName
	}
}
