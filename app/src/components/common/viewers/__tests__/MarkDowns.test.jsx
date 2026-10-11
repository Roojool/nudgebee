import { render } from '@testing-library/react';
import MarkDowns from '@shared/viewers/MarkDowns';

// A lone tilde means "approximately" in LLM answers; marked's GFM strikethrough
// also fires on a single tilde pair, which struck out everything between two
// approximate values. The guard used to be a source-text escape applied in two
// places at once, and the double escape rendered a stray backslash plus a live
// strikethrough. These tests pin the tokenizer rule that replaced it.
describe('MarkDowns tilde handling', () => {
  const html = (data) => render(<MarkDowns data={data} />).container.innerHTML;

  it('keeps lone tildes literal instead of striking the text between them', () => {
    const out = html('Namespace CPU: ~5.67 cores avg (Peak: ~40.97 cores)');
    expect(out).not.toContain('<del>');
    expect(out).not.toContain('\\');
    expect(out).toContain('~5.67 cores avg (Peak: ~40.97 cores)');
  });

  it('keeps a tilde pair inside one word literal', () => {
    const out = html('path /tmp/~foo~bar');
    expect(out).not.toContain('<del>');
    expect(out).toContain('/tmp/~foo~bar');
  });

  it('still renders real double-tilde strikethrough', () => {
    expect(html('~~removed~~ kept')).toContain('<del>removed</del>');
  });

  it('leaves tildes in code spans alone', () => {
    const out = html('run `kubectl ~a~ b`');
    expect(out).not.toContain('<del>');
    expect(out).toContain('~a~');
  });

  it('renders an already-escaped tilde as a single tilde', () => {
    const out = html('approx \\~5.67 cores');
    expect(out).toContain('~5.67 cores');
    expect(out).not.toContain('\\~');
  });
});
