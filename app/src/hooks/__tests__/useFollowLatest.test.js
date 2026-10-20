import { renderHook } from '@testing-library/react';
import { useFollowLatest } from '../useFollowLatest';

const makeContainer = () => {
  const el = document.createElement('div');
  document.body.appendChild(el);
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: 2000 });
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: 500 });
  return el;
};

const scrollTo = (el, top) => {
  el.scrollTop = top;
  el.dispatchEvent(new Event('scroll'));
};

describe('useFollowLatest', () => {
  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('follows by default', () => {
    const el = makeContainer();
    const { result } = renderHook(() => useFollowLatest({ current: el }));
    expect(result.current.current).toBe(true);
  });

  it('stops following when the reader scrolls up away from the bottom', () => {
    const el = makeContainer();
    const { result } = renderHook(() => useFollowLatest({ current: el }));
    scrollTo(el, 1500);
    scrollTo(el, 600);
    expect(result.current.current).toBe(false);
  });

  it('resumes following once the reader returns near the bottom', () => {
    const el = makeContainer();
    const { result } = renderHook(() => useFollowLatest({ current: el }));
    scrollTo(el, 1500);
    scrollTo(el, 600);
    scrollTo(el, 1400);
    expect(result.current.current).toBe(true);
  });

  it('keeps following while scrolling down toward the bottom', () => {
    const el = makeContainer();
    const { result } = renderHook(() => useFollowLatest({ current: el }));
    scrollTo(el, 100);
    scrollTo(el, 400);
    expect(result.current.current).toBe(true);
  });

  it('ignores scrolls of unrelated elements', () => {
    const el = makeContainer();
    const other = makeContainer();
    const { result } = renderHook(() => useFollowLatest({ current: el }));
    scrollTo(other, 1500);
    scrollTo(other, 0);
    expect(result.current.current).toBe(true);
  });
});
