// Opening a chat from another account (the Ask page's all-accounts list) changes the
// page account and the session in one navigation. The account-switch effect must
// then keep that session instead of resetting to a new chat, as it does when the
// user changes the account themselves.

export interface PendingCrossAccountOpen {
  accountId: string;
  sessionId: string;
}

// True only when this account change is the one the list asked for, for the
// session the page is now showing. Anything else is a manual switch.
export const isPendingCrossAccountOpen = (
  pending: PendingCrossAccountOpen | null | undefined,
  accountId: string | undefined,
  selectedSessionId: string | undefined
): boolean => Boolean(pending && accountId && pending.accountId === accountId && pending.sessionId === selectedSessionId);
