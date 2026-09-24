import { render } from '@testing-library/react';
import LLMAnswerRenderer from '@components/llm/common/LLMAnswerRenderer';

// The agent writes "~" for "approximately". This renderer used to escape every
// tilde before handing the text to MarkDowns, which escapes lone tildes itself —
// the second escape turned "\~" into "\\~", so marked emitted a literal backslash
// and re-armed the tilde as GFM strikethrough. Answers came out with whole
// phrases struck through. Escaping now happens in exactly one place.
describe('LLMAnswerRenderer tilde handling', () => {
  it('does not strike out text between two approximate values', () => {
    const text = 'arc-systems: ~5.67 cores avg (Peak: ~40.97 cores)';
    const { container } = render(<LLMAnswerRenderer toolCall={{ text }} />);

    expect(container.innerHTML).not.toContain('<del>');
    expect(container.innerHTML).not.toContain('\\');
    expect(container.textContent).toContain('~5.67 cores avg (Peak: ~40.97 cores)');
  });

  it('still renders real double-tilde strikethrough from the agent', () => {
    const { container } = render(<LLMAnswerRenderer toolCall={{ text: '~~dropped~~ kept' }} />);
    expect(container.innerHTML).toContain('<del>dropped</del>');
  });
});
