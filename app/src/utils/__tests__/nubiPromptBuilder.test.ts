import { buildNubiSecurityPrompt } from '../nubiPromptBuilder';

describe('buildNubiSecurityPrompt', () => {
  it('describes a CVE with its package, fix and location', () => {
    const prompt = buildNubiSecurityPrompt({
      kind: 'CVE',
      id: 'CVE-2024-1234',
      title: 'Heap overflow in libfoo',
      severity: 'High',
      resource: 'nginx:1.25 · web-frontend',
      namespace: 'shop',
      packageName: 'libfoo',
      installedVersion: '1.2.3',
      fixedVersion: '1.2.4',
      cvssScore: 8.1,
      kev: true,
      accountName: 'prod-eks',
      description: 'A crafted request overflows the heap.',
    });

    expect(prompt).toContain('## Analyze Security Finding');
    expect(prompt).toContain('**CVE:** CVE-2024-1234');
    expect(prompt).toContain('**Title:** Heap overflow in libfoo');
    expect(prompt).toContain('**Resource:** nginx:1.25 · web-frontend');
    expect(prompt).toContain('**Namespace:** `shop`');
    expect(prompt).toContain('**Package:** libfoo 1.2.3');
    expect(prompt).toContain('**Fixed in:** 1.2.4');
    expect(prompt).toContain('**CVSS:** 8.1');
    expect(prompt).toContain('**Known exploited:** yes (CISA KEV)');
    expect(prompt).toContain('**Account:** prod-eks');
    expect(prompt).toContain('### Summary\nA crafted request overflows the heap.');
    expect(prompt).toContain('safest way to remediate it');
  });

  it('reports the fix state when there is no fixed version', () => {
    const prompt = buildNubiSecurityPrompt({ kind: 'CVE', id: 'CVE-2024-9', severity: 'Low', packageName: 'bar', fixState: 'will_not_fix' });
    expect(prompt).toContain('**Fix status:** will_not_fix');
    expect(prompt).not.toContain('**Fixed in:**');
  });

  it('describes a grouped rule by its failing count and a posture check by its reason', () => {
    const cis = buildNubiSecurityPrompt({
      kind: 'CIS benchmark rule',
      id: '5.2.1',
      title: 'Minimize privileged containers',
      severity: 'Medium',
      failingCount: 7,
    });
    expect(cis).toContain('**CIS benchmark rule:** 5.2.1');
    expect(cis).toContain('**Failing resources:** 7');
    expect(cis).not.toContain('### Summary');

    const cloud = buildNubiSecurityPrompt({
      kind: 'Cloud posture check',
      id: 's3_bucket_public_access',
      severity: 'Critical',
      resource: 'my-bucket',
      provider: 'AWS',
      reason: 'Bucket policy allows public read',
    });
    expect(cloud).toContain('**Provider:** AWS');
    expect(cloud).toContain('### Summary\nReason: Bucket policy allows public read');
  });
});
