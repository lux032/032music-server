// Calendar timestamps use the browser's timezone, never the server's timezone.
// Durations and date-only release metadata intentionally do not use this module.
function parts(date, options) {
  return Object.fromEntries(new Intl.DateTimeFormat('zh-CN', options)
    .formatToParts(date).map(part => [part.type, part.value]));
}

export function formatLocalTimestamp(date, seconds = false) {
  const p = parts(date, {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', ...(seconds ? { second: '2-digit' } : {}),
    hourCycle: 'h23'
  });
  return `${p.year}-${p.month}-${p.day} ${p.hour}:${p.minute}${seconds ? `:${p.second}` : ''}`;
}

export function localizeTimes(root = document) {
  const elements = [];
  if (root.matches?.('[data-local-time], [data-local-today]')) elements.push(root);
  elements.push(...root.querySelectorAll('[data-local-time], [data-local-today]'));
  for (const element of elements) {
    try {
      const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
      if (element.hasAttribute('data-local-today')) {
        const now = new Date();
        const p = parts(now, { month: 'numeric', day: 'numeric', weekday: 'long' });
        const text = `${p.month}月${p.day}日 ${p.weekday}`;
        if (element.textContent !== text) element.textContent = text;
        element.title = `浏览器时区：${zone}`;
      } else {
        const raw = element.getAttribute('datetime') || '';
        // Never interpret a date-only or timezone-less value as an instant.
        if (!/T.*(?:Z|[+-]\d{2}:\d{2})$/i.test(raw)) continue;
        const date = new Date(raw);
        if (Number.isNaN(date.getTime())) continue;
        const text = formatLocalTimestamp(date);
        if (element.textContent !== text) element.textContent = text;
        element.title = `${formatLocalTimestamp(date, true)} · ${zone}`;
      }
    } catch {
      // Preserve the escaped, explicitly labelled server fallback if Intl fails.
    }
  }
}

export function setupLocalTimes() {
  localizeTimes();
  // Observe added elements only. Ignore text mutations from our own updates,
  // progress bars and lyrics; PJAX and async fragments both insert elements.
  const observer = new MutationObserver(records => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (node.nodeType === 1) localizeTimes(node);
      }
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });
  // Long-lived player tabs should follow midnight and OS timezone changes.
  setInterval(() => { if (!document.hidden) localizeTimes(); }, 60_000);
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) localizeTimes();
  });
}
