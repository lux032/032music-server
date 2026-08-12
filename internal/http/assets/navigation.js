document.addEventListener("click", (event) => {
  if (!(event.target instanceof Element)) {
    return;
  }
  const link = event.target.closest("[data-history-back]");
  if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
    return;
  }

  const expectedPath = link.dataset.historyBack;
  if (!document.referrer || !expectedPath) {
    return;
  }

  try {
    const previous = new URL(document.referrer);
    if (previous.origin === window.location.origin && previous.pathname === expectedPath) {
      event.preventDefault();
      window.history.back();
    }
  } catch (_) {
    // The href remains the safe fallback when the referrer cannot be parsed.
  }
});
