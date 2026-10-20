import { useEffect, useRef, useState } from 'react';
import apiKnowledgeBase from '@api1/knowledge-base';
import apiAskNudgebee from '@api1/ask-nudgebee';
import apiKubernetes from '@api1/kubernetes';
import { toast as snackbar } from '@ui/Toast';

// Context-tag options are built from the account's real resources: Service = k8s
// workloads, Namespace = k8s namespaces. Same shape KnowledgeBaseTab.jsx's own
// copy builds — kept separate rather than imported from there, since it is a
// small, stable, pure mapping and importing it would make this hook depend on
// the much larger tab component for four lines of logic.
const buildContextTagOptions = ({ services = [], namespaces = [] }) => [
  ...services.map((value) => ({ value: `service: ${value}`, label: value, group: 'Service' })),
  ...namespaces.map((value) => ({ value: `namespace: ${value}`, label: value, group: 'Namespace' })),
];

/**
 * Create/edit state and handlers for `KnowledgeBaseFormModal`, for a caller
 * that does not already own an account-scoped Knowledge Base surface the way
 * `KnowledgeBaseTab.jsx` does. Mirrors that component's own handleCreate/
 * handleEdit/handleFormSubmit exactly — same API calls, same agent-mapping
 * sync — so a second mount point (SetupCenter's Skills item) isn't stuck
 * re-deriving the same request shape by hand.
 *
 * The account is per-call, not fixed at construction (`openCreate(accountId)`,
 * `openEdit(accountId, kb)`): a caller like SetupCenter may not know which
 * account to scope to until the moment the user acts (picking one from
 * several, or reading it off the row being edited), and passing it as a call
 * argument avoids relying on a React state update having already landed by
 * the time the function that needs it runs.
 */
export default function useKnowledgeBaseForm({ onSaved } = {}) {
  const [activeAccountId, setActiveAccountId] = useState(null);
  const [open, setOpen] = useState(false);
  const [editKnowledgeBase, setEditKnowledgeBase] = useState(null);
  const [formAgentIds, setFormAgentIds] = useState([]);
  const [submitting, setSubmitting] = useState(false);
  const [agents, setAgents] = useState([]);
  const [agentsLoading, setAgentsLoading] = useState(false);
  const [tools, setTools] = useState([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [contextTagOptions, setContextTagOptions] = useState([]);
  const [contextTagsLoading, setContextTagsLoading] = useState(false);

  // Guards `openEdit`'s two awaited fetches against being superseded before
  // they resolve — a second chip click, or a switch to `openCreate` — so
  // only the most-recently-started call is ever allowed to write the form
  // state. Same request-sequence discipline as SetupCenter.tsx's own
  // aiIntegrationsRequestId/knowledgeBasesRequestId.
  const openEditRequestId = useRef(0);

  // Agents and tools back the `@`/`#` suggestions in the content box.
  useEffect(() => {
    if (!activeAccountId) return undefined;
    let cancelled = false;
    setAgentsLoading(true);
    apiAskNudgebee
      .listAgents({ accountId: activeAccountId })
      .then((response) => {
        if (cancelled) return;
        const list = response?.data?.data?.ai_list_agents?.data || [];
        setAgents([...list].sort((a, b) => (a.name || '').localeCompare(b.name || '')));
      })
      .catch((err) => {
        if (cancelled) return;
        console.error('Error fetching agents:', err);
        setAgents([]);
      })
      .finally(() => {
        if (!cancelled) setAgentsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeAccountId]);

  useEffect(() => {
    if (!activeAccountId) return undefined;
    let cancelled = false;
    setToolsLoading(true);
    apiAskNudgebee
      .listTools({ accountId: activeAccountId })
      .then((response) => {
        if (cancelled) return;
        const list = response?.data?.data?.ai_list_tools?.data;
        setTools((Array.isArray(list) ? [...list] : []).sort((a, b) => (a.name || '').localeCompare(b.name || '')));
      })
      .catch((err) => {
        if (cancelled) return;
        console.error('Error fetching tools:', err);
        setTools([]);
      })
      .finally(() => {
        if (!cancelled) setToolsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeAccountId]);

  useEffect(() => {
    if (!activeAccountId) return undefined;
    let cancelled = false;
    setContextTagsLoading(true);
    Promise.all([apiKubernetes.getK8sWorkloadNames({ accountId: activeAccountId }), apiKubernetes.getK8sNamespaceNames(activeAccountId)])
      .then(([workloadsRes, namespacesRes]) => {
        if (cancelled) return;
        const services = [...new Set(workloadsRes?.data?.workloadNames || [])].sort((a, b) => a.localeCompare(b));
        const namespaces = [...new Set(namespacesRes?.data?.namespaces || [])].sort((a, b) => a.localeCompare(b));
        setContextTagOptions(buildContextTagOptions({ services, namespaces }));
      })
      .catch((err) => {
        if (cancelled) return;
        console.error('Error fetching context-tag options:', err);
        setContextTagOptions([]);
      })
      .finally(() => {
        if (!cancelled) setContextTagsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeAccountId]);

  const openCreate = (accountId) => {
    openEditRequestId.current += 1;
    setActiveAccountId(accountId);
    setEditKnowledgeBase(null);
    setFormAgentIds([]);
    setOpen(true);
  };

  const openEdit = async (accountId, knowledgeBase) => {
    const requestId = (openEditRequestId.current += 1);
    setActiveAccountId(accountId);
    try {
      setSubmitting(true);
      // Fetch full knowledge base data (including content) before editing
      const response = await apiKnowledgeBase.getKnowledgeBase(accountId, knowledgeBase.id);
      if (requestId !== openEditRequestId.current) return;
      if (response.errors && response.errors.length > 0) {
        snackbar.error('Failed to fetch knowledge base details');
        return;
      }
      if (!response.data) {
        snackbar.error('Failed to fetch knowledge base details');
        return;
      }
      // Mapped agents are fetched before the modal opens so the picker renders
      // its real state on first paint. A failure here is not fatal: the KB is
      // still editable, the picker just opens with nothing ticked, so warn
      // rather than silently implying it is mapped to nobody.
      const agentsResponse = await apiKnowledgeBase.getKBAgents(accountId, knowledgeBase.id);
      if (requestId !== openEditRequestId.current) return;
      if (agentsResponse.errors && agentsResponse.errors.length > 0) {
        snackbar.error('Failed to fetch the agents this knowledge base is mapped to');
      }
      setFormAgentIds(agentsResponse.data || []);
      setEditKnowledgeBase(response.data);
      setOpen(true);
    } catch (err) {
      if (requestId !== openEditRequestId.current) return;
      console.error('Error fetching knowledge base:', err);
      snackbar.error('An error occurred while fetching knowledge base details');
    } finally {
      if (requestId === openEditRequestId.current) setSubmitting(false);
    }
  };

  const close = () => {
    setOpen(false);
    setEditKnowledgeBase(null);
    setFormAgentIds([]);
  };

  // Applies the picker's selection to llm_kb_agent_mappings. The KB itself is
  // already saved by the time this runs, so a mapping failure is reported but
  // never rolls the save back — it is a separate RPC per mapping, and the
  // wildcard ("all agents") is just another agent id to the backend.
  const syncAgentMappings = async (accountId, kbId, nextAgentIds, previousAgentIds) => {
    const toMap = nextAgentIds.filter((id) => !previousAgentIds.includes(id));
    const toUnmap = previousAgentIds.filter((id) => !nextAgentIds.includes(id));
    if (toMap.length === 0 && toUnmap.length === 0) {
      return true;
    }

    const results = await Promise.all([
      ...toMap.map((agentId) => apiKnowledgeBase.mapKnowledgeBaseToAgent(accountId, kbId, agentId)),
      ...toUnmap.map((agentId) => apiKnowledgeBase.unmapKnowledgeBaseFromAgent(accountId, kbId, agentId)),
    ]);
    const failed = results.filter((r) => r?.errors?.length > 0).length;
    if (failed > 0) {
      snackbar.error(`Failed to update agent mapping for ${failed} agent(s)`);
      return false;
    }
    return true;
  };

  const handleSubmit = async (data) => {
    const { agentIds = [], ...kbPayload } = data;
    const accountId = activeAccountId;
    try {
      setSubmitting(true);
      let response;
      let kbId;

      if (editKnowledgeBase) {
        response = await apiKnowledgeBase.updateKnowledgeBase(accountId, editKnowledgeBase.id, kbPayload);
        if (response.errors && response.errors.length > 0) {
          snackbar.error(response.errors[0]?.message || 'Failed to update knowledge base');
          return;
        }
        kbId = editKnowledgeBase.id;
        snackbar.success('Knowledge base updated successfully');
      } else {
        response = await apiKnowledgeBase.createKnowledgeBase(accountId, kbPayload);
        if (response.errors && response.errors.length > 0) {
          snackbar.error(response.errors[0]?.message || 'Failed to create knowledge base');
          return;
        }
        kbId = response.data?.id;
        snackbar.success('Knowledge base created successfully');
      }

      if (kbId) {
        await syncAgentMappings(accountId, kbId, agentIds, formAgentIds);
      }

      close();
      onSaved?.();
    } catch (err) {
      console.error('Error submitting knowledge base:', err);
      snackbar.error('An error occurred while saving the knowledge base');
    } finally {
      setSubmitting(false);
    }
  };

  return {
    open,
    editKnowledgeBase,
    submitting,
    agents,
    agentsLoading,
    tools,
    toolsLoading,
    contextTagOptions,
    contextTagsLoading,
    formAgentIds,
    openCreate,
    openEdit,
    close,
    handleSubmit,
  };
}
