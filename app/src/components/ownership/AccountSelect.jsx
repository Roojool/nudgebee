import React, { useEffect, useMemo, useState } from 'react';
import PropTypes from 'prop-types';
import { Select } from '@ui/Select';
import apiUserManagement from '@api1/user';
import CloudProviderIcon from '@shared/icons/CloudIcon';

// 'Other' means "no real cloud_provider", so it has no logo to show, unlike
// 'AWS'/'GCP'/'Azure'/'K8S'.
const renderAccountGroupIcon = (provider) =>
  provider === 'Other' ? null : <CloudProviderIcon cloud_provider={provider} width='14px' height='14px' />;

// Single-select of the tenant's cloud accounts. Shared by the rule modal, the
// bulk-assign modal, and the b-Cortex / admin AI & Tools account filters (which
// pass placeholder 'All accounts': an empty value is the tenant-wide view).
// Value is the account id; label is account_name.
// `providerFilter` optionally narrows the list by cloud_provider — e.g. K8S from
// ./accountProviders to show only Kubernetes clusters in K8s rule flows. Default = all.
export default function AccountSelect({ value, onChange, id, label, placeholder, required, clearable, disabled, providerFilter }) {
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;
    setLoading(true);
    apiUserManagement
      .listAccounts()
      .then((result) => {
        if (!active) {
          return;
        }
        setRows(Array.isArray(result) ? result : []);
      })
      .catch(() => active && setRows([]))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [providerFilter]);

  const options = useMemo(() => {
    const list = rows.filter((a) => (providerFilter ? providerFilter(a.cloud_provider) : true));
    return list.map((a) => ({ value: a.id, label: a.account_name || a.id, group: a.cloud_provider || 'Other' }));
  }, [rows, providerFilter]);

  return (
    <Select
      id={id || 'account-select'}
      label={label}
      placeholder={placeholder || 'Select an account'}
      options={options}
      grouped
      groupIcon={renderAccountGroupIcon}
      value={value || null}
      onChange={(next) => onChange(next || '')}
      loading={loading}
      searchable
      clearable={clearable !== false}
      required={required}
      disabled={disabled}
    />
  );
}

AccountSelect.propTypes = {
  value: PropTypes.string,
  onChange: PropTypes.func.isRequired,
  id: PropTypes.string,
  label: PropTypes.node,
  placeholder: PropTypes.string,
  required: PropTypes.bool,
  clearable: PropTypes.bool,
  disabled: PropTypes.bool,
  providerFilter: PropTypes.func,
};
