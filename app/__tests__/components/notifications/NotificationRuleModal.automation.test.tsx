import React from 'react';
import { render, screen, waitFor, fireEvent, act } from '@testing-library/react';
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

const CHATTER = ['automation.task.completed', 'automation.task.progress', 'automation.pr.created'];

const renderModal = (rule: any = {}) =>
  render(<NotificationRuleModal open handleClose={jest.fn()} listNotificationRules={jest.fn()} notificationRuleObject={rule} />);

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

describe('NotificationRuleModal — Automation tab', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    apiNotifications.getInstalledTools.mockResolvedValue({ messaging_platforms: [] });
    apiNotifications.getNotificationRules.mockResolvedValue({ data: {} });
    apiNotifications.getNotificationRuleEventTypes.mockResolvedValue([]);
    apiNotifications.insertNotificationRule.mockResolvedValue({ data: { data: { notifications_upsert_rule: { id: 'rule-1' } } } });
    apiDashboard.getCloudAccounts.mockResolvedValue([]);
    apiKubernetes.getK8sNamespaces.mockResolvedValue({ data: { k8s_namespaces: [] } });
    apiKubernetes.getAllK8sWorkload.mockResolvedValue({ data: [] });
    k8sApi.getEventRules.mockResolvedValue({ data: [] });
    apiUser.listUsers.mockResolvedValue({ data: [] });
    apiAutoPlaybook.listSlackChannels.mockResolvedValue({ data: [] });
    apiAutoPlaybook.listMSTeamsChannels.mockResolvedValue({ data: [] });
    apiAccount.getNotificationChannelList.mockResolvedValue({ data: { data: [] } });
    apiIntegrations.listIntegrations.mockResolvedValue({ data: { rows: [] } });
  });

  afterEach(flush);

  it('preselects the routine task events and never offers approval requests', async () => {
    renderModal();
    await waitFor(() => expect(document.getElementById('tab-automation')).toBeInTheDocument());

    fireEvent.click(document.getElementById('tab-automation') as HTMLElement);

    await waitFor(() => expect(screen.getByText('Events')).toBeInTheDocument());
    for (const value of CHATTER) {
      expect(document.getElementById(`event-type-${value.replace(/\./g, '-')}`)).toBeChecked();
    }
    expect(document.getElementById('event-type-automation-task-failed')).not.toBeChecked();
    expect(document.getElementById('event-type-automation-pr-failed')).not.toBeChecked();
    expect(document.getElementById('event-type-automation-approval-requested')).toBeNull();
    // Scope is every account: no account picker on this tab.
    expect(document.getElementById('account-1')).toBeNull();
    expect(document.getElementById('account-2')).toBeNull();
  });

  it('submits a suppressed automation rule with the selected event types and no account', async () => {
    renderModal();
    await waitFor(() => expect(document.getElementById('tab-automation')).toBeInTheDocument());
    fireEvent.click(document.getElementById('tab-automation') as HTMLElement);
    await waitFor(() => expect(screen.getByText('Events')).toBeInTheDocument());

    fireEvent.change(document.getElementById('notificationName') as HTMLElement, { target: { value: 'Silence task updates' } });
    // Drop one of the preselected events to prove the selection is what is sent.
    fireEvent.click(document.getElementById('event-type-automation-pr-created') as HTMLElement);
    // Switch the rule to Suppressed so no channel is required.
    const suppressSwitch = document.querySelector('#enable-notification input, input#enable-notification') as HTMLElement;
    fireEvent.click(suppressSwitch);

    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => expect(apiNotifications.insertNotificationRule).toHaveBeenCalledTimes(1));
    const payload = apiNotifications.insertNotificationRule.mock.calls[0][0];
    expect(payload.source).toBe('automation');
    expect(payload.isSuppressed).toBe(true);
    expect(payload.event_types).toEqual(['automation.task.completed', 'automation.task.progress']);
    expect(payload.accountIds).toBeUndefined();
    expect(payload.delivery).toBeUndefined();
  });

  it('refuses to save an automation rule with no event selected', async () => {
    renderModal();
    await waitFor(() => expect(document.getElementById('tab-automation')).toBeInTheDocument());
    fireEvent.click(document.getElementById('tab-automation') as HTMLElement);
    await waitFor(() => expect(screen.getByText('Events')).toBeInTheDocument());

    fireEvent.change(document.getElementById('notificationName') as HTMLElement, { target: { value: 'Empty rule' } });
    for (const value of CHATTER) {
      fireEvent.click(document.getElementById(`event-type-${value.replace(/\./g, '-')}`) as HTMLElement);
    }
    const suppressSwitch = document.querySelector('#enable-notification input, input#enable-notification') as HTMLElement;
    fireEvent.click(suppressSwitch);

    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => expect(screen.getByText('Select at least one event')).toBeInTheDocument());
    expect(apiNotifications.insertNotificationRule).not.toHaveBeenCalled();
  });

  it('loads an existing automation rule with its event types', async () => {
    apiNotifications.getNotificationRuleEventTypes.mockResolvedValue(['automation.task.failed']);
    renderModal({
      id: 'rule-9',
      name: 'Silence task updates',
      source: 'automation',
      account_id: null,
      is_suppressed: true,
      notification_rule_mappings: [],
    });

    await waitFor(() => expect(screen.getByText('Events')).toBeInTheDocument());
    await waitFor(() => expect(document.getElementById('event-type-automation-task-failed')).toBeChecked());
    expect(apiNotifications.getNotificationRuleEventTypes).toHaveBeenCalledWith('rule-9');
    expect(document.getElementById('event-type-automation-task-completed')).not.toBeChecked();
  });
});
