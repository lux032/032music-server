// Compact performing-artist lines: fit as many complete names as possible,
// then a +N chip whose popover keeps the original join phrases.

let measureCtx = null;
let popover = null;
let anchorButton = null;
let pinned = false;
let openTimer = 0;
let closeTimer = 0;
let observer = null;
const observedParents = new WeakSet();

export function fitArtistCount(nameWidths, sepWidth, gap, available, chipWidth) {
  const count = nameWidths.length;
  if (!count || available <= 0) return 0;
  const total = nameWidths.reduce((sum, width) => sum + width, 0) + sepWidth * Math.max(0, count - 1);
  if (total <= available) return count;
  for (let shown = count - 1; shown >= 1; shown--) {
    const hidden = count - shown;
    const used = nameWidths.slice(0, shown).reduce((sum, width) => sum + width, 0)
      + sepWidth * Math.max(0, shown - 1)
      + gap
      + chipWidth(hidden);
    if (used <= available) return shown;
  }
  return 1;
}

function textWidth(font, text) {
  if (!measureCtx) measureCtx = document.createElement('canvas').getContext('2d');
  measureCtx.font = font || '14px sans-serif';
  return measureCtx.measureText(text || '').width;
}

function measureChip(button, label) {
  const probe = button.cloneNode(false);
  probe.hidden = false;
  probe.textContent = label;
  probe.style.position = 'absolute';
  probe.style.visibility = 'hidden';
  probe.style.pointerEvents = 'none';
  (button.ownerDocument.body || button.ownerDocument.documentElement).append(probe);
  const width = probe.offsetWidth || Math.ceil(textWidth(getComputedStyle(button).font, label) + 18);
  probe.remove();
  return width;
}

function availableWidth(root) {
  const parent = root.parentElement;
  if (!parent) return root.clientWidth;
  const style = getComputedStyle(parent);
  const pad = (parseFloat(style.paddingLeft) || 0) + (parseFloat(style.paddingRight) || 0);
  const inner = parent.clientWidth - pad;
  if (inner < 8) return 0;
  let others = 0;
  const range = document.createRange();
  for (const node of parent.childNodes) {
    if (node === root) continue;
    if (node.nodeType === Node.ELEMENT_NODE) others += node.getBoundingClientRect().width;
    else if (node.nodeType === Node.TEXT_NODE && node.textContent.trim()) {
      range.selectNodeContents(node);
      others += range.getBoundingClientRect().width;
    }
  }
  let available = inner - others;
  if (available < inner * 0.35) available = inner * 0.62;
  return Math.max(24, Math.min(inner, Math.floor(available) - 1));
}

export function layoutArtistLinks(root) {
  if (!root || root.dataset.artistLinks !== 'compact' || !root.isConnected) return;
  if (root.dataset.layingOut === '1') return;
  const line = root.querySelector('.artist-links-line');
  const more = root.querySelector('.artist-more');
  if (!line || !more) return;
  const names = [...line.querySelectorAll('.artist-name')];
  const seps = [...line.querySelectorAll('.artist-sep')];
  if (!names.length) return;

  root.dataset.layingOut = '1';
  try {
    names.forEach(name => { name.hidden = false; name.classList.remove('is-ellipsis'); });
    seps.forEach(sep => { sep.hidden = false; });
    root.classList.remove('is-ellipsis');
    more.hidden = true;

    const available = availableWidth(root);
    if (available < 8) return;
    root.style.maxWidth = `${available}px`;

    const nameWidths = names.map(name => name.getBoundingClientRect().width);
    const sepWidth = seps[0] ? seps[0].getBoundingClientRect().width : 0;
    const gap = parseFloat(getComputedStyle(root).columnGap) || 0;
    const shown = fitArtistCount(nameWidths, sepWidth, gap, available, hidden => measureChip(more, `+${hidden}`));
    if (shown >= names.length) {
      more.hidden = true;
      if (more.getAttribute('aria-expanded') === 'true' && anchorButton === more) closePopover();
      more.setAttribute('aria-expanded', 'false');
      return;
    }
    const hidden = names.length - shown;
    more.hidden = false;
    more.textContent = `+${hidden}`;
    more.setAttribute('aria-label', `其余 ${hidden} 位艺人，查看全部艺人`);
    for (let index = shown; index < names.length; index++) {
      names[index].hidden = true;
      const sep = names[index].previousElementSibling;
      if (sep && sep.classList.contains('artist-sep')) sep.hidden = true;
    }
    if (shown === 1) {
      const chip = more.offsetWidth || measureChip(more, `+${hidden}`);
      if (nameWidths[0] + gap + chip > available) {
        names[0].classList.add('is-ellipsis');
        root.classList.add('is-ellipsis');
      }
    }
    if (anchorButton === more && popover && !popover.hidden) placePopover(more);
  } finally {
    delete root.dataset.layingOut;
  }
}

function watch(root) {
  layoutArtistLinks(root);
  const parent = root.parentElement;
  if (!parent || !observer || observedParents.has(parent)) return;
  observedParents.add(parent);
  observer.observe(parent);
}

function scan(scope) {
  const root = scope && scope.querySelectorAll ? scope : document;
  root.querySelectorAll('[data-artist-links="compact"]').forEach(watch);
}

function artistKey(artists, fallback) {
  if (artists?.length) {
    return artists.map(artist => `${artist.id}\t${artist.name || ''}\t${artist.joinPhrase ?? ''}`).join('\n');
  }
  return `fallback:${fallback ?? ''}`;
}

function joinPhraseOf(artist) {
  return artist?.joinPhrase == null || artist.joinPhrase === '' ? ', ' : artist.joinPhrase;
}

export function buildCompactArtists(artists, fallback, options = {}) {
  const root = document.createElement('span');
  root.className = 'artist-links is-compact';
  root.dataset.artistLinks = 'compact';
  const list = Array.isArray(artists) ? artists : [];
  if (!list.length) {
    const plain = document.createElement('span');
    plain.className = 'artist-fallback';
    plain.textContent = fallback ?? '';
    root.append(plain);
    return root;
  }
  const line = document.createElement('span');
  line.className = 'artist-links-line';
  list.forEach((artist, index) => {
    if (index) {
      const sep = document.createElement('span');
      sep.className = 'artist-sep';
      sep.dataset.join = joinPhraseOf(artist);
      sep.textContent = ' | ';
      line.append(sep);
    }
    const link = document.createElement('a');
    link.className = 'artist-name';
    link.href = `/admin/artists/${artist.id}`;
    link.textContent = artist.name || '';
    if (options.playerNav) link.dataset.playerNav = '1';
    line.append(link);
  });
  const more = document.createElement('button');
  more.type = 'button';
  more.className = 'artist-more';
  more.hidden = true;
  more.setAttribute('aria-expanded', 'false');
  more.setAttribute('aria-haspopup', 'dialog');
  more.textContent = '+0';
  root.append(line, more);
  return root;
}

// renderCompactArtists fills container with one compact artist line.
// Returns true when the DOM was rebuilt.
export function renderCompactArtists(container, artists, fallback, options = {}) {
  if (!container) return false;
  const key = options.key || artistKey(artists, fallback);
  if (container.dataset.artistKey === key && container.querySelector('[data-artist-links]')) return false;
  container.dataset.artistKey = key;
  const root = buildCompactArtists(artists, fallback, options);
  container.replaceChildren(root);
  watch(root);
  return true;
}

function ensurePopover() {
  if (popover) return popover;
  popover = document.createElement('div');
  popover.className = 'artist-popover';
  popover.hidden = true;
  popover.setAttribute('role', 'dialog');
  popover.setAttribute('aria-labelledby', 'artist-popover-title');
  document.body.append(popover);
  popover.addEventListener('pointerenter', () => window.clearTimeout(closeTimer));
  popover.addEventListener('pointerleave', event => {
    if (pinned || event.pointerType !== 'mouse') return;
    scheduleClose();
  });
  return popover;
}

function fillPopover(root) {
  const panel = ensurePopover();
  const names = [...root.querySelectorAll('.artist-name')];
  panel.replaceChildren();
  const title = document.createElement('p');
  title.className = 'artist-popover-title';
  title.id = 'artist-popover-title';
  title.textContent = `全部艺人 (${names.length})`;
  const list = document.createElement('ul');
  list.className = 'artist-popover-list';
  names.forEach((name, index) => {
    const item = document.createElement('li');
    if (index) {
      const join = document.createElement('span');
      join.className = 'artist-popover-join';
      const sep = name.previousElementSibling;
      join.textContent = sep?.dataset.join || ', ';
      item.append(join);
    }
    const link = document.createElement('a');
    link.href = name.getAttribute('href') || '#';
    link.textContent = name.textContent || '';
    if (name.dataset.playerNav) link.dataset.playerNav = '1';
    item.append(link);
    list.append(item);
  });
  panel.append(title, list);
}

function placePopover(button) {
  const panel = ensurePopover();
  panel.hidden = false;
  const rect = button.getBoundingClientRect();
  const margin = 8;
  const width = panel.offsetWidth;
  const height = panel.offsetHeight;
  let top = rect.bottom + 4;
  if (top + height > window.innerHeight - margin) top = Math.max(margin, rect.top - height - 4);
  let left = rect.left;
  if (left + width > window.innerWidth - margin) left = Math.max(margin, window.innerWidth - width - margin);
  panel.style.top = `${top}px`;
  panel.style.left = `${left}px`;
}

function openPopover(button, pin) {
  const root = button.closest('[data-artist-links]');
  if (!root) return;
  window.clearTimeout(openTimer);
  window.clearTimeout(closeTimer);
  if (anchorButton && anchorButton !== button) anchorButton.setAttribute('aria-expanded', 'false');
  anchorButton = button;
  pinned = pin;
  fillPopover(root);
  placePopover(button);
  button.setAttribute('aria-expanded', 'true');
  if (pin) popover.querySelector('a')?.focus();
}

function closePopover() {
  window.clearTimeout(openTimer);
  window.clearTimeout(closeTimer);
  if (anchorButton) anchorButton.setAttribute('aria-expanded', 'false');
  anchorButton = null;
  pinned = false;
  if (popover) popover.hidden = true;
}

function scheduleClose() {
  window.clearTimeout(closeTimer);
  closeTimer = window.setTimeout(() => {
    if (!pinned) closePopover();
  }, 220);
}

function togglePinned(button) {
  if (anchorButton === button && pinned && popover && !popover.hidden) {
    closePopover();
    button.focus();
    return;
  }
  openPopover(button, true);
}

export function setupArtistLinks() {
  if (window.__032_artist_links) return;
  window.__032_artist_links = true;
  if ('ResizeObserver' in window) {
    observer = new ResizeObserver(entries => {
      for (const entry of entries) {
        entry.target.querySelectorAll('[data-artist-links="compact"]').forEach(layoutArtistLinks);
      }
    });
  }
  scan(document);
  document.fonts?.ready?.then?.(() => scan(document));
  const changes = new MutationObserver(records => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (!(node instanceof Element)) continue;
        if (node.matches('[data-artist-links="compact"]')) watch(node);
        node.querySelectorAll?.('[data-artist-links="compact"]').forEach(watch);
      }
    }
  });
  if (document.body) changes.observe(document.body, { childList: true, subtree: true });

  document.addEventListener('click', event => {
    const more = event.target instanceof Element ? event.target.closest('.artist-more') : null;
    if (more) {
      event.preventDefault();
      event.stopPropagation();
      togglePinned(more);
      return;
    }
    if (popover && !popover.hidden && event.target instanceof Node && !popover.contains(event.target)) closePopover();
  }, true);

  document.addEventListener('pointerover', event => {
    if (event.pointerType && event.pointerType !== 'mouse') return;
    const more = event.target instanceof Element ? event.target.closest('.artist-more') : null;
    if (!more || (anchorButton === more && !popover?.hidden)) return;
    window.clearTimeout(closeTimer);
    window.clearTimeout(openTimer);
    openTimer = window.setTimeout(() => openPopover(more, false), 150);
  });
  document.addEventListener('pointerout', event => {
    if (event.pointerType && event.pointerType !== 'mouse') return;
    const more = event.target instanceof Element ? event.target.closest('.artist-more') : null;
    if (!more) return;
    const next = event.relatedTarget;
    if (next instanceof Node && (more.contains(next) || popover?.contains(next))) return;
    window.clearTimeout(openTimer);
    if (!pinned) scheduleClose();
  });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') closePopover();
  });
  window.addEventListener('scroll', event => {
    if (!popover || popover.hidden) return;
    if (event.target instanceof Node && popover.contains(event.target)) return;
    closePopover();
  }, true);
  window.addEventListener('resize', () => {
    if (anchorButton && popover && !popover.hidden) placePopover(anchorButton);
  });
  document.addEventListener('032:pjax-before-swap', () => closePopover());
  document.addEventListener('032:pjax-applied', () => scan(document));
}
