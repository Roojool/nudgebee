import { useEffect, useRef } from 'react';

const NEAR_BOTTOM_PX = 200;

// Whether the reader is following the latest message in a chat thread. Scrolling up away
// from the bottom turns following off; scrolling back near the bottom turns it on again.
// Polls that grow the thread fire no scroll event, so they leave it alone — auto-scroll
// effects check it and stop yanking a reader who scrolled up to read older messages.
// Watches both the container and the window: full-page chat scrolls the window, popup
// mode scrolls the container (see JumpToLatestPill).
export const useFollowLatest = (scrollContainerRef) => {
  const followingRef = useRef(true);

  useEffect(() => {
    if (typeof document === 'undefined') {
      return undefined;
    }
    const lastTops = new WeakMap();
    // Capture phase: element scroll events don't bubble, and the container may mount after us.
    const onScroll = (event) => {
      const el = scrollContainerRef?.current;
      let surface;
      let top;
      let height;
      let client;
      if (el && event.target === el) {
        surface = el;
        ({ scrollTop: top, scrollHeight: height, clientHeight: client } = el);
      } else if (event.target === document) {
        surface = document;
        top = window.scrollY || document.documentElement.scrollTop || 0;
        height = document.documentElement.scrollHeight;
        client = window.innerHeight;
      } else {
        return;
      }
      const lastTop = lastTops.get(surface);
      lastTops.set(surface, top);
      if (height - top - client <= NEAR_BOTTOM_PX) {
        followingRef.current = true;
      } else if (lastTop !== undefined && top < lastTop) {
        followingRef.current = false;
      }
    };
    document.addEventListener('scroll', onScroll, { capture: true, passive: true });
    return () => document.removeEventListener('scroll', onScroll, { capture: true });
  }, [scrollContainerRef]);

  return followingRef;
};
