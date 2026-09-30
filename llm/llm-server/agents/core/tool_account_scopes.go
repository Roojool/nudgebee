package core

import toolcore "nudgebee/llm/tools/core"

// Account scope of every tool registered in this package (see
// toolcore.AccountScope). A new tool must be declared here or the build's
// TestEveryToolDeclaresAnAccountScope fails.
func init() {
	toolcore.DeclareAccountScope(toolcore.AccountScopeIndependent,
		"llm",
	)
	toolcore.DeclareOriginBound(toolcore.OriginBoundConversationState,
		"memory",
	)
	toolcore.DeclareOriginBound(toolcore.OriginBoundNotYetSupported,
		"clarification",
	)
}
