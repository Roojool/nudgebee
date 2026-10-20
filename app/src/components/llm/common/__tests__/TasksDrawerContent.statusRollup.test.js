import { buildDrawerTasks } from '@hooks/useLLMInvestigationControl';
import { buildTaskTree, withRolledUpStatus } from '../TasksDrawerContent';

// #36633 session as persisted: aws_orchestrator -> metrics -> prometheus (nested via the
// forwarded answer); prometheus's queries failed, its promql_query sub-agent succeeded.
const ROOT = '00000000-0000-0000-0000-000000000000';
const answer = 'I am unable to retrieve the CPU utilization for your EC2 instances because the metrics agent is currently not connected.';
const agents = [
  {
    id: 'orch',
    agent_name: 'aws_orchestrator',
    parent_agent_id: ROOT,
    status: 'success',
    response: 'i-0169491a98464892f: 22.24%',
    created_at: '2026-08-20T07:29:42',
    llm_conversation_tool_calls: [
      { id: 'tc-metrics', tool_name: 'metrics', status: 'success', child_agent_id: '', created_at: '2026-08-20T07:29:54' },
      { id: 'tc-shell-1', tool_name: 'shell_execute', status: 'error', created_at: '2026-08-20T07:31:11' },
      { id: 'tc-shell-2', tool_name: 'shell_execute', status: 'success', created_at: '2026-08-20T07:31:18' },
    ],
  },
  { id: 'metrics', agent_name: 'metrics', parent_agent_id: 'orch', status: 'success', response: answer, created_at: '2026-08-20T07:29:54.69' },
  {
    id: 'prom',
    agent_name: 'prometheus',
    parent_agent_id: 'orch',
    status: 'success',
    response: answer,
    created_at: '2026-08-20T07:29:54.70',
    llm_conversation_tool_calls: [
      { id: 'tc-list', tool_name: 'metrics_list', status: 'error', created_at: '2026-08-20T07:30:00' },
      { id: 'tc-promql', tool_name: 'promql_query', status: 'success', child_agent_id: 'promql', created_at: '2026-08-20T07:30:35' },
      { id: 'tc-exec', tool_name: 'prometheus_execute', status: 'error', created_at: '2026-08-20T07:30:49' },
    ],
  },
  {
    id: 'promql',
    agent_name: 'promql_query',
    parent_agent_id: 'prom',
    status: 'success',
    response: '{"promql":"up"}',
    created_at: '2026-08-20T07:30:35.29',
  },
];

const statusById = (tasks) => Object.fromEntries(buildTaskTree(tasks).tasks.map((t) => [t.id, t.response_status]));

describe('Tasks drawer status roll-up (#36633)', () => {
  it('marks a successful parent whose children failed as completed with errors', () => {
    const status = statusById(buildDrawerTasks(agents, {}));

    expect(status.metrics).toBe('completed_with_errors');
    expect(status.prom).toBe('completed_with_errors');
    expect(status.orch).toBe('completed_with_errors');
    // Leaves keep their own status — the #36213 layer is untouched.
    expect(status.promql).toBe('success');
    expect(status['tc-list']).toBe('error');
    expect(status['tc-exec']).toBe('error');
    expect(status['tc-shell-2']).toBe('success');
  });

  it('leaves clean, failed and in-progress parents alone', () => {
    const status = Object.fromEntries(
      withRolledUpStatus([
        { id: 'clean', response_status: 'success' },
        { id: 'clean-child', parentId: 'clean', response_status: 'success' },
        { id: 'running', response_status: 'in_progress' },
        { id: 'running-child', parentId: 'running', response_status: 'error' },
        { id: 'failed', response_status: 'fail' },
        { id: 'failed-child', parentId: 'failed', response_status: 'error' },
      ]).map((t) => [t.id, t.response_status])
    );

    expect(status).toMatchObject({ clean: 'success', running: 'in_progress', failed: 'fail' });
  });

  it('survives a cyclic parent link', () => {
    const tasks = withRolledUpStatus([
      { id: 'a', parentId: 'b', response_status: 'success' },
      { id: 'b', parentId: 'a', response_status: 'success' },
    ]);
    expect(tasks.map((t) => t.response_status)).toEqual(['success', 'success']);
  });

  it('rolls up through a cyclic parent link when one member has a failed child', () => {
    const tasks = withRolledUpStatus([
      // `b` first: a memoised DFS from `b` caches `a` as clean before reaching `d`.
      { id: 'b', parentId: 'a', response_status: 'success' },
      { id: 'a', parentId: 'b', response_status: 'success' },
      { id: 'c', parentId: 'b', response_status: 'success' },
      { id: 'd', parentId: 'b', response_status: 'error' },
    ]);
    expect(tasks.map((t) => t.response_status)).toEqual(['completed_with_errors', 'completed_with_errors', 'success', 'error']);
  });
});
