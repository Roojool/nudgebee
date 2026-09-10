import { Box, Typography } from '@mui/material';
import { CodeBlock } from '@ui/CodeBlock';
import { Modal } from '@ui/Modal';
import { ds } from 'src/utils/colors';
import { buildCliCommand } from './cliCommands';
import { getResourceDisplayName } from './utils';

const CliCommandModal = ({ rec, onClose }: { rec: any; onClose: () => void }) => {
  const cli = buildCliCommand(rec);
  const resourceName = getResourceDisplayName(rec, '');

  return (
    <Modal open handleClose={onClose} width='sm' title={cli?.title ?? 'Recommendation Details'} isConfirmRequired={false} isCancelRequired={false}>
      <Typography sx={{ fontSize: 'var(--ds-text-small)', color: ds.gray[600], mb: 'var(--ds-space-2)' }}>
        {cli ? cli.intro : `Details for recommendation on ${resourceName}:`}
      </Typography>
      {cli?.destructive && (
        // One factual line and the safeguard, so the reader acts with confidence rather than being warned off.
        <Typography sx={{ fontSize: 'var(--ds-text-small)', color: ds.red[600], mb: 'var(--ds-space-2)' }} data-testid='cli-command-safeguard'>
          This permanently removes the resource. {cli.safeguard}
        </Typography>
      )}
      {!cli?.destructive && cli?.safeguard && (
        <Typography sx={{ fontSize: 'var(--ds-text-small)', color: ds.gray[600], mb: 'var(--ds-space-2)' }}>{cli.safeguard}</Typography>
      )}
      <Box>
        <CodeBlock
          code={cli ? cli.commands.join('\n') : `# Recommendation ID: ${rec.id}\n# Category: ${rec.category}\n# Rule: ${rec.rule_name}`}
          language='bash'
          tone='dark'
          wrap
          maxHeight={ds.space.mul(0, 150)}
          copyToast='Command copied'
          data-testid='cli-command-block'
        />
      </Box>
    </Modal>
  );
};

export default CliCommandModal;
