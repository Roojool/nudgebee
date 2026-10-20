import { isPathAncestor, nameListPositions, shiftsListPosition } from '@components/k8s/investigate/changeRollback';
import { markSupersededChanges } from '@components/k8s/investigate/cards/LastDeploymentCard';

const ENV2 = 'spec.template.spec.containers[0].env[2]';
const rows = (list) => list.map(([eventId, timestamp, changedFields]) => ({ eventId, timestamp, changedFields }));
const supersededAfter = (laterFields) =>
  markSupersededChanges(
    rows([
      ['later', 't2', laterFields],
      ['chosen', 't1', [{ path: ENV2, old: null, new: '{"name":"LLM_SERVER_URL"}' }]],
    ])
  )[1].supersededBy;

// Mirrors the server's laterChangeConflict cases (event/revert_request_test.go) so the page never
// offers what the server refuses, nor hides what it would allow.
describe('markSupersededChanges — list positions', () => {
  it.each([
    ['a child of the path changed', [{ path: `${ENV2}.value`, old: 'a', new: 'b' }], 't2'],
    ['a parent of the path changed', [{ path: 'spec.template.spec.containers[0].env', old: '[]', new: '[1]' }], 't2'],
    ['an item removed before it', [{ path: 'spec.template.spec.containers[0].env[1]', old: '{"name":"A"}', new: null }], 't2'],
    ['an item added at its position', [{ path: ENV2, old: null, new: '{"name":"B"}' }], 't2'],
    ['a container added before its container', [{ path: 'spec.template.spec.containers[0]', old: null, new: '{"name":"sidecar"}' }], 't2'],
    ['an item added after it', [{ path: 'spec.template.spec.containers[0].env[3]', old: null, new: '{"name":"B"}' }], undefined],
    ['an earlier item edited in place', [{ path: 'spec.template.spec.containers[0].env[1].value', old: 'a', new: 'b' }], undefined],
    ['another container’s list changed', [{ path: 'spec.template.spec.containers[1].env[0]', old: '{"name":"A"}', new: null }], undefined],
  ])('%s', (_, laterFields, expected) => {
    expect(supersededAfter(laterFields)).toBe(expected);
  });
});

describe('shiftsListPosition / isPathAncestor', () => {
  it('only an item at or before the position, in the same list, shifts it', () => {
    expect(shiftsListPosition('a.env[1]', 'a.env[2].value')).toBe(true);
    expect(shiftsListPosition('a.env[3]', 'a.env[2]')).toBe(false);
    expect(shiftsListPosition('a.env', 'a.env[2]')).toBe(false);
    expect(shiftsListPosition('b.env[0]', 'a.env[2]')).toBe(false);
  });

  it('treats both field and list nesting as containment', () => {
    expect(isPathAncestor('a.env', 'a.env[2]')).toBe(true);
    expect(isPathAncestor('a.env[2]', 'a.env[2].value')).toBe(true);
    expect(isPathAncestor('a.env[2]', 'a.env[20]')).toBe(false);
  });
});

// With several containers, "containers[1]" says nothing about which one a rollback touches.
describe('nameListPositions', () => {
  const after = {
    spec: {
      template: {
        spec: {
          containers: [
            { name: 'app', env: [{ name: 'A' }, { name: 'B' }] },
            { name: 'sidecar', env: [] },
          ],
          init_containers: [{ name: 'migrate' }],
        },
      },
    },
  };
  const before = { spec: { template: { spec: { containers: [{ name: 'app', env: [{ name: 'A' }, { name: 'B' }, { name: 'GONE' }] }] } } } };

  it('names containers and env entries from the manifest, reading snake_case keys', () => {
    expect(nameListPositions('spec.template.spec.containers[1].image', [after])).toBe('spec.template.spec.containers[sidecar].image');
    expect(nameListPositions('spec.template.spec.containers[0].env[1].value', [after])).toBe('spec.template.spec.containers[app].env[B].value');
    expect(nameListPositions('spec.template.spec.initContainers[0].image', [after])).toBe('spec.template.spec.initContainers[migrate].image');
  });

  it('falls back to the manifest before the change, then to the recorded item', () => {
    expect(nameListPositions('spec.template.spec.containers[0].env[2]', [after, before])).toBe('spec.template.spec.containers[app].env[GONE]');
    expect(nameListPositions('spec.template.spec.containers[0].env[5]', [after], { name: 'NEW' })).toBe(
      'spec.template.spec.containers[app].env[NEW]'
    );
  });

  it('keeps the position when nothing names the item', () => {
    expect(nameListPositions('spec.template.spec.containers[0].args[6]', [after])).toBe('spec.template.spec.containers[app].args[6]');
    expect(nameListPositions('spec.template.spec.containers[3].image', [])).toBe('spec.template.spec.containers[3].image');
  });
});
