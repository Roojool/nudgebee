import React from 'react';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import AutoSuggestTextarea from '@components/k8s/common/TextAreaV2';

it('restores an edited message even when its text equals the previous submitted value', async () => {
  const props = {
    onDraftSubmitted: jest.fn(),
    value: 'same message',
    suggestionsAt: [],
    placeholder: 'Ask',
    maxRows: 8,
    maxLength: 1000,
    onKeyDown: jest.fn(),
    fontSize: '14px',
    fontWeight: '400',
    onClick: jest.fn(),
    buttonProperties: { show: true, enable: true, onClick: jest.fn().mockResolvedValue(true), onClickStop: jest.fn() },
  };
  const { rerender } = render(<AutoSuggestTextarea {...props} />);
  expect(screen.getByPlaceholderText('Ask')).toHaveValue('same message');
  fireEvent.click(screen.getByRole('button', { name: 'Send' }));
  await waitFor(() => expect(screen.getByPlaceholderText('Ask')).toHaveValue(''));
  rerender(<AutoSuggestTextarea {...props} imageDraft={{ id: 'edited-message', text: 'same message', images: [] }} />);
  expect(screen.getByPlaceholderText('Ask')).toHaveValue('same message');
  fireEvent.click(screen.getByRole('button', { name: 'Send' }));
  await waitFor(() => expect(props.onDraftSubmitted).toHaveBeenCalledWith('edited-message'));
});

it.each([false, true])('keeps Stop available without clearing a draft (chat screen: %s)', (chatScreen) => {
  const onClickStop = jest.fn();
  render(
    <AutoSuggestTextarea
      value='keep this draft'
      suggestionsAt={[]}
      placeholder='Ask'
      maxRows={8}
      maxLength={1000}
      onKeyDown={jest.fn()}
      fontSize='14px'
      fontWeight='400'
      onClick={jest.fn()}
      chatScreen={chatScreen}
      isFollowUp
      allowStop
      buttonProperties={{ show: true, enable: true, onClick: jest.fn(), onClickStop }}
    />
  );
  expect(screen.getByRole('button', { name: 'Send' })).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
  expect(onClickStop).toHaveBeenCalledTimes(1);
  expect(screen.getByPlaceholderText('Ask')).toHaveValue('keep this draft');
});

it('reports a populated draft so restoring another message can preserve it', () => {
  const onDraftChange = jest.fn();
  render(
    <AutoSuggestTextarea
      value=''
      suggestionsAt={[]}
      placeholder='Ask'
      maxRows={8}
      maxLength={1000}
      onKeyDown={jest.fn()}
      fontSize='14px'
      fontWeight='400'
      onClick={jest.fn()}
      onDraftChange={onDraftChange}
      buttonProperties={{ show: true, enable: true, onClick: jest.fn() }}
    />
  );
  expect(onDraftChange).toHaveBeenLastCalledWith(false);
  fireEvent.change(screen.getByPlaceholderText('Ask'), { target: { value: 'new draft' } });
  expect(onDraftChange).toHaveBeenLastCalledWith(true);
});
