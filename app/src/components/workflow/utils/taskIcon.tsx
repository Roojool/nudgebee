import React from 'react';
import SafeIcon from '@shared/icons/SafeIcon';

// Category / task icon as the Actions tab renders it: emoji icons (no path
// characters) render as text, everything else as an image.
export const renderCategoryOrTaskIcon = (icon: any, label: string, size: number) => {
  if (typeof icon === 'string' && !icon.includes('/') && !icon.includes('.')) {
    return <span style={{ fontSize: size >= 24 ? 'var(--ds-text-heading)' : 'var(--ds-text-title)' }}>{icon}</span>;
  }
  return <SafeIcon src={icon} alt={label} width={size} height={size} style={{ objectFit: 'contain' }} />;
};
