import React, { useState } from 'react';
import { Box, Typography, IconButton, CircularProgress } from '@mui/material';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import StopCircleOutlinedIcon from '@mui/icons-material/StopCircleOutlined';
import Tooltip from '@ui/Tooltip';
import { ds } from '@utils/colors';

export interface RunningTask {
  id: string;
  label: string;
  detail?: string;
  status?: string;
}

export interface RunningTasksBannerProps {
  tasks: RunningTask[];
  isProcessing: boolean;
  onStop?: () => void;
  allowStop?: boolean;
}

export const RunningTasksBanner: React.FC<RunningTasksBannerProps> = ({ tasks, isProcessing, onStop, allowStop = false }) => {
  const [isExpanded, setIsExpanded] = useState(true);

  if (!isProcessing || !tasks || tasks.length === 0) {
    return null;
  }

  const count = tasks.length;
  const countLabel = `${count} task${count === 1 ? '' : 's'} running`;

  return (
    <Box
      data-testid='running-tasks-banner'
      sx={{
        width: '100%',
        backgroundColor: 'var(--ds-background-100)',
        border: `1px solid ${ds.gray[200]}`,
        borderRadius: 'var(--ds-radius-lg)',
        boxShadow: `0 1px 3px ${ds.gray.alpha[100]}`,
        overflow: 'hidden',
        mb: ds.space[2],
        boxSizing: 'border-box',
        transition: 'all 0.2s ease',
      }}
    >
      {/* Header */}
      <Box
        role='button'
        tabIndex={0}
        onClick={() => setIsExpanded((prev) => !prev)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            setIsExpanded((prev) => !prev);
          }
        }}
        data-testid='running-tasks-header'
        sx={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          px: ds.space[3],
          py: '6px',
          cursor: 'pointer',
          userSelect: 'none',
          backgroundColor: 'var(--ds-background-200)',
          '&:hover': {
            backgroundColor: ds.gray[100],
          },
        }}
      >
        <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[2] }}>
          <Typography
            sx={{
              fontFamily: ds.font.sans,
              fontWeight: 'var(--ds-font-weight-medium)',
              fontSize: 'var(--ds-text-caption)',
              color: ds.gray[700],
              lineHeight: 1.4,
            }}
          >
            {countLabel}
          </Typography>
        </Box>

        <Box sx={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
          {allowStop && onStop && (
            <Tooltip title='Stop execution' placement='top'>
              <IconButton
                size='small'
                aria-label='Stop execution'
                data-testid='running-tasks-stop'
                onClick={(e) => {
                  e.stopPropagation();
                  onStop();
                }}
                sx={{
                  p: 0.5,
                  color: ds.red[500],
                  '&:hover': { backgroundColor: ds.gray[200] },
                }}
              >
                <StopCircleOutlinedIcon sx={{ fontSize: 18 }} />
              </IconButton>
            </Tooltip>
          )}

          <IconButton
            size='small'
            aria-label={isExpanded ? 'Collapse running tasks' : 'Expand running tasks'}
            sx={{
              p: 0.5,
              color: ds.gray[500],
              '&:hover': { color: ds.gray[700] },
            }}
          >
            {isExpanded ? <KeyboardArrowUpIcon fontSize='small' /> : <KeyboardArrowDownIcon fontSize='small' />}
          </IconButton>
        </Box>
      </Box>

      {/* Task List */}
      {isExpanded && (
        <Box sx={{ display: 'flex', flexDirection: 'column' }}>
          {tasks.map((task) => (
            <Box
              key={task.id}
              data-testid={`running-task-item-${task.id}`}
              sx={{
                display: 'flex',
                alignItems: 'center',
                gap: ds.space[2],
                px: ds.space[3],
                py: '6px',
                borderTop: `1px solid ${ds.gray[200]}`,
                backgroundColor: 'var(--ds-background-100)',
              }}
            >
              <CircularProgress size={12} thickness={5} sx={{ color: ds.brand[500], flexShrink: 0 }} />
              <Typography
                sx={{
                  fontFamily: ds.font.mono || ds.font.sans,
                  fontSize: 'var(--ds-text-caption)',
                  color: ds.gray[700],
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  lineHeight: 1.4,
                }}
              >
                {task.label}
              </Typography>
            </Box>
          ))}
        </Box>
      )}
    </Box>
  );
};

export default RunningTasksBanner;
