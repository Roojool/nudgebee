// When the KB menu's Retrigger item is disabled. Extracted from
// KnowledgeBaseTab so it can be unit-tested without rendering the component.

export interface KBRetriggerTarget {
  kb_type?: string | null;
  status?: string | null;
  enabled?: boolean | null;
}

// Retrigger re-indexes a KB: an integration KB re-syncs from its source, a
// user-created KB re-embeds its stored content. Both are refused while a load
// is running, for an archived KB (its integration is disabled — nothing to
// re-sync), for a switched-off KB (the backend won't re-index content nothing
// can read), and without write access (the backend requires it).
export const isKbRetriggerDisabled = (knowledgeBase: KBRetriggerTarget, hasAccess: boolean): boolean =>
  !hasAccess || knowledgeBase.status === 'processing' || knowledgeBase.status === 'archived' || knowledgeBase.enabled === false;
