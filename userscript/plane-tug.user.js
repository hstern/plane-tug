// ==UserScript==
// @name         plane-tug
// @namespace    https://github.com/hstern/plane-tug
// @version      0.1.0
// @description  Push-driven refresh for Plane list views. Replaces a timer with an SSE stream from a plane-tug bridge.
// @match        https://plane.example.com/*
// @run-at       document-idle
// @grant        none
// @homepageURL  https://github.com/hstern/plane-tug
// @supportURL   https://github.com/hstern/plane-tug/issues
// ==/UserScript==

// Installation:
//
//   1. Replace the @match line above with your Plane host
//      (e.g. https://plane.your-domain.example/*).
//   2. Install in Tampermonkey / Violentmonkey / Greasemonkey.
//   3. Deploy a plane-tug bridge same-origin at /live/* so this
//      script can fetch /live/events?project=<uuid> with the user's
//      Plane session cookie automatically attached.
//
// The script holds the same scroll-preserve, busy-check, and
// refreshable-path filtering that a timer-based reloader would —
// the only structural change is swapping `setInterval` for an
// `EventSource`, with the timer surviving as a fallback when the
// stream is unreachable.

(function () {
  'use strict';

  // ---- Configuration -------------------------------------------------------

  // Path under the current origin where the plane-tug bridge is exposed.
  // /live/events?project=<uuid> must be reachable; the operator's reverse
  // proxy is expected to route this through with session cookies attached.
  const SSE_PATH = '/live/events';

  // Fallback poll interval used when the SSE stream is unreachable. Matches
  // the conservative 45s timer of the legacy reload-on-a-clock approach.
  const FALLBACK_INTERVAL_MS = 45_000;

  // After this many consecutive EventSource errors without a successful
  // message, fall back to the timer until the next successful reconnect.
  const ERROR_THRESHOLD = 3;

  // Debounce window: SSE events arriving in a tight burst (e.g. a bulk
  // edit) coalesce into a single reload. 500 ms is short enough that the
  // user perceives the update as immediate.
  const DEBOUNCE_MS = 500;

  const SCROLL_KEY = 'plane-tug:scroll';

  // Refreshable-path predicates. List views and "your work" views benefit
  // from auto-refresh; settings, profile, and page editors do not.
  const REFRESH_PATHS = [
    /\/[^/]+\/projects\/[^/]+\/(issues|cycles|modules|views)(\/|$)/,
    /\/[^/]+\/projects\/[^/]+\/cycles\/[^/]+/,
    /\/[^/]+\/projects\/[^/]+\/modules\/[^/]+/,
    /\/[^/]+\/your-work(\/|$)/,
  ];

  // Path → project UUID extractor. Plane URLs put the project UUID in the
  // second-to-last segment under /projects/. Returns null off a project
  // path.
  function currentProjectId() {
    const m = location.pathname.match(/\/projects\/([0-9a-f-]{36})(\/|$)/i);
    return m ? m[1] : null;
  }

  function onRefreshablePath() {
    return REFRESH_PATHS.some((re) => re.test(location.pathname));
  }

  // ---- User-busy heuristics ------------------------------------------------

  function userIsBusy() {
    const el = document.activeElement;
    if (el) {
      const tag = el.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
      if (el.isContentEditable) return true;
      if (el.closest('.ProseMirror')) return true;
    }
    if (document.querySelector('[role="dialog"]:not([aria-hidden="true"])')) return true;
    if (document.querySelector('[data-headlessui-state="open"]')) return true;
    return false;
  }

  // ---- Scroll preservation -------------------------------------------------

  function saveScroll() {
    try {
      const main = document.querySelector('main') || document.scrollingElement;
      sessionStorage.setItem(
        SCROLL_KEY,
        JSON.stringify({ path: location.pathname, y: main ? main.scrollTop : window.scrollY, t: Date.now() }),
      );
    } catch (_) { /* sessionStorage may be disabled in private mode */ }
  }

  function restoreScroll() {
    try {
      const raw = sessionStorage.getItem(SCROLL_KEY);
      if (!raw) return;
      const { path, y, t } = JSON.parse(raw);
      if (path !== location.pathname) return;
      if (Date.now() - t > 5 * 60_000) return;
      const tryRestore = (attemptsLeft) => {
        const main = document.querySelector('main') || document.scrollingElement;
        if (main) {
          main.scrollTop = y;
          window.scrollTo(0, y);
        }
        if (attemptsLeft > 0) setTimeout(() => tryRestore(attemptsLeft - 1), 300);
      };
      tryRestore(5);
    } catch (_) { /* malformed entry — ignore */ }
  }

  restoreScroll();

  // ---- Reload trigger ------------------------------------------------------

  let pending = null;

  function triggerReload(reason) {
    if (pending) return; // debouncing
    pending = setTimeout(() => {
      pending = null;
      if (document.hidden) return;
      if (!onRefreshablePath()) return;
      if (userIsBusy()) return;
      saveScroll();
      // eslint-disable-next-line no-console
      console.debug('[plane-tug] reload:', reason);
      location.reload();
    }, DEBOUNCE_MS);
  }

  // ---- SSE stream + fallback timer ----------------------------------------

  let source = null;
  let errorStreak = 0;
  let fallbackTimer = null;
  let currentProject = null;

  function startFallbackTimer() {
    if (fallbackTimer != null) return;
    // eslint-disable-next-line no-console
    console.warn('[plane-tug] SSE unreachable; falling back to ' + FALLBACK_INTERVAL_MS + 'ms timer');
    fallbackTimer = setInterval(() => triggerReload('fallback-timer'), FALLBACK_INTERVAL_MS);
  }

  function stopFallbackTimer() {
    if (fallbackTimer == null) return;
    clearInterval(fallbackTimer);
    fallbackTimer = null;
  }

  function closeStream() {
    if (source) {
      source.close();
      source = null;
    }
    currentProject = null;
  }

  function openStream(projectId) {
    if (source && currentProject === projectId) return;
    closeStream();
    currentProject = projectId;
    const url = SSE_PATH + '?project=' + encodeURIComponent(projectId);
    try {
      source = new EventSource(url);
    } catch (e) {
      // EventSource may throw synchronously on some browsers if the URL
      // is rejected (e.g. cross-origin). Fall back to the timer.
      startFallbackTimer();
      return;
    }
    source.addEventListener('open', () => {
      errorStreak = 0;
      stopFallbackTimer();
    });
    source.addEventListener('plane', (ev) => {
      errorStreak = 0;
      stopFallbackTimer();
      let evt = null;
      try { evt = JSON.parse(ev.data); } catch (_) { /* unknown payload — tickle anyway */ }
      triggerReload((evt && evt.event) || 'sse');
    });
    source.addEventListener('error', () => {
      errorStreak++;
      if (errorStreak >= ERROR_THRESHOLD) {
        startFallbackTimer();
      }
      // EventSource handles reconnect itself on transient errors.
    });
  }

  // ---- Path-aware lifecycle -----------------------------------------------

  function reconcile() {
    const pid = currentProjectId();
    if (pid && onRefreshablePath()) {
      openStream(pid);
    } else {
      closeStream();
      stopFallbackTimer();
    }
  }

  reconcile();

  // Plane is a SPA — react to history navigation. There is no canonical
  // "URL changed" event, so wrap pushState/replaceState and listen to
  // popstate.
  const origPush = history.pushState;
  const origReplace = history.replaceState;
  history.pushState = function () {
    origPush.apply(this, arguments);
    queueMicrotask(reconcile);
  };
  history.replaceState = function () {
    origReplace.apply(this, arguments);
    queueMicrotask(reconcile);
  };
  window.addEventListener('popstate', reconcile);
})();
