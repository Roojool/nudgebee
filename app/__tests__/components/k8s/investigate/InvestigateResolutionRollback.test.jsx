import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';

jest.mock('@api1/recommendation', () => ({ __esModule: true, default: { applyRecommendation: jest.fn() } }));
jest.mock('@api1/kubernetes', () => ({ __esModule: true, default: { resolveEventRecord: jest.fn() } }));

import apiKubernetes from '@api1/kubernetes';
import InvestigateResolution from '@components/k8s/investigate/InvestigateResolution';

// The manifests the collector records are serialised snake_case — owner references included.
const manifest = ({ labels = {}, annotations = {}, ownerReferences = '' } = {}) => {
  const block = (name, map) =>
    Object.keys(map).length
      ? `  ${name}:\n${Object.entries(map)
          .map(([k, v]) => `    ${k}: ${v}`)
          .join('\n')}\n`
      : '';
  return `metadata:\n${block('annotations', annotations)}${block('labels', labels)}${ownerReferences}  name: notifications\n  namespace: nudgebee\n`;
};

const eventWith = (resourceName, newManifest) => ({
  id: 'alert-1',
  cloud_account_id: 'acc',
  subject_namespace: 'nudgebee',
  starts_at: '2026-09-18T00:14:11Z',
  evidences: [
    {
      type: 'diff',
      start_at: '2026-09-16T17:12:28Z',
      data: {
        resource_name: resourceName,
        old: 'a',
        new: newManifest,
        updated_values: [
          { path: 'spec.template.metadata.annotations.rollme', old: 'FvTKN', new: 'Uf5yp' },
          { path: 'spec.template.spec.containers[0].image', old: 'registry/app:v1', new: 'registry/app:v2' },
        ],
      },
    },
  ],
});

const renderDialog = (row) =>
  render(
    <InvestigateResolution
      row={row}
      handleClose={jest.fn()}
      updateInvestigateSuccessSnackBar={jest.fn()}
      isRevertTheDevelopment
      cardId='LastDeploymentCard_0'
    />
  );

describe('rollback dialog', () => {
  it('names the target, the timing and each field as now vs after rollback', () => {
    renderDialog(eventWith('deployment/nudgebee/notifications.yaml', manifest()));
    expect(screen.getByText('Deployment nudgebee/notifications')).toBeInTheDocument();
    expect(screen.getByText(/31h before the alert/)).toBeInTheDocument();
    expect(screen.getAllByText('Now')).toHaveLength(2);
    expect(screen.getAllByText('After rollback')).toHaveLength(2);
    // Bare values, not JSON-quoted.
    expect(screen.getByText('registry/app:v2')).toBeInTheDocument();
    expect(screen.getByText('registry/app:v1')).toBeInTheDocument();
    expect(screen.getByText(/restart marker/)).toBeInTheDocument();
    expect(screen.getByText(/one by one/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Roll back' })).toBeEnabled();
  });

  it('warns that Helm will re-apply the change and offers helm rollback', () => {
    renderDialog(
      eventWith(
        'deployment/nudgebee/notifications.yaml',
        manifest({ labels: { 'app.kubernetes.io/managed-by': 'Helm' }, annotations: { 'meta.helm.sh/release-name': 'notifications' } })
      )
    );
    expect(screen.getByText(/helm rollback notifications -n nudgebee/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Roll back' })).toBeEnabled();
  });

  // vmagent: the VMAgent operator rewrites the Deployment within seconds, so the rollback would
  // "succeed" and change nothing.
  it('blocks a rollback of an operator-owned workload', () => {
    const owner =
      '  owner_references:\n  - api_version: operator.victoriametrics.com/v1beta1\n    controller: true\n    kind: VMAgent\n    name: victoria\n';
    renderDialog(eventWith('deployment/victoria/vmagent.yaml', manifest({ ownerReferences: owner })));
    expect(screen.getByText(/VMAgent victoria owns this workload/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Roll back' })).toBeDisabled();
  });

  it('names a DaemonSet as one and says it restarts on every node', () => {
    renderDialog(eventWith('daemonset/agent/node-agent.yaml', manifest()));
    expect(screen.getByText('DaemonSet agent/node-agent')).toBeInTheDocument();
    expect(screen.getByText(/every node/)).toBeInTheDocument();
  });

  // Several changes: the operator picks in the dialog, and the preview follows the pick.
  it('lets the operator pick the change', async () => {
    const changeEvent = (image) => ({
      data: {
        events: [
          {
            id: 'x',
            starts_at: '2026-09-16T12:00:00Z',
            evidences: [
              {
                type: 'diff',
                data: {
                  resource_name: 'deployment/nudgebee/notifications.yaml',
                  old: 'a',
                  new: manifest(),
                  updated_values: [{ path: 'spec.template.spec.containers[0].image', old: `${image}-old`, new: image }],
                },
              },
            ],
          },
        ],
      },
    });
    apiKubernetes.resolveEventRecord.mockImplementation((id) => Promise.resolve(changeEvent(id === 'c2' ? 'env-change' : 'image-change')));
    const choices = [
      { changeEventId: 'c3', when: '16 Sep 22:42', timing: '31h before the alert', summary: 'image' },
      { changeEventId: 'c2', when: '16 Sep 17:30', timing: '36h before the alert', summary: 'env' },
    ];
    render(
      <InvestigateResolution
        row={{ id: 'alert-1', cloud_account_id: 'acc', starts_at: '2026-09-18T00:14:11Z', evidences: [] }}
        handleClose={jest.fn()}
        updateInvestigateSuccessSnackBar={jest.fn()}
        isRevertTheDevelopment
        cardId='LastDeploymentCard_0'
        changeEventId='c3'
        changeChoices={choices}
      />
    );
    expect(await screen.findByText('image-change')).toBeInTheDocument();
    const radios = screen.getAllByRole('radio');
    expect(radios).toHaveLength(2);
    fireEvent.click(radios[1]);
    await waitFor(() => expect(apiKubernetes.resolveEventRecord).toHaveBeenLastCalledWith('c2', 'acc'));
    expect(await screen.findByText('env-change')).toBeInTheDocument();
  });
});
