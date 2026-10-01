import React from 'react';
import PropTypes from 'prop-types';
import { Box } from '@mui/material';
import Tooltip from '@ui/Tooltip';
import { Chip, hashHue } from '@ui/Chip';

const TARGET_KIND = {
  kubernetes: 'cluster',
  aws: 'AWS account',
  gcp: 'GCP account',
  azure: 'Azure account',
};

/**
 * Where a tool call ran, from its persisted metadata (`nb_executed_target`).
 * Set only when the call ran on a cluster or account other than the
 * conversation's own; returns null otherwise.
 */
export const parseExecutedTarget = (metadata) => {
  if (!metadata) {
    return null;
  }
  let parsed = metadata;
  if (typeof parsed === 'string') {
    try {
      parsed = JSON.parse(parsed);
    } catch {
      return null;
    }
  }
  const target = parsed?.nb_executed_target;
  return target?.name ? target : null;
};

/** Small tag naming the cluster or account a step ran on. */
const ExecutedTargetChip = ({ metadata }) => {
  const target = parseExecutedTarget(metadata);
  if (!target) {
    return null;
  }
  const kind = TARGET_KIND[target.provider] || 'account';
  return (
    <Tooltip title={`Ran on the ${kind} ${target.name}`} placement='top'>
      <Box component='span' sx={{ display: 'inline-flex' }} data-testid='executed-target-chip'>
        <Chip variant='tag' size='xs' hue={hashHue(target.name)}>
          {target.name}
        </Chip>
      </Box>
    </Tooltip>
  );
};

ExecutedTargetChip.propTypes = {
  metadata: PropTypes.oneOfType([PropTypes.string, PropTypes.object]),
};

export default ExecutedTargetChip;
