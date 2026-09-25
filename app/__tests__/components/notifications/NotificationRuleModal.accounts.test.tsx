import React from 'react';
import { render, screen, waitFor, fireEvent, act, within } from '@testing-library/react';
import '@testing-library/jest-dom';

jest.mock('@api1/notification', () => ({
  __esModule: true,
  default: {
    getInstalledTools: jest.fn(),
    getNotificationRules: jest.fn(),
    getNotificationRuleEventTypes: jest.fn(),
    insertNotificationRule: jest.fn(),
  },
}));
jest.mock('@api1/home', () => ({ __esModule: true, default: { getCloudAccounts: jest.fn() } }));
jest.mock('@api1/kubernetes', () => ({ __esModule: true, default: { getK8sNamespaces: jest.fn(), getAllK8sWorkload: jest.fn() } }));
jest.mock('@api1/kubernetes1', () => ({ __esModule: true, default: { getEventRules: jest.fn() } }));
jest.mock('@api1/user', () => ({ __esModule: true, default: { listUsers: jest.fn() } }));
jest.mock('@api1/autoPlaybook', () => ({ __esModule: true, default: { listSlackChannels: jest.fn(), listMSTeamsChannels: jest.fn() } }));
jest.mock('@api1/account', () => ({ __esModule: true, default: { getNotificationChannelList: jest.fn() } }));
jest.mock('@api1/integrations', () => ({ __esModule: true, default: { listIntegrations: jest.fn() } }));
jest.mock('@ui/Toast', () => ({ toast: { success: jest.fn(), error: jest.fn() } }));
jest.mock('@shared/icons/SafeIcon', () => ({ __esModule: true, default: () => null }));
jest.mock('@shared/icons/CloudIcon', () => ({ __esModule: true, default: () => null }));
jest.mock('@shared/widgets/CustomDateTimePicker', () => ({ __esModule: true, default: () => null }));
jest.mock('@assets', () => new Proxy({}, { get: () => '/icon.svg' }));

import NotificationRuleModal from '@components/notifications/NotificationRuleModal';

const apiNotifications = require('@api1/notification').default;
const apiDashboard = require('@api1/home').default;
const apiKubernetes = require('@api1/kubernetes').default;
const k8sApi = require('@api1/kubernetes1').default;
const apiUser = require('@api1/user').default;
const apiAutoPlaybook = require('@api1/autoPlaybook').default;
const apiAccount = require('@api1/account').default;
const apiIntegrations = require('@api1/integrations').default;

const ACCOUNTS = [
  { id: 'acc-a', account_name: 'prod-east', cloud_provider: 'K8s' },
  { id: 'acc-b', account_name: 'prod-west', cloud_provider: 'K8s' },
  { id: 'acc-c', account_name: 'staging', cloud_provider: 'K8s' },
];

const renderModal = (rule: any = {}, editingSource = '') =>
  render(
    <NotificationRuleModal
      open
      handleClose={jest.fn()}
      listNotificationRules={jest.fn()}
      notificationRuleObject={rule}
      editingSource={editingSource}
    />
  );

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

const multiAccountRule = {
  id: 'rule-7',
  name: 'Prod alerts',
  source: 'troubleshoot',
  scope_mode: 'accounts',
  account_id: 'acc-a',
  account_ids: '["acc-a","acc-b"]',
  cluster: 'prod-east',
  is_suppressed: true,
  notification_rule_mappings: [],
};

describe('NotificationRuleModal — accounts', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    apiNotifications.getInstalledTools.mockResolvedValue({ messaging_platforms: [] });
    apiNotifications.getNotificationRules.mockResolvedValue({ data: {} });
    apiNotifications.getNotificationRuleEventTypes.mockResolvedValue([]);
    apiNotifications.insertNotificationRule.mockResolvedValue({ data: { data: { notifications_upsert_rule: { id: 'rule-7' } } } });
    apiDashboard.getCloudAccounts.mockResolvedValue(ACCOUNTS);
    apiKubernetes.getK8sNamespaces.mockImplementation((_limit: number, _offset: number, { accountId }: { accountId: string }) =>
      Promise.resolve({
        data: { k8s_namespaces: accountId === 'acc-a' ? [{ name: 'payments' }, { name: 'shared' }] : [{ name: 'shared' }, { name: 'search' }] },
      })
    );
    apiKubernetes.getAllK8sWorkload.mockResolvedValue({ data: [] });
    k8sApi.getEventRules.mockImplementation(({ accountId }: { accountId: string }) =>
      Promise.resolve({
        data: { event_rules: accountId === 'acc-a' ? [{ alert: 'HighLatency' }, { alert: 'PodCrash' }] : [{ alert: 'PodCrash' }, { alert: ' ' }] },
      })
    );
    apiUser.listUsers.mockResolvedValue({ data: [] });
    apiAutoPlaybook.listSlackChannels.mockResolvedValue({ data: [] });
    apiAutoPlaybook.listMSTeamsChannels.mockResolvedValue({ data: [] });
    apiAccount.getNotificationChannelList.mockResolvedValue({ data: { data: [] } });
    apiIntegrations.listIntegrations.mockResolvedValue({ data: { rows: [] } });
  });

  afterEach(flush);

  it('loads every account of a rule and saves them back', async () => {
    renderModal(multiAccountRule, 'troubleshoot');

    await waitFor(() => expect(screen.getByText('prod-east')).toBeInTheDocument());
    expect(screen.getByText('prod-west')).toBeInTheDocument();
    expect(screen.queryByText('staging')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /^update$/i }));

    await waitFor(() => expect(apiNotifications.insertNotificationRule).toHaveBeenCalledTimes(1));
    const payload = apiNotifications.insertNotificationRule.mock.calls[0][0];
    expect(payload.id).toBe('rule-7');
    expect(payload.accountIds).toEqual(['acc-a', 'acc-b']);
  });

  it('offers namespaces and alerts from every selected account', async () => {
    renderModal(multiAccountRule, 'troubleshoot');

    await waitFor(() => expect(apiKubernetes.getK8sNamespaces).toHaveBeenCalledTimes(2));
    expect(apiKubernetes.getK8sNamespaces.mock.calls.map((call: any[]) => call[2].accountId)).toEqual(['acc-a', 'acc-b']);
    await waitFor(() => expect(k8sApi.getEventRules).toHaveBeenCalledTimes(2));
    expect(k8sApi.getEventRules.mock.calls.map((call: any[]) => call[0].accountId)).toEqual(['acc-a', 'acc-b']);

    fireEvent.click(document.getElementById('namespace') as HTMLElement);
    // The picker renders in a portal the dialog marks aria-hidden, so it is found by selector, not role.
    await waitFor(() => expect(document.querySelector('[role="listbox"]')).not.toBeNull());
    const listbox = document.querySelector('[role="listbox"]') as HTMLElement;
    await waitFor(() => expect(within(listbox).getByText('payments')).toBeInTheDocument());
    // A namespace present in both accounts is offered once.
    expect(
      within(listbox)
        .getAllByText(/^(payments|search|shared)$/)
        .map((o) => o.textContent)
    ).toEqual(['payments', 'search', 'shared']);

    fireEvent.keyDown(listbox, { key: 'Escape' });

    fireEvent.click(document.getElementById('aggregation-key') as HTMLElement);
    await waitFor(() =>
      expect(Array.from(document.querySelectorAll('[role="listbox"]')).some((l) => l.textContent?.includes('HighLatency'))).toBe(true)
    );
    const alerts = Array.from(document.querySelectorAll('[role="listbox"]')).find((l) => l.textContent?.includes('HighLatency')) as HTMLElement;
    // An alert both accounts define is offered once; blank names are dropped.
    expect(
      within(alerts)
        .getAllByText(/^(HighLatency|PodCrash)$/)
        .map((o) => o.textContent)
    ).toEqual(['HighLatency', 'PodCrash']);
  });

  it('keeps the options of the latest account selection when an earlier load finishes last', async () => {
    apiKubernetes.getK8sNamespaces.mockImplementation((_limit: number, _offset: number, { accountId }: { accountId: string }) =>
      accountId === 'acc-a'
        ? new Promise((resolve) => setTimeout(() => resolve({ data: { k8s_namespaces: [{ name: 'ns-a' }] } }), 50))
        : Promise.resolve({ data: { k8s_namespaces: [{ name: 'ns-b' }] } })
    );
    const props = { open: true, handleClose: jest.fn(), listNotificationRules: jest.fn(), editingSource: 'troubleshoot' };
    const { rerender } = render(<NotificationRuleModal {...props} notificationRuleObject={{ ...multiAccountRule, account_ids: '["acc-a"]' }} />);
    await waitFor(() => expect(apiKubernetes.getK8sNamespaces).toHaveBeenCalledTimes(1));
    rerender(<NotificationRuleModal {...props} notificationRuleObject={{ ...multiAccountRule, id: 'rule-8', account_ids: '["acc-b"]' }} />);
    await waitFor(() => expect(apiKubernetes.getK8sNamespaces).toHaveBeenCalledTimes(2));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 100));
    });

    fireEvent.click(document.getElementById('namespace') as HTMLElement);
    await waitFor(() => expect(document.querySelector('[role="listbox"]')?.textContent).toContain('ns-b'));
    expect(document.querySelector('[role="listbox"]')?.textContent).not.toContain('ns-a');
  });

  it('requires at least one account on an account-scoped tab', async () => {
    renderModal();
    await waitFor(() => expect(document.getElementById('account-2')).toBeInTheDocument());

    fireEvent.change(document.getElementById('notificationName') as HTMLElement, { target: { value: 'Prod alerts' } });
    const suppressSwitch = document.querySelector('#enable-notification input, input#enable-notification') as HTMLElement;
    fireEvent.click(suppressSwitch);
    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => expect(screen.getByText('Select at least one account')).toBeInTheDocument());
    expect(apiNotifications.insertNotificationRule).not.toHaveBeenCalled();
  });
});
