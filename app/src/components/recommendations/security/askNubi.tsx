/**
 * "Ask Nubi" for security findings — the same entry point the Cost and
 * Configuration rows have, so every Security listing on Optimise offers it too.
 *
 * The chat opens with the finding described in the prompt and a session keyed
 * on the finding, exactly as OptimizeNewPage does for a recommendation.
 */
import { useCallback } from 'react';
import { Button } from '@ui/Button';
import Tooltip from '@ui/Tooltip';
import SafeIcon from '@shared/icons/SafeIcon';
import { getNubiIconUrl } from '@hooks/useTenantBranding';
import { useNubiGlobalChat } from '@context/NubiGlobalChatContext';
import { buildNubiSecurityPrompt, type SecurityFindingPromptContext } from '@utils/nubiPromptBuilder';
import { type SecurityFinding } from './securityFinding';

export interface AskNubiTarget {
  accountId: string;
  /** Distinguishes the chat session; a recommendation id where one exists. */
  sessionKey: string;
  context: SecurityFindingPromptContext;
}

export const useAskNubiAboutSecurityFinding = () => {
  const { openWithContext } = useNubiGlobalChat();
  return useCallback(
    ({ accountId, sessionKey, context }: AskNubiTarget) => {
      openWithContext({
        accountId: accountId || '',
        sessionId: `recom_${sessionKey}`,
        query: buildNubiSecurityPrompt(context),
        categorySource: 'Optimize',
      });
    },
    [openWithContext]
  );
};

/** Prompt context for a normalized CVE finding (image scan or VM). */
export const findingPromptContext = (finding: SecurityFinding, accountName?: string): SecurityFindingPromptContext => ({
  kind: 'CVE',
  id: finding.vulnId || finding.packageName || finding.id,
  title: finding.title,
  severity: finding.severity,
  resource: finding.source === 'vm' ? finding.resourceName : [finding.image, finding.workloadName].filter(Boolean).join(' · ') || undefined,
  namespace: finding.namespace,
  packageName: finding.packageName,
  installedVersion: finding.installedVersion,
  fixedVersion: finding.fixedVersion,
  fixState: finding.fixState,
  cvssScore: finding.cvssScore,
  kev: finding.kev,
  accountName,
  description: finding.description,
});

interface AskNubiIconButtonProps {
  id: string;
  assistantName?: string;
  onClick: () => void;
}

/** The row-level icon button, matching the Cost tab's RowActions. */
export const AskNubiIconButton = ({ id, assistantName, onClick }: AskNubiIconButtonProps) => {
  const label = `Ask ${assistantName || 'Nubi'}`;
  return (
    <Tooltip title={label} placement='top'>
      <span>
        <Button
          tone='ghost'
          size='xs'
          composition='icon-only'
          icon={<SafeIcon src={getNubiIconUrl()} alt='' width={16} height={16} />}
          aria-label={label}
          id={id}
          onClick={(e) => {
            e.stopPropagation();
            onClick();
          }}
        />
      </span>
    </Tooltip>
  );
};
