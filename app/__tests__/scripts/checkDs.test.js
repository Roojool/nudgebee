const path = require('path');
const { findRestrictedImports, newRestrictedImports, newTokenViolations, resolveModule, isChecked } = require('../../scripts/check-ds');
const { findTokenViolations, loadTokens, loadAccessors, normalizeColor } = require('../../scripts/ds-tokens');

// The real token table, so a token renamed in CSS breaks these tests rather
// than silently un-enforcing itself.
const COLORS_TS = path.join(__dirname, '../../src/utils/colors.ts');
const tokens = loadTokens(path.join(__dirname, '../../src/styles/theme-tokens.css'), COLORS_TS);
const found = (src, file = 'src/x.tsx') => findTokenViolations(src, file, tokens).map((h) => `${h.value}->${h.token}`);

const keys = (hits) => hits.map((h) => h.key);

describe('check-ds', () => {
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
        "import CustomStepper from '../common/navigation/CustomStepper';",
        "export { default as Field } from '@shared/forms/CustomTextField';",
        "const Nav = dynamic(() => import('@components/common/navigation/VerticalStepNavigation'));",
      ].join('\n');
      expect(keys(findRestrictedImports(src, 'src/components/k8s/Page.tsx'))).toEqual([
        'legacy:CustomStepper',
        'legacy:CustomTextField',
        'legacy:VerticalStepNavigation',
      ]);
    });

    it('does not flag MUI Paper — its uses are floating surfaces, not ds/Card containers', () => {
      const src = "import { Paper, Popper, Card } from '@mui/material';";
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Card']);
    });

    it('allows MUI TextField when the file uses MUI Autocomplete', () => {
      const src = "import { Autocomplete, TextField, Chip } from '@mui/material';";
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual(['@mui/material:Chip']);
    });

    it('flags MUI edit/delete icons, the superseded @assets exports and react-icons pencils', () => {
      const src = [
        "import DeleteOutlineIcon from '@mui/icons-material/DeleteOutline';",
        "import { Edit as EditIcon, DragIndicator, Close } from '@mui/icons-material';",
        "import { writeIconLight, K8sIcon } from '@assets';",
        "import { LiaEditSolid } from 'react-icons/lia';",
      ].join('\n');
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual([
        '@mui/icons-material:DeleteOutline',
        '@mui/icons-material:Edit',
        '@assets:writeIconLight',
        'react-icons:LiaEditSolid',
      ]);
    });

    it('leaves icons that are not edit or delete alone', () => {
      const src = [
        "import { MoreVert, Close, Add, DragIndicator } from '@mui/icons-material';",
        "import CheckIcon from '@mui/icons-material/Check';",
      ].join('\n');
      expect(findRestrictedImports(src, 'src/x.tsx')).toEqual([]);
    });

    it('flags the MUI theme variants of those icons', () => {
      const src = [
        "import { DeleteRounded, EditTwoTone, ModeEditOutlineSharp, DeleteOutlineRounded } from '@mui/icons-material';",
        "import DeleteTwoToneIcon from '@mui/icons-material/DeleteTwoTone';",
      ].join('\n');
      expect(keys(findRestrictedImports(src, 'src/x.tsx'))).toEqual([
        '@mui/icons-material:DeleteRounded',
        '@mui/icons-material:EditTwoTone',
        '@mui/icons-material:ModeEditOutlineSharp',
        '@mui/icons-material:DeleteOutlineRounded',
        '@mui/icons-material:DeleteTwoTone',
      ]);
    });

    it('leaves similarly named icons that mean something else alone', () => {
      const src =
        "import { DeleteSweep, EditCalendar, EditLocationAlt, EditNotifications, EditOff, EditRoad, EditAttributes } from '@mui/icons-material';";
      expect(findRestrictedImports(src, 'src/x.tsx')).toEqual([]);
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
      expect(keys(newRestrictedImports(null, "import { Chip } from '@mui/material';", 'src/new.tsx'))).toEqual(['@mui/material:Chip']);
    });

    it('does not flag an import that moved between subpath and named form', () => {
      const base = "import Tooltip from '@mui/material/Tooltip';";
      const head = "import { Tooltip } from '@mui/material';";
      expect(newRestrictedImports(base, head, 'src/x.tsx')).toEqual([]);
    });
  });

  describe('resolveModule', () => {
    it('maps aliases and relative specifiers to src-relative paths', () => {
      expect(resolveModule('@ui/Card', 'src/pages/a.tsx')).toBe('components/common/ds/Card');
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

  describe('token rules', () => {
    it('reads each `ds` accessor out of colors.ts rather than guessing it', () => {
      const accessors = loadAccessors(COLORS_TS);
      expect(accessors.get('--ds-gray-alpha-300')).toBe('ds.gray.alpha[300]');
      expect(accessors.get('--ds-text-body-lg')).toBe('ds.text.bodyLg');
      expect(accessors.get('--ds-space-3')).toBe('ds.space[3]');
      expect(accessors.get('--ds-font-weight-medium')).toBe('ds.weight.medium');
    });

    it('never suggests a token that `ds` does not export', () => {
      // theme-tokens.css defines --ds-slate-600, but `ds` has no `slate` key, so
      // `ds.slate[600]` would throw at render — a "fix" worse than the hardcoded
      // colour. Every accessor offered must resolve on the real exported object.
      const { ds } = require('@utils/colors');
      expect(loadAccessors(COLORS_TS).has('--ds-slate-600')).toBe(false);
      expect(tokens.colors.has('#475569')).toBe(false);

      const resolve = (accessor) =>
        accessor
          .replace(/^ds/, '')
          .split(/[.[\]]/)
          .filter(Boolean)
          .reduce((value, key) => (value == null ? undefined : value[key]), ds);
      for (const table of [tokens.colors, tokens.space, tokens.radius, tokens.fontSize, tokens.fontWeight]) {
        for (const accessor of table.values()) {
          expect(`${accessor} = ${resolve(accessor)}`).toMatch(/= var\(--ds-/);
        }
      }
    });

    it('normalises colours so shorthand and spacing differences still match', () => {
      expect(normalizeColor('#FFF')).toBe(normalizeColor('#ffffff'));
      expect(normalizeColor('rgba( 0 , 0,0 , .1 )')).toBe('rgba(0,0,0,0.1)');
      // A fully opaque rgba is the same colour as its 3-arg form.
      expect(normalizeColor('rgba(0,0,0,1)')).toBe(normalizeColor('rgb(0, 0, 0)'));
    });

    it('flags px values that land exactly on a token scale', () => {
      const src = "const a = <Box sx={{ padding: '16px', borderRadius: '8px', fontSize: '11px' }} />;";
      expect(found(src)).toEqual(['16px->ds.space[4]', '8px->ds.radius.lg', '11px->ds.text.caption']);
    });

    it('flags MUI numeric spacing units via the 8px scale, and font weights', () => {
      const src = 'const a = <Box sx={{ mt: 2, gap: 1, py: 0.25, fontWeight: 600 }} />;';
      expect(found(src)).toEqual(['2 (16px)->ds.space[4]', '1 (8px)->ds.space[2]', '0.25 (2px)->ds.space[0]', '600->ds.weight.semibold']);
    });

    it('flags colours in any colour-valued property, including several in one value', () => {
      const src = "const a = <Box sx={{ color: '#6f6f6f', boxShadow: '0 1px 2px rgba(0,0,0,0.1), 0 0 0 1px #e3e3e3' }} />;";
      expect(found(src)).toEqual(['#6f6f6f->ds.gray[600]', 'rgba(0,0,0,0.1)->ds.gray.alpha[300]', '#e3e3e3->ds.gray[300]']);
    });

    it('flags a raw px value passed as a JSX prop, as design-system.md’s own Modal recipe once did', () => {
      expect(found("const a = <Stack gap='12px' />;")).toEqual(['12px->ds.space[3]']);
    });

    it('flags MUI style shorthands passed as bare JSX attributes, not only inside sx', () => {
      // extendSxProp folds any defaultSxConfig key into sx, so <Box mt='24px'>
      // renders exactly like <Box sx={{ mt: '24px' }}> and must be judged alike.
      const src = "const a = <Box mt='24px' p={2} pl='16px' bgcolor='#6f6f6f' />;";
      expect(found(src)).toEqual(['24px->ds.space[5]', '2 (16px)->ds.space[4]', '16px->ds.space[4]', '#6f6f6f->ds.gray[600]']);
    });

    it('reads SCREAMING_SNAKE style constants, which the repo really uses', () => {
      // DigestsTab's BODY_SX, ecs/Instances' DETAIL_GRID_SX.
      const src = "const BODY_SX = { padding: '16px', color: '#6f6f6f' };";
      expect(found(src)).toEqual(['16px->ds.space[4]', '#6f6f6f->ds.gray[600]']);
    });

    it('reads a style object returned from a style-named helper', () => {
      const src = "function getRowStyles() {\n  return { padding: '16px' };\n}";
      expect(found(src)).toEqual(['16px->ds.space[4]']);
    });

    it('handles negative spacing with the calc() helper, not a negated token', () => {
      // `-ds.space[1]` would be `-'var(--ds-space-1)'` → NaN. §1.8's mul() is the form.
      expect(found('const a = <Typography sx={{ mt: -0.5 }} />;')).toEqual(['-0.5 (-4px)->ds.space.mul(1, -1)']);
      expect(found('const a = <Box sx={{ mt: 0 }} />;')).toEqual([]);
    });

    it('handles multi-value shorthands, but only when every part has a token', () => {
      expect(found("const a = <Box sx={{ padding: '8px 16px' }} />;")).toEqual(['8px 16px->`${ds.space[2]} ${ds.space[4]}`']);
      // 6px is off-scale, so half a suggestion would read as "drop the other value".
      expect(found("const a = <Box sx={{ margin: '2px 6px' }} />;")).toEqual([]);
    });

    it('leaves values alone that have no token, and box dimensions that have no scale', () => {
      const src = [
        "const a = <Box sx={{ padding: '13px', color: '#6f6f70', gap: 0.4 }} />;",
        "const b = <Box sx={{ minWidth: '140px', maxHeight: '600px', width: '32px' }} />;",
      ].join('\n');
      expect(found(src)).toEqual([]);
    });

    it('does not read MUI position props as spacing — `top: 2` renders 2px, not 16px', () => {
      expect(found('const a = <Box sx={{ top: 2, left: 1, bottom: 3 }} />;')).toEqual([]);
    });

    it('ignores object literals that are not styles, and icon font sizes', () => {
      const src = [
        "const payload = { color: '#6f6f6f', padding: '16px' };",
        "const a = <SettingsIcon sx={{ fontSize: '16px' }} />;",
        "const b = <Chart colors={['#6f6f6f']} />;",
      ].join('\n');
      expect(found(src)).toEqual([]);
    });

    it('reads styles out of named style objects and nested sx selectors', () => {
      const src = ["const panelSx = { padding: '24px' };", "const a = <Box sx={{ '& p': { mb: 0.5 } }} />;"].join('\n');
      expect(found(src)).toEqual(['24px->ds.space[5]', '0.5 (4px)->ds.space[1]']);
    });

    it('exempts chart files, which need more colour stops than the palette defines', () => {
      const src = "const a = <Box sx={{ color: '#6f6f6f' }} />;";
      expect(found(src, 'src/components/charts/Donut.tsx')).toEqual([]);
      expect(found(src, 'src/components/k8s/UsageHeatmap.tsx')).toEqual([]);
    });

    describe('newTokenViolations', () => {
      const wrap = (v) => `const a = <Box sx={{ padding: '${v}' }} />;`;

      it('reports nothing when the file already had the value', () => {
        expect(newTokenViolations(wrap('16px'), wrap('16px'), 'src/x.tsx', tokens)).toEqual([]);
      });

      it('counts values as a multiset, so three becoming four reports one', () => {
        const base = "const a = <Box sx={{ p: '16px', m: '16px', gap: '16px' }} />;";
        const head = "const a = <Box sx={{ p: '16px', m: '16px', gap: '16px', rowGap: '16px' }} />;";
        expect(newTokenViolations(base, head, 'src/x.tsx', tokens)).toHaveLength(1);
      });

      it('reports every value in a brand new file', () => {
        expect(newTokenViolations(null, wrap('16px'), 'src/x.tsx', tokens)).toHaveLength(1);
      });

      it('honours a ds-allow marker on the line or the comment line above', () => {
        const head = [
          '// ds-allow: matches the vendor widget it sits inside',
          "const a = <Box sx={{ padding: '16px' }} />;",
          "const b = <Box sx={{ margin: '24px' }} />; // ds-allow: aligns with the iframe",
        ].join('\n');
        expect(newTokenViolations(null, head, 'src/x.tsx', tokens)).toEqual([]);
      });
    });
  });
});
