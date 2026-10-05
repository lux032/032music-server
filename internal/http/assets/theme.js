// Theme preference: "system" (default), "light" or "dark". Loaded
// synchronously in <head> so data-theme is set before first paint; the
// switch buttons ([data-theme-toggle]) live in the sidebar and the mobile
// "更多" sheet, which PJAX never replaces, so one delegated listener is enough.
(function () {
  'use strict';
  var KEY = '032_theme';
  var ORDER = ['system', 'light', 'dark'];
  var LABEL = { system: '跟随系统', light: '浅色', dark: '深色' };
  var ICON = { system: 'icon-theme-system', light: 'icon-sun', dark: 'icon-moon' };
  var media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;
  var root = document.documentElement;

  function read() {
    try { var v = localStorage.getItem(KEY); return ORDER.indexOf(v) >= 0 ? v : 'system'; } catch (e) { return 'system'; }
  }
  function resolve(pref) {
    if (pref === 'system') return media && media.matches ? 'dark' : 'light';
    return pref;
  }
  function apply(pref) {
    root.setAttribute('data-theme', resolve(pref));
    root.setAttribute('data-theme-pref', pref);
    var next = ORDER[(ORDER.indexOf(pref) + 1) % ORDER.length];
    var buttons = document.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      var text = '主题：' + LABEL[pref] + '，点击切换为' + LABEL[next];
      b.setAttribute('aria-label', text);
      b.setAttribute('title', text);
      var use = b.querySelector('use');
      if (use) {
        var href = use.getAttribute('href') || '';
        use.setAttribute('href', href.replace(/#.*$/, '') + '#' + ICON[pref]);
      }
      var label = b.querySelector('[data-theme-label]');
      if (label) label.textContent = LABEL[pref];
    }
  }

  apply(read());
  if (media) {
    var onChange = function () { if (read() === 'system') apply('system'); };
    if (media.addEventListener) media.addEventListener('change', onChange);
    else if (media.addListener) media.addListener(onChange);
  }
  document.addEventListener('DOMContentLoaded', function () { apply(read()); });
  document.addEventListener('click', function (e) {
    var target = e.target instanceof Element ? e.target.closest('[data-theme-toggle]') : null;
    if (!target) return;
    var pref = ORDER[(ORDER.indexOf(read()) + 1) % ORDER.length];
    try { localStorage.setItem(KEY, pref); } catch (err) { /* private mode: session-only */ }
    apply(pref);
  });
  window.addEventListener('storage', function (e) { if (e.key === KEY) apply(read()); });
})();
