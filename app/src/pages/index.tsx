import * as React from 'react';
import { useRouter } from 'next/router';

// Was pushing during render, unguarded — every visit to `/` painted `/home`
// for a frame before the setup gate (also evaluating at that instant) could
// object and correct it to `/setup`. The destination was always right; the
// paint was not. Routing belongs in an effect, and the target should be the
// gate's own default rather than a second opinion this page has to keep in
// sync with it.
function RedirectPage(): null {
  const router = useRouter();
  React.useEffect(() => {
    router.replace('/home');
  }, [router]);
  return null;
}

export default RedirectPage;
