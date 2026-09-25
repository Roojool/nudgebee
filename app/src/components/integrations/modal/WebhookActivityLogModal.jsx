import { useEffect, useMemo, useState } from 'react';
import PropTypes from 'prop-types';
import dayjs from 'dayjs';
import { Box, Stack, Typography } from '@mui/material';
import { Modal } from '@ui/Modal';
import FilterDropdown from '@ui/FilterDropdown';
import CustomDateTimeRangePicker from '@components/common/widgets/CustomDateTimeRangePicker';
import CustomTable from '@shared/tables/CustomTable';
import apiIntegrations from '@api1/integrations';
import apiUser from '@api1/user';
import { formatCount } from '@utils/common';
import { ds } from '@utils/colors';

const HEADERS = ['Title', 'Type', 'Status', 'Priority', 'Created At'];

// Expanded row: the raw webhook payload. Skipped / failed events store no
// extracted title/type (only `processed` events do), so the payload is where
// the "why" lives — for a skipped event its `event_type` (e.g. an annotation
// that is not an alert), for a failed one the body that could not be parsed.
// The row's raw + processing_status ride on the last cell (see tableData).
const WebhookEventExpandedRow = ({ row }) => {
  const meta = (Array.isArray(row) && row[row.length - 1]) || {};
  const raw = meta._raw;
  const status = meta._status;
  let pretty = raw;
  try {
    if (raw) {
      pretty = JSON.stringify(JSON.parse(raw), null, 2);
    }
  } catch {
    // Not valid JSON — show it verbatim rather than nothing.
  }
  return (
    <Box sx={{ p: ds.space[3] }}>
      {status && status !== 'processed' && (
        <Typography sx={{ fontSize: ds.text.small, color: ds.gray[600], mb: ds.space[2] }}>
          This event was <strong>{status}</strong> — stored but not turned into an alert. The raw payload below is what was received.
        </Typography>
      )}
      {raw ? (
        <Box
          component='pre'
          sx={{
            m: 0,
            p: ds.space[3],
            borderRadius: ds.radius.lg,
            backgroundColor: ds.gray[100],
            border: `1px solid ${ds.gray[200]}`,
            overflow: 'auto',
            maxHeight: ds.space.mul(1, 100),
            fontSize: ds.text.small,
            fontFamily: 'monospace',
            color: ds.gray[700],
          }}
        >
          {pretty}
        </Box>
      ) : (
        <Typography sx={{ fontSize: ds.text.small, color: ds.gray[500] }}>No payload stored for this event.</Typography>
      )}
    </Box>
  );
};

WebhookEventExpandedRow.propTypes = {
  row: PropTypes.array,
};

// One count widget (Total / Total Processed). Kept inline — there is no DS stat
// primitive, and these two tiles are the only place this shape is needed.
const StatTile = ({ label, value, loading, testId }) => (
  <Box
    data-testid={testId}
    sx={{
      flex: 1,
      p: ds.space[4],
      border: `1px solid ${ds.gray[200]}`,
      borderRadius: ds.radius.lg,
      backgroundColor: ds.gray[100],
    }}
  >
    <Typography sx={{ fontSize: ds.text.title, fontWeight: ds.weight.semibold, color: ds.gray[800] }}>
      {loading ? '—' : formatCount(value)}
    </Typography>
    <Typography sx={{ fontSize: ds.text.small, color: ds.gray[500] }}>{label}</Typography>
  </Box>
);

StatTile.propTypes = {
  label: PropTypes.string.isRequired,
  value: PropTypes.number,
  loading: PropTypes.bool,
  testId: PropTypes.string,
};

const WebhookActivityLogModal = ({ open, onClose, integration }) => {
  const [loading, setLoading] = useState(false);
  const [events, setEvents] = useState([]);
  const [total, setTotal] = useState(0);
  const [processed, setProcessed] = useState(0);
  const [currentPage, setCurrentPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(apiUser.getUserPreferencesTablePageSize());
  // Optional narrowing filters. Both default to "off" so the log opens on the
  // integration's full history; the widget counts track whatever is filtered.
  const [accountFilter, setAccountFilter] = useState('');
  const [dateSelection, setDateSelection] = useState(null); // { startTime, endTime } epoch ms, or null = all time

  const integrationId = integration?.id;

  // The accounts this webhook is wired to — the only accounts its events can be
  // routed to — become the Account filter's options.
  const accountOptions = useMemo(() => {
    const linked = Array.isArray(integration?.integrations_cloud_accounts) ? integration.integrations_cloud_accounts : [];
    return linked.filter((a) => a?.cloud_account_id).map((a) => ({ label: a.cloud_account_name || a.cloud_account_id, value: a.cloud_account_id }));
  }, [integration]);

  // Pre-fill the date picker with the last 24h for convenience; it does not
  // filter anything until the user applies a range (dateSelection stays null).
  const defaultDateDisplay = useMemo(() => {
    const end = Date.now();
    return { startTime: end - 24 * 60 * 60 * 1000, endTime: end };
  }, [open, integrationId]);

  const handleDateRangeChange = ({ selection }) => {
    setDateSelection({ startTime: selection.startTime, endTime: selection.endTime });
    setCurrentPage(0);
  };

  // Reset page + filters whenever the modal opens for a different integration,
  // so an event / filter from the previously-viewed webhook never bleeds through.
  useEffect(() => {
    if (open) {
      setCurrentPage(0);
      setAccountFilter('');
      setDateSelection(null);
    }
  }, [open, integrationId]);

  useEffect(() => {
    if (!open || !integrationId) {
      return;
    }
    let cancelled = false;
    setLoading(true);
    apiIntegrations
      .listWebhookEvents({
        integrationId,
        accountId: accountFilter || undefined,
        startDate: dateSelection?.startTime,
        endDate: dateSelection?.endTime,
        limit: rowsPerPage,
        offset: currentPage * rowsPerPage,
      })
      .then((res) => {
        if (cancelled) {
          return;
        }
        setEvents(res.events || []);
        setTotal(res.total || 0);
        setProcessed(res.processed || 0);
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [open, integrationId, currentPage, rowsPerPage, accountFilter, dateSelection]);

  const tableData = useMemo(
    () =>
      events.map((e) => [
        { text: e.event_title || '-' },
        { text: e.event_type || '-' },
        { text: e.event_status || '-' },
        { text: e.event_priority || '-' },
        // Raw payload + processing_status ride on the last cell so the expanded
        // row (WebhookEventExpandedRow) can read them off row[row.length - 1].
        {
          text: e.event_created_at ? dayjs(e.event_created_at).format('DD MMM YYYY, HH:mm') : '-',
          _raw: e.raw,
          _status: e.processing_status,
        },
      ]),
    [events]
  );

  return (
    <Modal
      open={open}
      handleClose={onClose}
      width='lg'
      title={
        <Typography component='h2' variant='h6' fontWeight={ds.weight.semibold}>
          {`Activity Log${integration?.name ? `: ${integration.name}` : ''}`}
        </Typography>
      }
    >
      <Stack spacing={ds.space[4]}>
        <Stack direction='row' spacing={ds.space[4]}>
          <StatTile label='Total Events' value={total} loading={loading} testId='webhook-activity-total' />
          <StatTile label='Total Processed Events' value={processed} loading={loading} testId='webhook-activity-processed' />
        </Stack>
        <Stack direction='row' spacing={ds.space[3]} alignItems='center' justifyContent='flex-end' useFlexGap flexWrap='wrap'>
          {accountOptions.length > 0 && (
            <FilterDropdown
              id='webhook-activity-account-filter'
              label='Account'
              options={accountOptions}
              value={accountOptions.find((o) => o.value === accountFilter) ?? null}
              onSelect={(_e, item) => {
                setAccountFilter(item?.value || '');
                setCurrentPage(0);
              }}
            />
          )}
          <CustomDateTimeRangePicker
            passedSelectedDateTime={dateSelection || defaultDateDisplay}
            onChange={handleDateRangeChange}
            sx={{ height: ds.space[6] }}
          />
        </Stack>
        <CustomTable
          id='webhook-activity-log'
          loading={loading}
          tableData={tableData}
          headers={HEADERS}
          totalRows={total}
          rowsPerPage={rowsPerPage}
          pageNumber={currentPage + 1}
          onPageChange={(page, limit) => {
            setCurrentPage(page - 1);
            setRowsPerPage(limit);
          }}
          showExpandable
          expandable={{ component: WebhookEventExpandedRow }}
        />
      </Stack>
    </Modal>
  );
};

WebhookActivityLogModal.propTypes = {
  open: PropTypes.bool.isRequired,
  onClose: PropTypes.func.isRequired,
  integration: PropTypes.shape({
    id: PropTypes.string,
    name: PropTypes.string,
  }),
};

export default WebhookActivityLogModal;
