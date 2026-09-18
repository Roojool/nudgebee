import { useEffect, useRef } from 'react';
import { useRouter } from 'next/router';

// Bridges the admin Overview quick-links to the modals/inner-tabs they target.
// A quick-link navigates to a tab (hash) with `?action=<slug>`; the destination
// body calls this hook to fire `onAction(slug)` once when its slug arrives, then
// the param is stripped so a refresh or re-render doesn't reopen the modal.
//
// `match` is a single slug or an array of slugs one body handles (e.g. all the
// `alert-*` variants the Notification Rules body opens with a preset type).
//
// There is no other convention for opening a modal from a URL in this app — the
// only precedent is integration.jsx reading `?integration=` to select a category
// tab. This keeps to the same query-param spirit, one hook shared by every body.
export default function useAdminAction(match, onAction) {
  const router = useRouter();
  // Guards against the effect firing twice for the same param value; reset to
  // null once the param clears so the SAME action can be triggered again later
  // (open invite modal, close it, click the link again).
  const handledValue = useRef(null);

  useEffect(() => {
    const action = router.query.action;
    if (!action || typeof action !== 'string') {
      handledValue.current = null;
      return;
    }
    const matched = Array.isArray(match) ? match.includes(action) : action === match;
    if (!matched || handledValue.current === action) return;
    handledValue.current = action;
    onAction(action);
    // Strip ?action= while keeping the tab hash and any sibling query params
    // (e.g. ?integration=). asPath always parses; the try guards defensively.
    try {
      const url = new URL(router.asPath, 'http://local');
      url.searchParams.delete('action');
      router.replace(`${url.pathname}${url.search}${url.hash}`, undefined, { shallow: true }).catch(() => {});
    } catch {
      /* ignore — asPath is always a valid relative URL */
    }
    // onAction intentionally omitted: it may be a fresh closure each render, and
    // handledValue already de-dupes. Firing keys on the param value alone.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [router.query.action]);
}
