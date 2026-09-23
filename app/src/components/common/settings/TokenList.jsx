import React from 'react';
import { Typography } from '@mui/material';
import { Button } from '@ui/Button';
import { EmptyState } from '@ui/EmptyState';
import CustomTable from '@shared/tables/CustomTable';
import dayjs from 'dayjs';
import { carriesGatewayTraffic, KIND_LABELS } from './keyKinds';

const formatDate = (date) => dayjs(date).format('MMM DD, YYYY HH:mm');

// One key kind's rows. Gateway-reaching kinds also show the key's caps and its spend.
const TokenList = ({ tokens, kind, onRevoke }) => {
  const rows = tokens.filter((t) => t.kind === kind);
  const showQuotas = carriesGatewayTraffic(kind);

  if (rows.length === 0) {
    return <EmptyState title={`No ${KIND_LABELS[kind]} keys yet`} size='section' />;
  }

  const headers = ['Name', 'Created', 'Last used', 'Expires', ...(showQuotas ? ['Limits', 'This month'] : []), ''];

  const tableData = rows.map((t) => {
    const row = [
      { text: t.name, component: <Typography variant='body2'>{t.name}</Typography> },
      { text: formatDate(t.created_at) },
      { text: t.accessed_at ? formatDate(t.accessed_at) : '—' },
      { text: t.expires_at ? formatDate(t.expires_at) : 'never' },
    ];
    if (showQuotas) {
      row.push({
        text: (t.limits || []).map((l) => `${l.value} ${l.metric}/${l.period}${l.model ? ` (${l.model})` : ''}`).join(', ') || '—',
      });
      row.push({ text: t.usage ? `$${(t.usage.cost_usd ?? 0).toFixed(2)} · ${t.usage.requests ?? 0} reqs this month` : '—' });
    }
    row.push({
      component: (
        <Button tone='danger' size='sm' onClick={() => onRevoke(t.id, t.name)}>
          Revoke
        </Button>
      ),
    });
    return row;
  });

  return <CustomTable headers={headers} tableData={tableData} />;
};

export default TokenList;
