// Mirrors carriesGatewayTraffic in api-server/services/user/token_policy.go and the kind
// filter in llm/gateway/auth/user_auths.go — one place, so the UI and server cannot drift.
export const carriesGatewayTraffic = (kind) => kind === 'gateway' || kind === 'all';

export const KIND_LABELS = { platform: 'platform', gateway: 'AI Gateway', all: 'all-access' };
