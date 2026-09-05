document.addEventListener("click", (event) => {
  if (!(event.target instanceof Element)) return;
  const link = event.target.closest("[data-history-back]");
  if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;

  const expectedPath = link.dataset.historyBack;
  const state = window.history.state;
  if (!expectedPath || !state || state.app !== "032" || !state.previousURL) return;

  try {
    const previous = new URL(state.previousURL, window.location.href);
    if (previous.origin === window.location.origin && (previous.pathname === expectedPath || previous.pathname.startsWith(expectedPath + "/"))) {
      event.preventDefault();
      window.history.back();
    }
  } catch (_) {
    // The href remains the safe fallback.
  }
});
