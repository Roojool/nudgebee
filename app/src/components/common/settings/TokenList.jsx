import React from 'react';
import { Typography } from '@mui/material';
import { Button } from '@ui/Button';
import { Chip } from '@ui/Chip';
import { EmptyState } from '@ui/EmptyState';
import Tooltip from '@ui/Tooltip';
import CustomTable from '@shared/tables/CustomTable';
import dayjs from 'dayjs';
import { carriesGatewayTraffic, KIND_LABELS } from './keyKinds';

const formatDate = (date) => dayjs(date).format('MMM DD, YYYY HH:mm');

// 'all' takes the warm hue: it is the one kind that opens both the app API and the gateway.
const KIND_HUES = { platform: 'blue', gateway: 'violet', all: 'amber' };

const limitsText = (limits) => (limits || []).map((l) => `${l.value} ${l.metric}/${l.period}${l.model ? ` (${l.model})` : ''}`).join(', ') || '—';

const usageText = (usage) => (usage ? `$${(usage.cost_usd ?? 0).toFixed(2)} · ${usage.requests ?? 0} reqs` : '—');

// A key with no scopes has its owner's full access; otherwise list what it may call.
const AccessCell = ({ scopes }) => {
  if (!scopes?.length) return <Typography variant='body2'>Full access</Typography>;
  return (
    <Tooltip title={scopes.join(', ')}>
      <Typography variant='body2' component='span'>
        {`${scopes.length} ${scopes.length === 1 ? 'scope' : 'scopes'}`}
      </Typography>
    </Tooltip>
  );
};

// Every key in one table, labelled by kind. A column that applies to some kinds only shows
// '—' on the rest: a gateway key cannot call the app API, so it has no access to show, and
// limits and spend meter gateway calls only.
const TokenList = ({ tokens, onRevoke }) => {
  if (tokens.length === 0) {
    return <EmptyState title='No API tokens yet' size='section' />;
  }

  const headers = ['Name', 'Type', 'Created', 'Last used', 'Expires', 'Access', 'Limits', 'This month', ''];

  const tableData = tokens.map((t) => {
    const label = KIND_LABELS[t.kind] ?? t.kind;
    const reachesGateway = carriesGatewayTraffic(t.kind);
    return [
      { text: t.name, component: <Typography variant='body2'>{t.name}</Typography> },
      {
        text: label,
        component: (
          <Chip size='xs' variant='tag' hue={KIND_HUES[t.kind]}>
            {label}
          </Chip>
        ),
      },
      { text: formatDate(t.created_at) },
      { text: t.accessed_at ? formatDate(t.accessed_at) : '—' },
      { text: t.expires_at ? formatDate(t.expires_at) : 'never' },
      t.kind === 'gateway'
        ? { text: '—' }
        : { text: t.scopes?.length ? t.scopes.join(', ') : 'Full access', component: <AccessCell scopes={t.scopes} /> },
      { text: reachesGateway ? limitsText(t.limits) : '—' },
      { text: reachesGateway ? usageText(t.usage) : '—' },
      {
        component: (
          <Button tone='danger' size='sm' onClick={() => onRevoke(t.id, t.name)}>
            Revoke
          </Button>
        ),
      },
    ];
  });

  return <CustomTable headers={headers} tableData={tableData} />;
};

export default TokenList;
