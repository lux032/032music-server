// Detail pages' "返回" buttons go back to wherever the user came from
// (收藏, 歌手页, 搜索结果 …). The href is the fallback for a page opened
// directly — new tab, bookmark, full reload after a new deployment.

function backTarget() {
  const state = window.history.state;
  if (!state || state.app !== "032" || !state.previousURL) return null;
  try {
    const previous = new URL(state.previousURL, window.location.href);
    if (previous.origin !== window.location.origin || !previous.pathname.startsWith("/admin")) return null;
    return { url: previous, title: state.previousTitle || "", steps: Math.max(1, state.backSteps || 1) };
  } catch (_) {
    return null;
  }
}

// "周杰伦 · 032" → "周杰伦"; "某人 · 幕后人员 · 032" → "某人".
function pageName(title) {
  return title.split(" · ")[0].trim();
}

function updateBackLabels() {
  const target = backTarget();
  document.querySelectorAll("[data-history-back]").forEach((link) => {
    const label = link.querySelector(".back-button-label");
    if (!label) return;
    if (!label.dataset.fallbackLabel) label.dataset.fallbackLabel = label.textContent;
    let text = label.dataset.fallbackLabel;
    let fallbackPath = "";
    try { fallbackPath = new URL(link.href, window.location.href).pathname; } catch (_) {}
    // Coming from the fallback page itself keeps its more specific wording
    // ("返回专辑列表" rather than the list's bare title "专辑").
    if (target && target.url.pathname !== fallbackPath) {
      const name = pageName(target.title);
      // Latin names read better spaced: "返回 Taylor Swift", "返回我的收藏".
      text = !name ? "返回" : /^[\x00-\x7f]/.test(name) ? "返回 " + name : "返回" + name;
    }
    label.textContent = text;
    link.title = text;
  });
}

document.addEventListener("click", (event) => {
  if (!(event.target instanceof Element)) return;
  const link = event.target.closest("[data-history-back]");
  if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  const target = backTarget();
  if (!target) return;
  event.preventDefault();
  window.history.go(-target.steps);
});

document.addEventListener("032:pjax-applied", updateBackLabels);
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", updateBackLabels);
else updateBackLabels();
