import React, { useState } from 'react';
import { Box } from '@mui/material';
import Tabs from '@shared/navigation/Tabs';
import ErrorBoundary from '@shared/ErrorBoundary';
import OverviewCards from './AdminOverviewCards';
import AdminConnections from './AdminConnections';

// The admin landing tab (first in the strip, default no-hash landing at
// /user-management). Two internal sub-views, switched by a filter toggle:
//   - Overview    → module cards + quick links into each admin area
//   - Connections → Kubernetes clusters' logs/metrics/traces coverage
//
// The sub-view is component-local state, not a URL fragment: like Tenant
// Settings' own General/Label/Features tabs, it isn't separately hash-routed or
// listed in global search. The tab itself (#overview) is.

const SUB_VIEWS = {
  tabOptions: [
    { value: 'overview', text: 'Overview' },
    { value: 'connections', text: 'Connections' },
  ],
};

const AdminOverview = () => {
  const [subView, setSubView] = useState('overview');

  return (
    <Box>
      <Tabs value={subView} onChange={setSubView} behavior='filter' ariaLabel='Admin overview sub-views' options={SUB_VIEWS} />
      <Box sx={{ mt: 'var(--ds-space-3)' }}>
        <ErrorBoundary key={subView}>{subView === 'connections' ? <AdminConnections /> : <OverviewCards />}</ErrorBoundary>
      </Box>
    </Box>
  );
};

export default AdminOverview;
