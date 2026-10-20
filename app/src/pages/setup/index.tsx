/**
 * `/setup` — the Setup Center's own page.
 *
 * The card used to render inside the home page, with a `hideWidgets` flag
 * suppressing the real dashboard around it. That made one page mean two things,
 * required every home widget to know about onboarding, and forced the card to
 * hide itself once setup was complete — which is what left a gated user with no
 * Finish button on screen and no way out.
 *
 * On its own route none of that is needed. The card always renders here, because
 * you only arrive deliberately: while the setup gate is locked every other route
 * redirects here. Nothing links here afterwards — the Home progress banner
 * points at the admin Overview's Connections view instead.
 */
import * as React from 'react';
import { Box } from '@mui/material';
import SetupCenter from '@components/onboarding/SetupCenter';

const SetupPage = () => (
  <Box
    data-testid='setup-page'
    sx={{
      // Matches the reading width the card was given inside the home grid, so
      // moving it to its own route does not change how it looks.
      maxWidth: 1180,
      mx: 'auto',
      px: 'var(--ds-space-4)',
      pt: 'calc(var(--ds-space-0) * 14)',
      pb: 'var(--ds-space-6)',
    }}
  >
    <SetupCenter />
  </Box>
);

export default SetupPage;
