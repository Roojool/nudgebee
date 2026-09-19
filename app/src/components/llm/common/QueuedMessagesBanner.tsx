import React, { useState } from 'react';
import { Box, Typography, IconButton, CircularProgress } from '@mui/material';
import ArrowForwardIcon from '@mui/icons-material/ArrowForward';
import SafeIcon from '@shared/icons/SafeIcon';
import { DeleteIconRed, EditIcon } from '@assets';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowUpIcon from '@mui/icons-material/KeyboardArrowUp';
import ImageIcon from '@mui/icons-material/Image';
import Tooltip from '@ui/Tooltip';
import { ds } from '@utils/colors';
import { QueuedMessage } from '@hooks/useQueuedMessages';

export interface QueuedMessagesBannerProps {
  queuedMessages: QueuedMessage[];
  editingMessageId?: string;
  onRefresh?: () => void;
  onRestore?: (id: string) => void;
  isExpanded: boolean;
  maxQueuedMessages?: number;
  onToggleExpanded: () => void;
  onSendNow: (id: string) => void;
  onEdit: (id: string) => void;
  onDelete: (id: string) => Promise<void> | void;
}

export const QueuedMessagesBanner: React.FC<QueuedMessagesBannerProps> = ({
  queuedMessages,
  editingMessageId,
  onRefresh,
  onRestore,
  isExpanded,
  maxQueuedMessages,
  onToggleExpanded,
  onSendNow,
  onEdit,
  onDelete,
}) => {
  const [deletingMessageId, setDeletingMessageId] = useState<string>();
  const isDeleting = queuedMessages?.some((message) => message.id === deletingMessageId);

  if (!queuedMessages || queuedMessages.length === 0) {
    return null;
  }

  const count = queuedMessages.filter((message) => !message.saveError && !message.isForeground).length;
  const waiting = queuedMessages.filter((message) => !message.saving && !message.saveError).length;
  const saving = queuedMessages.filter((message) => message.saving).length;
  const unconfirmed = queuedMessages.filter((message) => message.saveError).length;
  const summary = [waiting ? `${waiting} waiting` : '', saving ? `${saving} saving` : '', unconfirmed ? `${unconfirmed} need attention` : '']
    .filter(Boolean)
    .join(' · ');
  const isFull = maxQueuedMessages !== undefined && count >= maxQueuedMessages;

  return (
    <Box
      data-testid='queued-messages-banner'
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
        onClick={onToggleExpanded}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onToggleExpanded();
          }
        }}
        data-testid='queued-messages-header'
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
        <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[2], minWidth: 0 }}>
          <Typography
            sx={{
              fontFamily: ds.font.sans,
              fontWeight: 'var(--ds-font-weight-medium)',
              fontSize: 'var(--ds-text-caption)',
              color: ds.gray[700],
              lineHeight: 1.4,
            }}
          >
            {summary}
          </Typography>
          <Box
            component='span'
            data-testid='queued-messages-count'
            sx={{
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              px: '6px',
              py: '1px',
              borderRadius: '999px',
              backgroundColor: isFull ? ds.amber[100] : ds.gray[200],
              color: isFull ? ds.amber[700] : ds.gray[700],
              fontSize: ds.text.caption,
              fontWeight: 'var(--ds-font-weight-medium)',
              lineHeight: 1,
            }}
          >
            {maxQueuedMessages ? `${count}/${maxQueuedMessages}` : count}
          </Box>
          <Typography
            sx={{
              fontFamily: ds.font.sans,
              fontSize: 'var(--ds-text-caption)',
              color: isFull ? ds.amber[700] : ds.gray[500],
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {isFull
              ? 'Queue full (wait for tasks to finish or delete a message)'
              : waiting
              ? 'Runs after the current response'
              : 'Waiting for confirmation'}
          </Typography>
        </Box>

        <IconButton
          size='small'
          aria-label={isExpanded ? 'Collapse queued messages' : 'Expand queued messages'}
          sx={{
            p: 0.5,
            color: ds.gray[500],
            '&:hover': { color: ds.gray[700] },
          }}
        >
          {isExpanded ? <KeyboardArrowUpIcon fontSize='small' /> : <KeyboardArrowDownIcon fontSize='small' />}
        </IconButton>
      </Box>

      {/* Message List */}
      {isExpanded && (
        <Box sx={{ display: 'flex', flexDirection: 'column' }}>
          {queuedMessages.map((msg, index) => {
            const hasImages = msg.images && msg.images.length > 0;
            return (
              <Box
                key={msg.id}
                data-testid={`queued-message-item-${msg.id}`}
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  px: ds.space[3],
                  py: '6px',
                  borderTop: `1px solid ${ds.gray[200]}`,
                  backgroundColor: 'var(--ds-background-100)',
                  '&:hover': {
                    backgroundColor: 'var(--ds-background-200)',
                  },
                }}
              >
                <Tooltip title={msg.text} placement='top-start'>
                  <Box
                    sx={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: ds.space[1],
                      flex: 1,
                      minWidth: 0,
                      mr: ds.space[2],
                    }}
                  >
                    {hasImages && (
                      <Box
                        sx={{
                          display: 'inline-flex',
                          alignItems: 'center',
                          color: ds.gray[500],
                          flexShrink: 0,
                        }}
                      >
                        <ImageIcon sx={{ fontSize: 16 }} />
                      </Box>
                    )}
                    {msg.user && msg.user !== 'You' && (
                      <Typography
                        component='span'
                        sx={{
                          fontFamily: ds.font.sans,
                          fontSize: ds.text.caption,
                          fontWeight: 'var(--ds-font-weight-medium)',
                          color: ds.gray[700],
                          backgroundColor: ds.gray[200],
                          borderRadius: ds.radius.sm,
                          px: ds.space[1],
                          py: '1px',
                          flexShrink: 0,
                          lineHeight: 1.2,
                        }}
                      >
                        {msg.user}
                      </Typography>
                    )}
                    <Typography
                      sx={{
                        fontFamily: ds.font.sans,
                        fontSize: 'var(--ds-text-body-sm)',
                        color: ds.gray[700],
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        lineHeight: 1.4,
                      }}
                    >
                      {msg.text || (hasImages ? 'Attached image' : `Message ${index + 1}`)}
                    </Typography>
                  </Box>
                </Tooltip>

                {(editingMessageId === msg.id || deletingMessageId === msg.id) && (
                  <Box role='status' sx={{ display: 'flex', alignItems: 'center', gap: 1, mr: 1 }}>
                    <CircularProgress size={14} />
                    <Typography variant='caption'>{deletingMessageId === msg.id ? 'Deleting…' : 'Moving to composer…'}</Typography>
                  </Box>
                )}
                {msg.saving && (
                  <Typography role='status' sx={{ fontSize: 'var(--ds-text-caption)' }}>
                    Saving…
                  </Typography>
                )}
                {msg.saveError && (
                  <Box>
                    <Typography role='alert'>{msg.saveError}</Typography>
                    {!msg.notAdmitted && (
                      <button type='button' onClick={onRefresh}>
                        Refresh history
                      </button>
                    )}
                    {msg.notAdmitted && (
                      <button type='button' onClick={() => onRestore?.(msg.id)}>
                        Move to composer
                      </button>
                    )}
                  </Box>
                )}
                {/* Actions: Send now, Edit, Delete */}
                <Box sx={{ display: 'flex', alignItems: 'center', gap: ds.space[0], flexShrink: 0 }}>
                  <Tooltip title='Send now' placement='top'>
                    <IconButton
                      size='small'
                      disabled={Boolean(editingMessageId) || isDeleting || msg.saving || Boolean(msg.saveError)}
                      aria-label='Send now'
                      data-testid={`queued-message-send-now-${msg.id}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        onSendNow(msg.id);
                      }}
                      sx={{
                        p: ds.space[1],
                        color: ds.gray[600],
                        '&:hover': { color: ds.brand[500], backgroundColor: ds.gray[100] },
                      }}
                    >
                      <ArrowForwardIcon sx={{ fontSize: 16 }} />
                    </IconButton>
                  </Tooltip>

                  <Tooltip title='Edit message' placement='top'>
                    <IconButton
                      size='small'
                      disabled={Boolean(editingMessageId) || isDeleting || msg.saving || Boolean(msg.saveError)}
                      aria-label='Edit message'
                      data-testid={`queued-message-edit-${msg.id}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        onEdit(msg.id);
                      }}
                      sx={{
                        p: ds.space[1],
                        color: ds.gray[600],
                        '&:hover': { color: ds.gray[700], backgroundColor: ds.gray[100] },
                      }}
                    >
                      <SafeIcon src={EditIcon} alt='edit' width={16} height={16} />
                    </IconButton>
                  </Tooltip>

                  <Tooltip title='Delete message' placement='top'>
                    <IconButton
                      size='small'
                      disabled={Boolean(editingMessageId) || isDeleting || msg.saving || Boolean(msg.saveError)}
                      aria-label='Delete message'
                      data-testid={`queued-message-delete-${msg.id}`}
                      onClick={async (e) => {
                        e.stopPropagation();
                        setDeletingMessageId(msg.id);
                        try {
                          await onDelete(msg.id);
                        } finally {
                          setDeletingMessageId(undefined);
                        }
                      }}
                      sx={{
                        p: ds.space[1],
                        color: ds.gray[500],
                        '&:hover': { color: ds.red[500] || '#EF4444', backgroundColor: ds.gray[100] },
                      }}
                    >
                      <SafeIcon src={DeleteIconRed} alt='delete' width={16} height={16} />
                    </IconButton>
                  </Tooltip>
                </Box>
              </Box>
            );
          })}
        </Box>
      )}
    </Box>
  );
};

export default QueuedMessagesBanner;
