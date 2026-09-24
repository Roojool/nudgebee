import { isMutatingCommand, mutatingCommandWarning } from '../commandRisk';

// The gate these back is the only thing between a Run click and a live cluster,
// so the cases below are written against the server's allow-lists verbatim
// (api-server/services/k8s_upgrade/service.go) rather than against a summary.
describe('isMutatingCommand', () => {
  describe('kubectl verbs the server permits', () => {
    it.each([
      ['kubectl drain node-1 --ignore-daemonsets', true],
      ['kubectl cordon node-1', true],
      ['kubectl uncordon node-1', true],
      ['kubectl scale deployment/api --replicas=0', true],
      ['kubectl patch deployment api -p {"spec":{"replicas":0}}', true],
      ['kubectl rollout undo deployment/api', true],
      ['kubectl get pods -n kube-system', false],
      ['kubectl describe node node-1', false],
      ['kubectl logs pod/api-0', false],
      ['kubectl top nodes', false],
      ['kubectl version', false],
      ['kubectl cluster-info', false],
    ])('%s → mutating: %s', (command, expected) => {
      expect(isMutatingCommand(command)).toBe(expected);
    });
  });

  describe('aws eks subcommands the server permits', () => {
    it.each([
      ['aws eks update-nodegroup-config --cluster-name prod', true],
      ['aws eks describe-cluster --name prod', false],
      ['aws eks list-nodegroups --cluster-name prod', false],
      ['aws eks describe-update --name prod', false],
      ['aws eks get-token --cluster-name prod', false],
    ])('%s → mutating: %s', (command, expected) => {
      expect(isMutatingCommand(command)).toBe(expected);
    });
  });

  it('tolerates the whitespace a markdown code block introduces', () => {
    expect(isMutatingCommand('  kubectl   drain   node-1  ')).toBe(true);
    expect(isMutatingCommand('\nkubectl get pods\n')).toBe(false);
  });

  it('treats anything it cannot classify as mutating', () => {
    expect(isMutatingCommand('helm rollback api')).toBe(true);
    expect(isMutatingCommand('')).toBe(true);
    expect(isMutatingCommand('   ')).toBe(true);
  });

  // The server rejects these before running anything, so prompting about them
  // would be a dialog for a command that cannot execute.
  it('does not prompt for malformed kubectl/aws commands the server will reject', () => {
    expect(isMutatingCommand('kubectl')).toBe(false);
    expect(isMutatingCommand('aws eks')).toBe(false);
  });

  // The fail-closed property. If the server's allow-list grows a mutating verb
  // and nobody updates this module, the new verb must still prompt.
  it('classifies an unknown kubectl subcommand as mutating', () => {
    expect(isMutatingCommand('kubectl delete pod api-0')).toBe(true);
    expect(isMutatingCommand('kubectl apply -f manifest.yaml')).toBe(true);
    expect(isMutatingCommand('kubectl some-future-verb')).toBe(true);
  });

  it('classifies an unknown aws service or subcommand as mutating', () => {
    expect(isMutatingCommand('aws eks delete-nodegroup --cluster-name prod')).toBe(true);
    expect(isMutatingCommand('aws ec2 terminate-instances --instance-ids i-1')).toBe(true);
  });
});

describe('mutatingCommandWarning', () => {
  it('names the specific consequence for the destructive verbs', () => {
    expect(mutatingCommandWarning('kubectl drain node-1')).toMatch(/evicts every pod/i);
    expect(mutatingCommandWarning('kubectl scale deployment/api --replicas=0')).toMatch(/replica count/i);
    expect(mutatingCommandWarning('aws eks update-nodegroup-config --cluster-name prod')).toMatch(/node group/i);
  });

  it('falls back to a generic warning for anything else', () => {
    expect(mutatingCommandWarning('helm rollback api')).toMatch(/changes the state of your cluster/i);
  });
});
