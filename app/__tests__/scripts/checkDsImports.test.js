const { findRestrictedImports, newRestrictedImports, resolveModule, isChecked } = require('../../scripts/check-ds-imports');

const keys = (hits) => hits.map((h) => h.key);

describe('check-ds-imports', () => {
  describe('findRestrictedImports', () => {
    it('flags MUI named and subpath imports that have a DS replacement', () => {
      const src = ["import { Box, Divider, Typography } from '@mui/material';", "import Alert from '@mui/material/Alert';"].join('\n');
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Divider', '@mui/material:Alert']);
    });

    it('ignores layout primitives and components without a DS equivalent', () => {
      const src = "import { Box, Stack, Grid, Typography, CircularProgress, Popover, List, Tabs } from '@mui/material';";
      expect(findRestrictedImports(src, 'src/x.tsx')).toEqual([]);
    });

    it('reads multi-line imports and aliased names', () => {
      const src = "import {\n  Box,\n  Switch as MuiSwitch,\n} from '@mui/material';";
      const hits = findRestrictedImports(src, 'src/x.jsx');
      expect(keys(hits)).toEqual(['@mui/material:Switch']);
      expect(hits[0].line).toBe(3);
    });

    it('skips type-only imports', () => {
      const src = "import type { ButtonProps } from '@mui/material';\nimport { type Button } from '@mui/material';";
      expect(findRestrictedImports(src, 'src/x.ts')).toEqual([]);
    });

    it('flags legacy modules through aliases, relative paths, re-exports and dynamic imports', () => {
      const src = [
        "import WidgetCard from '@ui/WidgetCard';",
        "import CustomStepper from '../common/navigation/CustomStepper';",
        "export { default as Field } from '@shared/forms/CustomTextField';",
        "const Nav = dynamic(() => import('@components/common/navigation/VerticalStepNavigation'));",
      ].join('\n');
      expect(keys(findRestrictedImports(src, 'src/components/k8s/Page.tsx'))).toEqual([
        'legacy:WidgetCard',
        'legacy:CustomStepper',
        'legacy:CustomTextField',
        'legacy:VerticalStepNavigation',
      ]);
    });

    it('allows MUI TextField when the file uses MUI Autocomplete', () => {
      const src = "import { Autocomplete, TextField, Chip } from '@mui/material';";
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Chip']);
    });

    it('honours the ds-imports-allow marker on or above the import', () => {
      const src = [
        '// ds-imports-allow: ds/Modal cannot host a fullScreen drawer',
        "import { Dialog } from '@mui/material';",
        "import { Menu } from '@mui/material'; // ds-imports-allow: nested submenu",
      ].join('\n');
      expect(findRestrictedImports(src, 'src/x.tsx')).toEqual([]);
    });

    it('does not let one allow marker cover a second restricted import on the same line', () => {
      const src = "import { Dialog, Menu } from '@mui/material'; // ds-imports-allow: Dialog needs fullscreen";
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Dialog', '@mui/material:Menu']);
    });

    it('scopes a marker to the specifier line inside a multi-line import', () => {
      const src = "import {\n  // ds-imports-allow: Dialog needs fullscreen\n  Dialog,\n  Menu,\n} from '@mui/material';";
      const hits = findRestrictedImports(src, 'src/x.tsx');
      expect(keys(hits)).toEqual(['@mui/material:Menu']);
      expect(hits[0].line).toBe(4);
    });

    it('does not let an inline marker on one line exempt the next line', () => {
      const src = "import {\n  Dialog, // ds-imports-allow: Dialog needs fullscreen\n  Menu,\n} from '@mui/material';";
      const hits = findRestrictedImports(src, 'src/x.tsx');
      expect(keys(hits)).toEqual(['@mui/material:Menu']);
      expect(hits[0].line).toBe(3);
    });

    it('does not accept a marker without a reason', () => {
      const src = "// ds-imports-allow:\nimport { Dialog } from '@mui/material';";
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Dialog']);
    });
  });

  describe('newRestrictedImports', () => {
    it('reports only imports the base version did not have', () => {
      const base = "import { Divider } from '@mui/material';";
      const head = "import { Divider, Alert } from '@mui/material';";
      expect(keys(newRestrictedImports(base, head, 'src/x.tsx'))).toEqual(['@mui/material:Alert']);
    });

    it('catches a regression back to a raw MUI component', () => {
      const base = "import { Divider } from '@ui/Divider';";
      const head = "import { Divider } from '@mui/material';";
      expect(keys(newRestrictedImports(base, head, 'src/components/optimise-new/DetailsPanel.tsx'))).toEqual(['@mui/material:Divider']);
    });

    it('treats every restricted import in a new file as new', () => {
      expect(keys(newRestrictedImports(null, "import { Paper } from '@mui/material';", 'src/new.tsx'))).toEqual(['@mui/material:Paper']);
    });

    it('does not flag an import that moved between subpath and named form', () => {
      const base = "import Tooltip from '@mui/material/Tooltip';";
      const head = "import { Tooltip } from '@mui/material';";
      expect(newRestrictedImports(base, head, 'src/x.tsx')).toEqual([]);
    });
  });

  describe('resolveModule', () => {
    it('maps aliases and relative specifiers to src-relative paths', () => {
      expect(resolveModule('@ui/WidgetCard', 'src/pages/a.tsx')).toBe('components/common/ds/WidgetCard');
      expect(resolveModule('./navigation/CustomStepper.tsx', 'src/components/common/b.tsx')).toBe('components/common/navigation/CustomStepper');
      expect(resolveModule('react', 'src/pages/a.tsx')).toBeNull();
    });
  });

  describe('isChecked', () => {
    it('excludes ds primitives, sanctioned compositions, tests and non-source files', () => {
      expect(isChecked('src/pages/investigate.jsx')).toBe(true);
      expect(isChecked('src/components/common/ds/Button.tsx')).toBe(false);
      expect(isChecked('src/components/common/tables/CustomTable.jsx')).toBe(false);
      expect(isChecked('src/components/common/ds/Link.test.tsx')).toBe(false);
      expect(isChecked('src/lib/__tests__/x.test.ts')).toBe(false);
      expect(isChecked('src/styles/theme.css')).toBe(false);
    });
  });
});
