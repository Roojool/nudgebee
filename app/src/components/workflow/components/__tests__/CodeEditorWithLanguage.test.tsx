import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { CodeEditorWithLanguage } from '../WorkflowFieldComponents';

// CodeMirror doesn't render in jsdom; expose whether the line-wrapping extension was passed instead.
jest.mock('@uiw/react-codemirror', () => ({
  __esModule: true,
  default: ({ extensions }: { extensions: unknown[] }) => {
    const { EditorView } = jest.requireActual('@codemirror/view');
    return <div data-testid='cm' data-wrapped={String(extensions.includes(EditorView.lineWrapping))} />;
  },
}));

describe('CodeEditorWithLanguage line wrap toggle', () => {
  it('wraps long lines by default and lets the user switch wrapping off and on', () => {
    render(<CodeEditorWithLanguage value='aws ec2 describe-instances' onChange={jest.fn()} />);

    const toggle = screen.getByTestId('code-editor-wrap-toggle');
    expect(screen.getByTestId('cm')).toHaveAttribute('data-wrapped', 'true');
    expect(toggle).toHaveAttribute('aria-pressed', 'true');

    fireEvent.click(toggle);
    expect(screen.getByTestId('cm')).toHaveAttribute('data-wrapped', 'false');
    expect(toggle).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(toggle);
    expect(screen.getByTestId('cm')).toHaveAttribute('data-wrapped', 'true');
  });

  it('keeps the toggle usable when the editor is read-only', () => {
    render(<CodeEditorWithLanguage value='echo hi' onChange={jest.fn()} disabled />);

    fireEvent.click(screen.getByTestId('code-editor-wrap-toggle'));
    expect(screen.getByTestId('cm')).toHaveAttribute('data-wrapped', 'false');
  });
});
