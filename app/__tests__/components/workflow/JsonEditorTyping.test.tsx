import React, { useState } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { JsonEditor } from '@components/workflow/components/WorkflowFieldComponents';
import { resolveObjectFieldValue } from '@components/workflow/utils/fieldTypeUtils';

// CodeMirror does not lay out under jsdom, so stand in a textarea that keeps the
// same controlled contract (value down, raw text up) the real CodeEditor has.
jest.mock('@ui/CodeEditor', () => ({
  CodeEditor: ({ value, onChange }: any) => <textarea data-testid='editor' value={value} onChange={(e) => onChange(e.target.value)} />,
}));

// Mirrors how ActionDetailsSidebar / SubTaskParamForm feed an object-typed field:
// the edit lands in form state, then flows back down through resolveObjectFieldValue.
const Harness: React.FC<{ rendersRawJsonText: boolean }> = ({ rendersRawJsonText }) => {
  const [stored, setStored] = useState<any>({});
  return <JsonEditor value={resolveObjectFieldValue(stored, rendersRawJsonText)} onChange={setStored} />;
};

describe('JsonEditor round-trip through an object-typed field', () => {
  it('keeps half-typed JSON in the box', () => {
    // Regression: the parent used to coerce the unparseable text to {}, which
    // reseeded the editor and wiped the value mid-keystroke.
    render(<Harness rendersRawJsonText />);
    const editor = screen.getByTestId('editor');

    fireEvent.change(editor, { target: { value: '{"API_KEY"' } });
    expect(editor).toHaveValue('{"API_KEY"');

    fireEvent.change(editor, { target: { value: '{"API_KEY": "abc"' } });
    expect(editor).toHaveValue('{"API_KEY": "abc"');

    fireEvent.change(editor, { target: { value: '{"API_KEY": "abc"}' } });
    expect(editor).toHaveValue('{"API_KEY": "abc"}');
  });

  it('still coerces to {} for editors that need a real object', () => {
    render(<Harness rendersRawJsonText={false} />);
    const editor = screen.getByTestId('editor');

    fireEvent.change(editor, { target: { value: '{"API_KEY"' } });
    expect(editor).toHaveValue('{}');
  });
});
