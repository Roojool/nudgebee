import type { NextApiRequest, NextApiResponse } from 'next';
import { authenticateRequest } from '@lib/rpcGateway';
import { loadRpcRoutes } from '@lib/rpcRoutes';
import { buildCatalog, isTokenScopeBlockedAction, type CatalogEntry } from '@lib/permissionCatalog';

// The scopes an API key can be limited to — the same module × {Read,Write,Execute}
// catalog the Roles editor renders (/api/permissions/catalog), minus the actions a
// scoped key is never allowed to reach (isTokenScopeBlockedAction), so the picker
// only offers boxes the gateway would honour. Any authenticated user may read it:
// every user can create their own keys, and the response is derived from
// actions.yaml with no tenant data in it.

let cache: CatalogEntry[] | null = null;

function getCatalog(): CatalogEntry[] {
  if (cache) return cache;
  const routes = loadRpcRoutes();
  const names = Object.keys(routes).filter((name) => !isTokenScopeBlockedAction(name));
  cache = buildCatalog(names.map((name) => ({ name, comment: routes[name].comment })));
  return cache;
}

export default async function handler(req: NextApiRequest, res: NextApiResponse) {
  if (req.method !== 'GET') {
    res.setHeader('Allow', 'GET');
    return res.status(405).json({ error: 'method_not_allowed' });
  }
  const auth = await authenticateRequest(req);
  if (!auth) {
    return res.status(401).json({ error: 'unauthenticated' });
  }
  return res.status(200).json({ catalog: getCatalog() });
}
