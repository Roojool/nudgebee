import React, { useEffect, useState } from 'react';
import k8sApi, { firstGraphQLError } from '@api1/kubernetes1';
import { ListingLayout } from '@ui/ListingLayout';
import FilterDropdown from '@ui/FilterDropdown';
import CustomSearch from '@shared/CustomSearch';
import { Button as DsButton } from '@ui/Button';
import DownloadButton from '@shared/buttons/DownloadButton';
import { Label } from '@ui/Label';
import { SeverityIcon } from '@ui/SeverityIcon';

// API returns severity as 'Critical' / 'High' / 'Medium' / 'Low' / 'Info'
// titlecased. ds/SeverityIcon's level enum is the lowercase 5-tier.
const SEVERITY_TO_DS_LEVEL: Record<string, 'critical' | 'high' | 'medium' | 'low' | 'info'> = {
  critical: 'critical',
  high: 'high',
  medium: 'medium',
  low: 'low',
  info: 'info',
};
const toDsSeverityLevel = (s: string | undefined) => SEVERITY_TO_DS_LEVEL[String(s || '').toLowerCase()] || 'info';
import ThreeDotsMenu from '@shared/ds/ThreeDotsMenu';
import { Modal } from '@ui/Modal';
import KubernetesCreateAlert from './KubernetesCreateAlert';
import { hasWriteAccess } from '@lib/auth';
import NDialog from '@shared/modal/NDialog';
import { titleCase } from '@lib/formatter';
import { action } from 'src/utils/actionStyles';
import Text from '@shared/format/Text';
import apiUser from '@api1/user';
import CustomTable from '@shared/tables/CustomTable2';
import { useRouter } from 'next/router';
import { toast as snackbar } from '@ui/Toast';
import { isValidSeverity, safeJSONParse, snakeToTitleCase } from 'src/utils/common';
import {
  ALERT_RULE_MENU,
  AlertRuleItem,
  alertRuleStatusText,
  buildAlertRuleMenuItems,
  muteConfirmTitle,
  toggleConfirmTitle,
} from './alertRuleControls';

interface KubernetesAlertManagerProps {
  accountId: string;
}

const TABLE_HEADERS = [
  { name: 'Name', width: '25%' },
  { name: 'Category', width: '15%' },
  { name: 'Source', width: '10%' },
  { name: 'Severity', width: '10%' },
  { name: 'Status', width: '10%' },
  { name: 'Configured Actions', width: '25%' },
  { name: '', width: '5%' },
];

const KubernetesAlertManager: React.FC<KubernetesAlertManagerProps> = ({ accountId }) => {
  const router = useRouter();
  const k8sAlertManager = 'k8sMgr';

  const [data, setData] = useState<any[][]>([]);
  const [totalCount, setTotalCount] = useState(0);
  const [currentPage, setCurrentPage] = useState(0);
  const [loading, setLoading] = useState(false);
  const [openCreateNewAlertModal, setOpenCreateNewAlertModal] = useState<boolean>(false);
  const [isCreateAlert, setIsCreateAlert] = useState<boolean>(true);
  const [alertManagerObject, setAlertManagerObject] = useState<any | null>({});
  const [categoryList, setCategoryList] = useState<string[]>([]);
  const [selectedCategory, setSelectedCategory] = useState<string>('');
  const [sourceList, setSourceList] = useState<string[]>([]);
  const [selectedSource, setSelectedSource] = useState<string>('');
  const [severityList, setSeverityList] = useState<string[]>([]);
  const [selectedSeverity, setSelectedSeverity] = useState<string>('');
  const [selectedStatus, setSelectedStatus] = useState<string>('');
  const [disableAlert, setDisableAlert] = useState(false);
  const [searchByName, setSearchByName] = useState<string>(typeof router.query?.name === 'string' ? router.query.name : '');
  const [searchNameInput, setSearchNameInput] = useState<string>(typeof router.query?.name === 'string' ? router.query.name : '');
  const [rowsPerPage, setRowsPerPage] = useState(apiUser.getUserPreferencesTablePageSize());
  const [alertNames, setAlertNames] = useState<string[]>([]);
  const [agentPlaybookOnEvents, setAgentPlaybookOnEvents] = useState<any[]>([]);
  const [muteTarget, setMuteTarget] = useState<{ item: AlertRuleItem; mute: boolean } | null>(null);

  const getMenuItems = (item: AlertRuleItem) => buildAlertRuleMenuItems(item, hasWriteAccess(accountId));

  const statusCell = (item: AlertRuleItem) => ({ component: <Label margin='auto' text={alertRuleStatusText(item)} /> });
  // rowItem lets later patches (capabilities, toggle, mute) find and rebuild the row.
  const menuCell = (item: AlertRuleItem) => ({
    rowItem: item,
    component: <ThreeDotsMenu sx={{ ...action.primary }} onMenuClick={onMenuClick} data={item} menuItems={getMenuItems(item)} />,
  });

  const replaceRowItems = (updatedById: Record<string, AlertRuleItem>) => {
    setData((prevData: any) =>
      prevData.map((row: any) => {
        const updated = updatedById[row[6]?.rowItem?.id];
        if (!updated) {
          return row;
        }
        const newRow = [...row];
        newRow[4] = statusCell(updated);
        newRow[6] = menuCell(updated);
        return newRow;
      })
    );
  };

  const onMenuClick = (menuItem: any, data: any) => {
    if (menuItem.id === ALERT_RULE_MENU.mute || menuItem.id === ALERT_RULE_MENU.unmute) {
      setMuteTarget({ item: data, mute: menuItem.id === ALERT_RULE_MENU.mute });
    }
    if (menuItem.id === ALERT_RULE_MENU.toggle) {
      setDisableAlert(true);
      setAlertManagerObject(data);
    }
    if (menuItem.id === ALERT_RULE_MENU.edit) {
      setIsCreateAlert(false);
      setOpenCreateNewAlertModal(true);
      setAlertManagerObject(data);
    }
  };

  useEffect(() => {
    if (!accountId || accountId === 'undefined') return;
    k8sApi
      .getDistinctData(accountId)
      .then((res: any) => {
        if (res?.data?.distinct_category) {
          setCategoryList(res?.data?.distinct_category.map((c: any) => c.category));
        }
        if (res?.data?.distinct_source) {
          setSourceList(
            res?.data?.distinct_source.map((c: any) => ({
              label: titleCase(c.source),
              value: c.source,
            }))
          );
        }
        if (res?.data?.distinct_severity) {
          setSeverityList(
            res?.data?.distinct_severity.map((c: any) => ({
              label: titleCase(c.severity),
              value: c.severity,
            }))
          );
        }
      })
      .catch((error: any) => {
        console.error(error);
      });
  }, [accountId]);

  useEffect(() => {
    listAlertManager();
  }, [currentPage, selectedCategory, selectedSeverity, selectedSource, selectedStatus, searchByName, accountId, rowsPerPage]);

  useEffect(() => {
    if (!alertNames.length) {
      return;
    }
    k8sApi
      .getAgentPlaybookOfEvent({
        accountId,
        alertName: alertNames,
      })
      .then((res) => {
        const agentPlaybooks = res?.data?.data?.agent_playbook || [];
        if (agentPlaybooks) {
          setAgentPlaybookOnEvents(agentPlaybooks);
          // Functional update: the capabilities patch may have replaced rows since this effect started.
          setData((prevData: any) =>
            prevData.map((row: any) => {
              const itemData = [...row];
              const item = agentPlaybooks.find((g: any) => g.alert_name == itemData[0].drilldownQuery.name);
              const actionParamParsed = typeof item?.action_params === 'string' ? safeJSONParse(item.action_params) : item?.action_params;
              if (actionParamParsed && Array.isArray(actionParamParsed) && actionParamParsed.length > 0) {
                itemData[5] = {
                  component: actionParamParsed
                    .map((obj: any) => {
                      const dynamicKey = Object.keys(obj)[0];
                      return obj[dynamicKey]?.title || snakeToTitleCase(dynamicKey);
                    })
                    .join(', '),
                };
              } else {
                itemData[5] = { text: '-' };
              }
              return itemData;
            })
          );
        }
      });
  }, [alertNames]);

  const listAlertManager = () => {
    if (!accountId || accountId === 'undefined') return;
    setLoading(true);
    setData([]);
    setAlertNames([]);
    k8sApi
      .getEventRules(
        {
          accountId: accountId,
          category: selectedCategory,
          severity: selectedSeverity,
          source: selectedSource,
          status: selectedStatus,
          searchByName: searchByName,
        },
        rowsPerPage,
        currentPage * rowsPerPage
      )
      .then((res: any) => {
        setLoading(false);
        setTotalCount(res?.data?.event_rules_aggregate?.aggregate?.count);
        const alertNames = [] as string[];
        const items: AlertRuleItem[] = res?.data?.event_rules || [];
        const data = items.map((item: any) => {
          alertNames.push(item.alert);
          return [
            { component: <Text value={item.alert} />, drilldownQuery: { name: item.alert } },
            { component: <Text value={item.category} /> },
            { component: <Text value={item?.source || '-'} /> },
            { component: <SeverityIcon level={toDsSeverityLevel(item?.severity)} />, data: item?.severity },
            statusCell(item),
            {
              text: '--',
            },
            menuCell(item),
          ];
        });
        setData(data);
        setAlertNames(alertNames);
        k8sApi.getEventRuleCapabilities(items.map((item) => item.id)).then((capabilities) => {
          const updated: Record<string, AlertRuleItem> = {};
          items.forEach((item) => {
            if (capabilities[item.id]) {
              updated[item.id] = { ...item, capabilities: capabilities[item.id] };
            }
          });
          replaceRowItems(updated);
        });
      })
      .catch(() => {
        setLoading(false);
      });
  };

  const onPageChange = (page: number, limit: number) => {
    setCurrentPage(page - 1);
    setRowsPerPage(limit);
  };

  const onSubmit = (message: string, severity: string) => {
    if (severity === 'success') {
      listAlertManager();
    }
    if (severity && isValidSeverity(severity)) {
      snackbar[severity](message);
    }
  };

  const handleCloseCreateNewAlertModal = () => {
    setOpenCreateNewAlertModal(false);
    setAlertManagerObject(null);
    setIsCreateAlert(true);
  };

  const handleCloseAlertPopUp = () => {
    setDisableAlert(false);
  };

  const handleSubmit = () => {
    const previousItem: AlertRuleItem = alertManagerObject;
    const wasEnabled = previousItem.enabled;
    const request: any = {
      accountId: accountId,
      alert: previousItem.alert,
      enable: !wasEnabled,
      id: previousItem?.id,
      namespace: previousItem?.namespace || '',
      group: previousItem?.group || '',
    };

    replaceRowItems({ [previousItem.id]: { ...previousItem, enabled: !wasEnabled } });
    handleCloseAlertPopUp();

    const failed = (reason?: string) => {
      snackbar.error(`Failed to ${wasEnabled ? 'disable' : 'enable'} ${previousItem.alert}${reason ? `: ${reason}` : ''}`);
      replaceRowItems({ [previousItem.id]: previousItem });
    };
    k8sApi
      .disableAlertManager(request)
      .then((res: any) => {
        if (res instanceof Error || res?.data?.errors?.length > 0 || res?.data?.data?.errors) {
          failed(firstGraphQLError(res));
          return;
        }
        snackbar.success(`Rule ${previousItem.alert} ${wasEnabled ? 'disabled' : 'enabled'}`);
      })
      .catch(() => failed());
  };

  const handleMute = () => {
    if (!muteTarget) {
      return;
    }
    const { item, mute } = muteTarget;
    setMuteTarget(null);
    k8sApi
      .updateEventRuleMute({ rule_id: item.id, account_id: accountId, muted: mute })
      .then((res: any) => {
        const capabilities = res?.data?.data?.mute;
        if (res instanceof Error || res?.data?.errors?.length > 0 || !capabilities) {
          const reason = firstGraphQLError(res);
          snackbar.error(`Failed to ${mute ? 'mute' : 'unmute'} ${item.alert}${reason ? `: ${reason}` : ''}`);
          return;
        }
        replaceRowItems({ [item.id]: { ...item, capabilities } });
        snackbar.success(`${item.alert} ${mute ? 'muted' : 'unmuted'}`);
      })
      .catch(() => snackbar.error(`Failed to ${mute ? 'mute' : 'unmute'} ${item.alert}`));
  };

  const onClickLoader = (loaderStatus: boolean) => {
    setLoading(loaderStatus);
  };

  const linkedPlaybook = agentPlaybookOnEvents?.find((f) => f.alert_name === alertManagerObject?.alert);
  const linkedPlaybookActions =
    typeof linkedPlaybook?.action_params === 'string' ? safeJSONParse(linkedPlaybook.action_params) : linkedPlaybook?.action_params;
  const linkedActionsCount = Array.isArray(linkedPlaybookActions) ? linkedPlaybookActions.length : 0;

  return (
    <div>
      <NDialog
        buttonText='Confirm'
        handleClose={handleCloseAlertPopUp}
        dialogTitle={toggleConfirmTitle(alertManagerObject)}
        handleSubmit={handleSubmit}
        open={disableAlert}
        dialogContent={
          alertManagerObject?.enabled
            ? linkedActionsCount > 0
              ? `This alert will stop triggering until it's re-enabled, and the ${linkedActionsCount} action${
                  linkedActionsCount > 1 ? 's' : ''
                } configured on it will no longer run.`
              : "This alert will stop triggering until it's re-enabled."
            : 'This alert will resume triggering as configured.'
        }
        additionalComponent={undefined}
      />
      <Modal
        confirmText={muteTarget?.mute ? 'Mute' : 'Unmute'}
        handleClose={() => setMuteTarget(null)}
        title={muteConfirmTitle(muteTarget?.item, !!muteTarget?.mute)}
        onConfirm={handleMute}
        open={!!muteTarget}
        width='md'
      />
      <Modal
        width='md'
        open={openCreateNewAlertModal}
        handleClose={handleCloseCreateNewAlertModal}
        title={isCreateAlert ? 'Create New Alert' : 'Update Alert'}
        contentStyles={{ padding: '0px' }}
        maxHeight='80vh'
        rightComponentOnTitle={undefined}
        loader={loading}
      >
        <KubernetesCreateAlert
          alertManagerObject={alertManagerObject}
          onSubmit={onSubmit}
          accountId={accountId}
          handleCloseCreateNewAlertModal={handleCloseCreateNewAlertModal}
          isCreateAlert={isCreateAlert}
          onClickLoader={onClickLoader}
          agentPlaybookOnEvent={agentPlaybookOnEvents?.filter((f) => f.alert_name == alertManagerObject?.alert) || []}
        />
      </Modal>
      <ListingLayout id='alert-manager-list-box'>
        <ListingLayout.Toolbar
          actions={
            <>
              <DownloadButton onClick={() => ({ tableId: k8sAlertManager })} />
              {hasWriteAccess(accountId) && (
                <DsButton
                  tone='primary'
                  size='md'
                  onClick={() => {
                    setAlertManagerObject(null);
                    setOpenCreateNewAlertModal(true);
                  }}
                >
                  Create New Alert
                </DsButton>
              )}
            </>
          }
        >
          <FilterDropdown
            id='k8s-alert-filter-category'
            label='Category'
            options={categoryList}
            value={selectedCategory}
            onSelect={(e: React.ChangeEvent<HTMLInputElement>) => {
              setSelectedCategory(e?.target?.value);
              setCurrentPage(0);
            }}
          />
          <FilterDropdown
            id='k8s-alert-filter-severity'
            label='Severity'
            options={severityList}
            value={selectedSeverity}
            onSelect={(e: React.ChangeEvent<HTMLInputElement>) => {
              setSelectedSeverity(e?.target?.value);
              setCurrentPage(0);
            }}
          />
          <FilterDropdown
            id='k8s-alert-filter-source'
            label='Source'
            options={sourceList}
            value={selectedSource}
            onSelect={(e: React.ChangeEvent<HTMLInputElement>) => {
              setSelectedSource(e?.target?.value);
              setCurrentPage(0);
            }}
          />
          <FilterDropdown
            id='k8s-alert-filter-status'
            label='Status'
            options={['Enabled', 'Disabled']}
            value={selectedStatus}
            onSelect={(e: React.ChangeEvent<HTMLInputElement>) => {
              setSelectedStatus(e?.target?.value);
              setCurrentPage(0);
            }}
          />
          <CustomSearch
            id='k8s-alert-name-search'
            value={searchNameInput}
            onChange={(next: string) => {
              setSearchNameInput((prev: string) => {
                if (prev.trim() !== '' && next.trim() === '') {
                  setSearchByName('');
                  setCurrentPage(0);
                }
                return next;
              });
            }}
            onEnterPress={() => {
              setSearchByName(searchNameInput);
              setCurrentPage(0);
            }}
            onClear={() => {
              setSearchNameInput('');
              setSearchByName('');
              setCurrentPage(0);
            }}
            label='Search By Name'
          />
        </ListingLayout.Toolbar>
        <ListingLayout.Body>
          <CustomTable
            id={k8sAlertManager}
            totalRows={totalCount}
            tableData={data}
            headers={TABLE_HEADERS}
            rowsPerPage={rowsPerPage}
            showExpandable={false}
            loading={loading}
            onPageChange={onPageChange}
            onSortChange={undefined}
            pageNumber={currentPage + 1}
            tableHeadingCenter={['Severity', 'Status']}
          />
        </ListingLayout.Body>
      </ListingLayout>
    </div>
  );
};

export default KubernetesAlertManager;
