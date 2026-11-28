(function () {
  'use strict';

  const PRESETS = {
    gemini: {
      baseURL: 'https://generativelanguage.googleapis.com/v1beta/openai',
      modelPlaceholder: '例如 gemini-2.5-flash-lite',
      reviewPlaceholder: '例如 gemini-2.5-flash',
      defaultModel: 'gemini-2.5-flash-lite',
      defaultReview: 'gemini-2.5-flash',
      keyOptional: false
    },
    deepseek: {
      baseURL: 'https://api.deepseek.com/v1',
      modelPlaceholder: '例如 deepseek-chat',
      reviewPlaceholder: '例如 deepseek-reasoner',
      defaultModel: 'deepseek-chat',
      defaultReview: 'deepseek-reasoner',
      keyOptional: false
    },
    openai: {
      baseURL: 'https://api.openai.com/v1',
      modelPlaceholder: '例如 gpt-4o-mini',
      reviewPlaceholder: '例如 gpt-4o',
      defaultModel: 'gpt-4o-mini',
      defaultReview: 'gpt-4o',
      keyOptional: false
    },
    qwen: {
      baseURL: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
      modelPlaceholder: '例如 qwen-turbo / qwen-plus',
      reviewPlaceholder: '例如 qwen-max',
      defaultModel: 'qwen-plus',
      defaultReview: 'qwen-max',
      keyOptional: false
    },
    openrouter: {
      baseURL: 'https://openrouter.ai/api/v1',
      modelPlaceholder: '例如 google/gemini-2.5-flash-lite',
      reviewPlaceholder: '例如 google/gemini-2.5-flash',
      defaultModel: 'google/gemini-2.5-flash-lite',
      defaultReview: 'google/gemini-2.5-flash',
      keyOptional: false
    },
    ollama: {
      baseURL: 'http://127.0.0.1:11434/v1',
      modelPlaceholder: '例如 qwen2.5:14b',
      reviewPlaceholder: '留空复用主模型',
      defaultModel: 'qwen2.5:14b',
      defaultReview: '',
      keyOptional: true
    },
    custom: {
      baseURL: '',
      modelPlaceholder: '输入兼容接口的模型名称',
      reviewPlaceholder: '可选，留空复用主模型',
      defaultModel: '',
      defaultReview: '',
      keyOptional: false
    }
  };

  const $ = (id) => document.getElementById(id);
  const csrfMeta = document.querySelector('meta[name="csrf-token"]');
  const CSRF = csrfMeta ? csrfMeta.getAttribute('content') || '' : '';

  let data;
  try {
    data = JSON.parse($('setup-bootstrap').textContent || '{}');
  } catch (err) {
    return;
  }

  const state = {
    step: (data.state && data.state.currentStep) || 1,
    preset: 'custom'
  };

  function toast(message) {
    const region = $('toast-region');
    if (!region) return;
    region.innerHTML = '';
    const el = document.createElement('div');
    el.className = 'toast-item';
    el.innerHTML = '<svg class="icon icon-sm" aria-hidden="true"><use href="#i-check"></use></svg><span></span>';
    el.querySelector('span').textContent = message;
    region.appendChild(el);
    setTimeout(() => { if (el.parentNode) el.remove(); }, 2400);
  }

  function showStepError(msg) {
    const banner = $('step-error-banner');
    if (!msg) {
      banner.hidden = true;
      $('step-error-text').textContent = '';
      return;
    }
    $('step-error-text').textContent = msg;
    banner.hidden = false;
    banner.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }

  async function apiJSON(method, url, payload) {
    const opts = {
      method,
      credentials: 'same-origin',
      headers: { Accept: 'application/json' }
    };
    if (method !== 'GET') {
      opts.headers['X-CSRF-Token'] = CSRF;
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(payload || {});
    }
    const res = await fetch(url, opts);
    let json = {};
    try { json = await res.json(); } catch (_) {}
    if (!res.ok) {
      const errObj = json && json.error ? json.error : {};
      const err = new Error(errObj.message || ('请求失败（HTTP ' + res.status + '）'));
      err.field = errObj.field || '';
      err.code = errObj.code || '';
      throw err;
    }
    return json;
  }

  /* ---- Secret show/hide ---- */
  document.querySelectorAll('[data-toggle-secret]').forEach((btn) => {
    btn.addEventListener('click', () => {
      const input = $(btn.dataset.toggleSecret);
      if (!input) return;
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      const use = btn.querySelector('use');
      if (use) use.setAttribute('href', show ? '#i-eye-off' : '#i-eye');
    });
  });

  function detectPreset(baseUrl) {
    const u = (baseUrl || '').trim().toLowerCase().replace(/\/+$/, '');
    if (!u) return 'gemini';
    for (const [k, p] of Object.entries(PRESETS)) {
      if (p.baseURL && u === p.baseURL.toLowerCase()) return k;
    }
    return 'custom';
  }

  function parseNewTavilyKeys() {
    const raw = $('ws-tavily-keys').value || '';
    return Array.from(new Set(raw.split(/[\r\n,;]+/).map((s) => s.trim()).filter(Boolean)));
  }

  function isDeepSeekOfficialLLM() {
    const url = ($('llm-base-url').value || '').trim().toLowerCase();
    const hasKey = Boolean(($('llm-api-key').value || '').trim()) || (data.step2.keySet && !$('clear-llm-key').checked);
    return url.startsWith('https://api.deepseek.com') && hasKey;
  }

  function isLLMConfigured() {
    const url = ($('llm-base-url').value || '').trim();
    const model = ($('llm-model').value || '').trim();
    const hasKey = Boolean(($('llm-api-key').value || '').trim()) || (data.step2.keySet && !$('clear-llm-key').checked);
    const isOllama = url.includes('127.0.0.1:11434') || url.includes('localhost:11434') || state.preset === 'ollama';
    return Boolean(url && model && (hasKey || isOllama));
  }

  function effectiveTavilyCount() {
    const added = parseNewTavilyKeys().length;
    if ($('clear-tavily-keys').checked) return 0;
    if ($('ws-tavily-mode').value === 'replace' && added > 0) return added;
    return (data.step2.tavilyKeyCount || 0) + added;
  }

  function isWebSearchConfigured() {
    const prov = $('ws-provider').value;
    const hasTav = effectiveTavilyCount() > 0;
    const hasDS = Boolean(($('ws-deepseek-key').value || '').trim()) ||
      (data.step2.deepSeekKeySet && !$('clear-deepseek-key').checked) ||
      isDeepSeekOfficialLLM();
    if (prov === 'native_then_search') return isLLMConfigured() || hasTav || hasDS;
    if (prov === 'tavily') return hasTav;
    if (prov === 'deepseek') return hasDS;
    return hasTav || hasDS;
  }

  function webSearchSummaryText() {
    const prov = $('ws-provider').value;
    const tavCount = effectiveTavilyCount();
    const hasDS = Boolean(($('ws-deepseek-key').value || '').trim()) ||
      (data.step2.deepSeekKeySet && !$('clear-deepseek-key').checked) ||
      isDeepSeekOfficialLLM();
    if (prov === 'native_then_search') {
      if (isLLMConfigured()) {
        if (tavCount && hasDS) return `模型原生优先 + Tavily（${tavCount} 把）/ DeepSeek 回退`;
        if (tavCount) return `模型原生优先 + Tavily（${tavCount} 把）回退`;
        if (hasDS) return '模型原生优先 + DeepSeek 回退';
        return '模型原生优先（复用全局 AI）';
      }
      if (tavCount || hasDS) return '模型原生优先（仅回退可用）';
      return '未配置';
    }
    if (prov === 'tavily') return tavCount ? `Tavily（${tavCount} 把 Key）` : '未配置';
    if (prov === 'deepseek') return hasDS ? 'DeepSeek 联网' : '未配置';
    if (tavCount && hasDS) return `Tavily（${tavCount} 把）+ DeepSeek 兜底`;
    if (tavCount) return `Tavily（${tavCount} 把 Key）`;
    if (hasDS) return 'DeepSeek 联网';
    return '未配置';
  }

  function enabledMetadataNames() {
    const names = [];
    if ($('mb-enabled').checked && ($('mb-contact').value || '').trim()) names.push('MusicBrainz');
    if ($('bgm-enabled').checked) names.push('Bangumi');
    if ($('lastfm-enabled').checked && (($('lastfm-key').value || '').trim() || data.step1.lastfmKeySet)) names.push('Last.fm');
    if ((($('fanart-key').value || '').trim()) || (data.step1.fanartKeySet && !$('clear-fanart-key').checked)) names.push('Fanart.tv');
    return names;
  }

  function enabledFeaturesList() {
    const list = [];
    if ($('feat-ai-review').checked) {
      list.push($('feat-review-mode').value === 'auto' ? 'AI 初审（分级自治）' : 'AI 初审（仅给建议）');
    }
    if ($('feat-alias').checked) list.push('中文别名补全');
    if ($('feat-avatar-ai').checked) list.push('头像联网兜底');
    if ($('feat-bio-ai').checked) list.push('简介 AI 翻译');
    if ($('feat-lyrics-trans').checked) list.push('AI 歌词翻译');
    if ($('feat-daily-mix').checked) list.push('每日推荐');
    if ($('feat-ai-tagging').checked) list.push('AI 曲库打标');
    if ($('feat-acoustic').checked) list.push('本地声学分析');
    return list;
  }

  /* ---- Hydrate DOM from bootstrap payload ---- */
  function hydrateFromBootstrap(nextData) {
    if (nextData) data = nextData;
    const isPending = data.state && data.state.status === 'pending';
    $('header-context').textContent = isPending ? '首次启动初始化' : '初始化向导（配置复查）';
    $('skip-all-btn').hidden = !isPending;
    $('return-admin-link').hidden = isPending;
    $('rail-scan-hold-text').textContent = isPending
      ? '首次扫描已暂缓，第 5 步确认后启动'
      : '当前为配置复查模式，保存后立即生效';

    // Contextual deployment alerts (only shown when warnings exist)
    const alerts = $('global-env-alerts');
    const warnParts = [];
    if (data.deployment.proxyPeer) {
      warnParts.push(`检测到反向代理 ${data.deployment.proxyPeer} 未加入 MUSIC_SERVER_TRUSTED_PROXIES，代理后的访问者将共享限流计数`);
    }
    if (data.deployment.mediaTokenEphemeral) {
      warnParts.push('MUSIC_SERVER_MEDIA_TOKEN 为本次启动临时生成，重启后外部播放器链接将失效，建议在「账号与安全」生成固定 Token');
    }
    if (warnParts.length > 0) {
      alerts.hidden = false;
      alerts.innerHTML = '';
      const box = document.createElement('div');
      box.className = 'notice notice-warning';
      box.innerHTML = '<svg class="icon icon-sm" aria-hidden="true"><use href="#i-alert"></use></svg><div class="notice-body"><strong></strong><span></span></div>';
      box.querySelector('strong').textContent = `检测到 ${warnParts.length} 项部署提醒（不阻断当前配置）`;
      box.querySelector('span').innerHTML = warnParts.map((t, i) => `(${i + 1}) ${escapeHTML(t)}`).join('；') + '。<a href="/admin/settings/security">前往账号与安全 →</a>';
      alerts.appendChild(box);
    } else {
      alerts.hidden = true;
      alerts.innerHTML = '';
    }

    // Step 1
    $('mb-enabled').checked = Boolean(data.step1.musicbrainzEnabled);
    $('mb-contact').value = data.step1.musicbrainzContact || '';
    $('mb-auto').checked = data.step1.musicbrainzAuto !== false;
    $('bgm-enabled').checked = Boolean(data.step1.bangumiEnabled);
    $('bgm-auto').checked = data.step1.bangumiAuto !== false;
    $('lastfm-enabled').checked = Boolean(data.step1.lastfmEnabled);
    $('lastfm-key').value = '';
    $('lastfm-key').placeholder = data.step1.lastfmKeySet ? '已配置；留空保持不变' : '在 last.fm/api/account/create 获取 32 位 API Key';
    $('bio-lang').value = data.step1.bioTargetLanguage || 'zh-Hans';
    $('wiki-enabled').checked = data.step1.wikipediaEnabled !== false;
    $('fanart-key').value = '';
    $('fanart-key').placeholder = data.step1.fanartKeySet ? '已配置；留空保持不变' : '个人 Project / Personal API Key';
    $('clear-fanart-wrap').hidden = !data.step1.fanartKeySet;
    $('clear-fanart-key').checked = false;
    $('meta-cache-days').value = data.step1.cacheDays || 30;
    $('img-bgm').checked = data.step1.imgBangumi !== false;
    $('img-deezer').checked = data.step1.imgDeezer !== false;
    $('img-wiki').checked = data.step1.imgWikipedia !== false;
    syncStep1Fields();

    // Step 2
    state.preset = detectPreset(data.step2.baseUrl);
    document.querySelectorAll('#llm-presets .chip').forEach((c) => {
      c.classList.toggle('is-selected', c.dataset.preset === state.preset);
    });
    $('llm-base-url').value = data.step2.baseUrl || (PRESETS[state.preset] ? PRESETS[state.preset].baseURL : '');
    $('llm-api-key').value = '';
    $('llm-api-key').placeholder = data.step2.keySet ? '已设置（留空保持不变）' : '输入模型服务商 API Key';
    $('clear-llm-key-wrap').hidden = !data.step2.keySet;
    $('clear-llm-key').checked = false;
    $('llm-model').value = data.step2.model || '';
    $('llm-review-model').value = data.step2.reviewModel || '';
    $('llm-fallback').value = data.step2.fallbackModel || '';
    $('llm-proxy').value = '';
    $('llm-proxy').placeholder = data.step2.proxySet ? '已设置（留空保持不变）' : 'http://192.168.1.2:7890（留空沿用容器环境变量）';
    $('clear-llm-proxy-wrap').hidden = !data.step2.proxySet;
    $('clear-llm-proxy').checked = false;
    $('llm-rpm').value = data.step2.rpm || 60;
    $('llm-concurrency').value = data.step2.concurrency || 4;
    $('llm-reasoning').value = data.step2.reasoningEffort || '';
    $('llm-vision-model').value = data.step2.visionModel || '';
    $('ws-provider').value = data.step2.webSearchProvider || 'tavily_then_deepseek';
    $('ws-tavily-keys').value = '';
    $('clear-tavily-wrap').hidden = !(data.step2.tavilyKeyCount > 0);
    $('clear-tavily-keys').checked = false;
    $('ws-deepseek-key').value = '';
    $('ws-deepseek-key').placeholder = data.step2.deepSeekKeySet
      ? '已设置（留空保持不变）'
      : '如仅使用 Tavily 或上方已填 DeepSeek 官方 Key，此处可留空';
    $('clear-deepseek-wrap').hidden = !data.step2.deepSeekKeySet;
    $('clear-deepseek-key').checked = false;
    syncStep2Status();

    // Step 3
    $('feat-ai-review').checked = Boolean(data.step3.aiReviewEnabled);
    $('feat-review-mode').value = data.step3.aiReviewMode || 'recommend';
    $('feat-review-web').checked = Boolean(data.step3.aiReviewWeb);
    $('feat-alias').checked = Boolean(data.step3.aliasEnabled);
    $('feat-avatar-ai').checked = Boolean(data.step3.avatarWebAiEnabled);
    $('feat-bio-ai').checked = Boolean(data.step3.bioAiEnabled);
    $('feat-lyrics-trans').checked = Boolean(data.step3.lyricsTransEnabled);
    $('feat-daily-mix').checked = Boolean(data.step3.dailyMixEnabled);
    $('feat-mix-llm').checked = Boolean(data.step3.dailyMixLlmTitles);
    $('feat-ai-tagging').checked = Boolean(data.step3.aiTaggingEnabled);
    $('feat-acoustic').checked = Boolean(data.step3.acousticEnabled);
    syncStep3Dependencies();

    // Step 4
    $('lb-enabled').checked = Boolean(data.step4.lbEnabled);
    $('lb-url').value = data.step4.lbServerUrl || 'https://api.listenbrainz.org';
    $('lb-token').value = '';
    $('lb-token').placeholder = data.step4.lbTokenSet ? '已配置；留空保持不变' : '在 ListenBrainz 设置页复制 User Token';
    $('lb-nowplaying').checked = data.step4.lbNowPlaying !== false;
    $('lb-love').checked = Boolean(data.step4.lbLoveSync);
    $('lb-fields').hidden = !$('lb-enabled').checked;
    $('lb-status-badge').className = data.step4.lbConnected ? 'badge badge-success' : 'badge';
    $('lb-status-badge').textContent = data.step4.lbConnected ? ('已连接 · ' + data.step4.lbUsername) : '未连接';

    $('lfm-scrobble-enabled').checked = Boolean(data.step4.lfmScrobbleEnabled);
    $('lfm-scrobble-key').value = '';
    $('lfm-secret').value = '';
    $('lfm-secret').placeholder = data.step4.lfmSecretSet ? '已配置；留空保持不变' : '与 API Key 一同在 Last.fm API 页获取';
    $('lfm-nowplaying').checked = data.step4.lfmNowPlaying !== false;
    $('lfm-love').checked = Boolean(data.step4.lfmLoveSync);
    $('lfm-scrobble-fields').hidden = !$('lfm-scrobble-enabled').checked;
    $('lfm-scrobble-badge').className = data.step4.lfmConnected ? 'badge badge-success' : 'badge';
    $('lfm-scrobble-badge').textContent = data.step4.lfmConnected ? ('已连接 · ' + data.step4.lfmUsername) : '未连接';
    if (data.step4.lfmPendingAuthUrl) {
      $('lfm-open-auth-link').href = data.step4.lfmPendingAuthUrl;
      $('lfm-oauth-box').hidden = false;
    } else {
      $('lfm-oauth-box').hidden = true;
    }
    syncStep4LastFMReuse();

    $('acq-enabled').checked = Boolean(data.step4.acqEnabled);
    $('acq-fields').hidden = !$('acq-enabled').checked;
    $('acq-status-badge').className = data.step4.acqEnabled ? 'badge badge-accent' : 'badge';
    $('acq-status-badge').textContent = data.step4.acqEnabled ? '已启用' : '未启用';
    $('acq-jackett-url').value = data.step4.jackettUrl || '';
    $('acq-jackett-indexer').value = data.step4.jackettIndexer || 'jpopsuki';
    $('acq-jackett-key').value = '';
    $('acq-jackett-key').placeholder = data.step4.jackettKeySet ? '已设置；留空保留' : 'Jackett 面板右上角 API Key';
    $('acq-qb-url').value = data.step4.qbUrl || '';
    $('acq-qb-user').value = data.step4.qbUsername || '';
    $('acq-qb-pass').value = '';
    $('acq-qb-pass').placeholder = data.step4.qbPasswordSet ? '已设置；留空保留' : 'Web UI 密码';
    $('acq-qb-cat').value = data.step4.qbCategory || '';

    // Step 5
    $('watch-enabled').checked = Boolean(data.step5.watchEnabled);
    const intervalSel = $('watch-interval');
    const secStr = String(data.step5.watchIntervalSeconds || 60);
    if (!Array.from(intervalSel.options).some((o) => o.value === secStr)) {
      const opt = document.createElement('option');
      opt.value = secStr;
      opt.textContent = `每 ${secStr} 秒`;
      intervalSel.appendChild(opt);
    }
    intervalSel.value = secStr;
    intervalSel.disabled = !$('watch-enabled').checked;
    $('db-backup-meta').textContent = `SQLite 数据目录 ${data.deployment.dataDirectory || '/data'} 可写 · 每日 ${data.deployment.backupTime || '02:30'} (UTC+8) 自动一致快照（保留 ${data.deployment.backupKeepDaily || 3} 份）· 缺失文件策略默认「仅隐藏不删除」。`;
    renderPreflight(data.preflight);
    updateRail();
  }

  function escapeHTML(str) {
    return String(str).replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
  }

  function syncStep1Fields() {
    $('mb-fields').hidden = !$('mb-enabled').checked;
    $('bgm-fields').hidden = !$('bgm-enabled').checked;
    $('lastfm-fields').hidden = !$('lastfm-enabled').checked;
    if (!$('mb-enabled').checked) {
      $('mb-contact').classList.remove('is-invalid');
      $('mb-contact-error').hidden = true;
    }
    if (!$('lastfm-enabled').checked) {
      $('lastfm-key').classList.remove('is-invalid');
      $('lastfm-key-error').hidden = true;
    }
    syncStep4LastFMReuse();
  }

  function syncStep2Status() {
    const llmOK = isLLMConfigured();
    $('llm-status-badge').className = llmOK ? 'badge badge-success' : 'badge';
    $('llm-status-badge').textContent = llmOK ? ('已填写 · ' + $('llm-model').value.trim()) : '未配置';

    const added = parseNewTavilyKeys().length;
    const saved = data.step2.tavilyKeyCount || 0;
    $('tavily-count-label').textContent = added > 0
      ? `已保存 ${saved} 把 · 新增识别 ${added} 把`
      : `已保存 ${saved} 把 Key`;
    $('ws-deepseek-reuse-hint').hidden = !isDeepSeekOfficialLLM();

    const wsOK = isWebSearchConfigured();
    $('ws-status-badge').className = wsOK ? 'badge badge-success' : 'badge';
    $('ws-status-badge').textContent = wsOK ? webSearchSummaryText() : '未配置';
    syncStep3Dependencies();
  }

  function setFeatureLock(rowKey, checkboxId, locked, depText, jumpStep) {
    const row = document.querySelector(`[data-feature-row="${rowKey}"]`);
    if (!row) return;
    const cb = $(checkboxId);
    const badge = row.querySelector('[data-dep-badge]');
    row.classList.toggle('is-locked', locked);
    cb.disabled = locked;
    if (locked) cb.checked = false;

    let link = row.querySelector('.dep-jump-link');
    if (locked) {
      badge.className = 'badge badge-warning';
      badge.textContent = depText;
      if (!link && jumpStep) {
        link = document.createElement('button');
        link.type = 'button';
        link.className = 'inline-link-btn dep-jump-link';
        link.textContent = '去配置 →';
        link.addEventListener('click', () => goToStep(jumpStep));
        row.querySelector('.feature-title').appendChild(link);
      }
    } else {
      badge.className = 'badge badge-success';
      badge.textContent = '前置已就绪';
      if (link) link.remove();
    }
  }

  function syncStep3Dependencies() {
    const llmOK = isLLMConfigured();
    const wsOK = isWebSearchConfigured();
    const analyzerOK = Boolean(data.deployment.analyzerConfigured);
    const metaNames = enabledMetadataNames();

    const strip = $('prereq-strip');
    strip.innerHTML = '';
    [
      { ready: metaNames.length > 0, label: metaNames.length ? `元数据：${metaNames.join(' + ')}` : '元数据：未启用', jump: 1 },
      { ready: llmOK, label: llmOK ? `LLM：${$('llm-model').value.trim()}` : 'LLM：未配置', jump: 2 },
      { ready: wsOK, label: wsOK ? `联网搜索：${webSearchSummaryText()}` : '联网搜索：未配置', jump: 2 },
      { ready: analyzerOK, label: analyzerOK ? '声学分析：Sidecar 在线' : '声学分析：未部署', jump: null }
    ].forEach((it) => {
      const pill = document.createElement('span');
      pill.className = 'prereq-pill' + (it.ready ? ' is-ready' : '');
      const dot = document.createElement('span');
      dot.className = 'prereq-dot';
      dot.setAttribute('aria-hidden', 'true');
      const txt = document.createElement('span');
      txt.textContent = it.label;
      pill.appendChild(dot);
      pill.appendChild(txt);
      if (!it.ready && it.jump) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.textContent = '去配置';
        btn.addEventListener('click', () => goToStep(it.jump));
        pill.appendChild(btn);
      }
      strip.appendChild(pill);
    });

    setFeatureLock('ai-review', 'feat-ai-review', !llmOK, '需先配置 LLM', 2);
    setFeatureLock('alias', 'feat-alias', !(llmOK && wsOK), llmOK ? '需联网搜索' : '需 LLM + 联网搜索', 2);
    setFeatureLock('avatar-ai', 'feat-avatar-ai', !(llmOK && wsOK), llmOK ? '需联网搜索' : '需 LLM + 联网搜索', 2);
    setFeatureLock('bio-ai', 'feat-bio-ai', !llmOK, '需先配置 LLM', 2);
    setFeatureLock('lyrics-trans', 'feat-lyrics-trans', !llmOK, '需先配置 LLM', 2);
    setFeatureLock('ai-tagging', 'feat-ai-tagging', !llmOK, '需先配置 LLM', 2);
    setFeatureLock('acoustic', 'feat-acoustic', !analyzerOK, '未部署 Sidecar', null);

    $('feat-acoustic-desc').textContent = analyzerOK
      ? '提取曲目能量、情绪、乐器与 1280 维声学向量用于相似推荐，纯本地计算，遇到实时转码与扫描自动让路。'
      : '当前未检测到 music-analyzer 服务；如需启用请在部署目录执行 docker compose --profile analysis up -d --build。';

    $('feat-review-web').disabled = !wsOK;
    if (!wsOK) $('feat-review-web').checked = false;
    $('sub-ai-review').hidden = !$('feat-ai-review').checked;
    $('sub-daily-mix').hidden = !$('feat-daily-mix').checked;
  }

  function syncStep4LastFMReuse() {
    const hasKey = Boolean(($('lastfm-key').value || '').trim()) || Boolean(data.step1.lastfmKeySet);
    $('lfm-key-reused').hidden = !hasKey;
    $('lfm-scrobble-apikey-wrap').hidden = hasKey;
  }

  function renderPreflight(pf) {
    if (!pf) return;
    data.preflight = pf;
    $('dir-path-code').textContent = pf.directory || '/music';
    $('dir-check-desc').textContent = pf.message || '';
    $('dir-check-icon').className = pf.ok ? 'check-icon is-ok' : 'check-icon is-err';
    $('dir-check-badge').className = pf.ok ? 'badge badge-success' : 'badge badge-danger';
    $('dir-check-badge').textContent = pf.ok ? '挂载正常' : (pf.status === 'empty' ? '目录为空' : '无法读取');
    $('finish-start-scan-btn').disabled = !pf.ok;
  }

  function renderStep5() {
    renderPreflight(data.preflight);
    const meta = enabledMetadataNames();
    const llmOK = isLLMConfigured();
    const wsOK = isWebSearchConfigured();
    const feats = enabledFeaturesList();
    const syncParts = [];
    if ($('lb-enabled').checked && (($('lb-token').value || '').trim() || data.step4.lbTokenSet || data.step4.lbConnected)) {
      syncParts.push(data.step4.lbConnected ? `ListenBrainz（${data.step4.lbUsername}）` : 'ListenBrainz（已填 Token）');
    }
    if ($('lfm-scrobble-enabled').checked && (($('lfm-secret').value || '').trim() || data.step4.lfmSecretSet || data.step4.lfmConnected)) {
      syncParts.push(data.step4.lfmConnected ? `Last.fm（${data.step4.lfmUsername}）` : 'Last.fm Scrobble（待网页授权）');
    }
    if ($('acq-enabled').checked) syncParts.push('资源获取（Jackett + qBittorrent）');

    const chain = $('pipeline-chain');
    chain.innerHTML = '';
    const nodes = [{ label: '首次扫描导入本地标签', primary: true }];
    const autoMeta = meta.filter((m) => m === 'MusicBrainz' || m === 'Bangumi');
    if (autoMeta.length) nodes.push({ label: `${autoMeta.join(' / ')} 自动对齐` });
    if ($('feat-ai-review').checked) {
      nodes.push({ label: $('feat-review-mode').value === 'auto' ? 'AI 分级自治初审' : 'AI 辅助预审' });
    }
    const postTasks = feats.filter((f) => !f.startsWith('AI 初审') && f !== 'AI 歌词翻译');
    if (postTasks.length) {
      nodes.push({ label: postTasks.slice(0, 3).join(' · ') + (postTasks.length > 3 ? ` 等 ${postTasks.length} 项` : '') });
    }
    if (nodes.length === 1) {
      nodes.push({ label: '仅本地曲库就绪（可随时补充增强源）' });
    }
    nodes.forEach((node, idx) => {
      if (idx > 0) {
        const arrow = document.createElement('span');
        arrow.className = 'pipeline-arrow';
        arrow.innerHTML = '<svg class="icon icon-sm" aria-hidden="true"><use href="#i-arrow-right"></use></svg>';
        chain.appendChild(arrow);
      }
      const span = document.createElement('span');
      span.className = 'pipeline-node' + (node.primary ? ' is-primary' : '');
      span.textContent = node.label;
      chain.appendChild(span);
    });
    const hasChainWork = autoMeta.length > 0 || postTasks.length > 0 || $('feat-ai-review').checked;
    $('auto-chain-wrap').hidden = !hasChainWork;

    const rows = [
      {
        step: 1,
        title: '元数据与图源',
        badge: meta.length ? [`已启用 ${meta.length} 项`, 'badge-success'] : ['未启用', 'badge'],
        detail: meta.length ? `${meta.join(' · ')} · 简介语言 ${$('bio-lang').value}` : '暂未启用外部数据源，仅使用音频文件本地标签'
      },
      {
        step: 2,
        title: 'LLM 与联网搜索',
        badge: (llmOK || wsOK) ? ['已配置', 'badge-success'] : ['未配置', 'badge'],
        detail: [
          llmOK ? `主模型 ${$('llm-model').value.trim()}${$('llm-review-model').value.trim() ? ' / 复核 ' + $('llm-review-model').value.trim() : ''}` : '未配置 LLM',
          wsOK ? `联网搜索：${webSearchSummaryText()}` : '未配置联网搜索'
        ].join(' · ')
      },
      {
        step: 3,
        title: '自动化能力',
        badge: feats.length ? [`已开启 ${feats.length} 项`, 'badge-success'] : ['保守关闭', 'badge'],
        detail: feats.length ? feats.join(' · ') : '所有后台 AI 与分析任务均保持关闭，零额度消耗'
      },
      {
        step: 4,
        title: '同步与资源获取',
        badge: syncParts.length ? [`已配置 ${syncParts.length} 项`, 'badge-success'] : ['未配置', 'badge'],
        detail: syncParts.length ? syncParts.join(' · ') : '未配置 Scrobble 或 Jackett / qBittorrent'
      }
    ];

    const container = $('summary-list');
    container.innerHTML = '';
    rows.forEach((r) => {
      const div = document.createElement('div');
      div.className = 'summary-row';
      div.innerHTML = '<div class="summary-meta"><div class="summary-head"><strong></strong><span class="badge"></span></div><div class="summary-detail"></div></div><button type="button" class="btn btn-secondary btn-compact">修改</button>';
      div.querySelector('strong').textContent = r.title;
      const b = div.querySelector('.badge');
      b.className = 'badge ' + r.badge[1];
      b.textContent = r.badge[0];
      div.querySelector('.summary-detail').textContent = r.detail;
      div.querySelector('button').addEventListener('click', () => goToStep(r.step));
      container.appendChild(div);
    });
  }

  function updateRail() {
    const stepStates = (data.state && data.state.stepStates) || {};
    let savedCount = 0;
    for (let s = 1; s <= 4; s++) {
      if (stepStates[s] === 'done' || stepStates[String(s)] === 'done') savedCount++;
    }
    document.querySelectorAll('.step-item').forEach((el) => {
      const s = Number(el.dataset.gotoStep);
      const st = stepStates[s] || stepStates[String(s)] || 'pending';
      const active = s === state.step;
      el.classList.toggle('is-active', active);
      el.classList.toggle('is-done', st === 'done');
      el.classList.toggle('is-skipped', st === 'skipped');
      if (active) el.setAttribute('aria-current', 'step');
      else el.removeAttribute('aria-current');
      const badge = el.querySelector('[data-step-badge]');
      if (st === 'done' && !active) {
        badge.innerHTML = '<svg class="icon icon-sm" aria-hidden="true"><use href="#i-check"></use></svg>';
      } else {
        badge.textContent = String(s);
      }
    });

    const meta = enabledMetadataNames();
    const stOf = (s) => stepStates[s] || stepStates[String(s)] || 'pending';
    document.querySelector('[data-step-sub="1"]').textContent = stOf(1) === 'skipped'
      ? '已跳过'
      : (stOf(1) === 'done' && meta.length ? `已选 ${meta.slice(0, 2).join(' · ')}${meta.length > 2 ? ' 等' : ''}` : 'MusicBrainz · Bangumi');
    document.querySelector('[data-step-sub="2"]').textContent = stOf(2) === 'skipped'
      ? '已跳过'
      : (isLLMConfigured() ? $('llm-model').value.trim() : '大模型与 Tavily 密钥');
    const feats = enabledFeaturesList();
    document.querySelector('[data-step-sub="3"]').textContent = stOf(3) === 'skipped'
      ? '已跳过'
      : (feats.length ? `已开启 ${feats.length} 项能力` : '按需开启后台任务');
    document.querySelector('[data-step-sub="4"]').textContent = stOf(4) === 'skipped'
      ? '已跳过'
      : ((data.step4.lbConnected || data.step4.lfmConnected || $('acq-enabled').checked) ? '已配置集成' : 'Scrobble · Jackett（可选）');
    document.querySelector('[data-step-sub="5"]').textContent = (data.preflight && data.preflight.ok) ? '确认目录并开始入库' : '目录需检查挂载';

    const pct = state.step === 5 ? 100 : Math.max(10, Math.round((savedCount / 4) * 100));
    $('rail-progress-text').textContent = `${savedCount} / 4 步已保存`;
    $('rail-progress-bar').style.width = pct + '%';
    $('rail-progress-bar').classList.toggle('is-success', state.step === 5);
  }

  function goToStep(n) {
    state.step = Math.max(1, Math.min(5, n));
    showStepError('');
    document.querySelectorAll('.step-panel').forEach((panel) => {
      const isTarget = Number(panel.dataset.stepPanel) === state.step;
      panel.hidden = !isTarget;
      panel.classList.remove('is-entering');
      if (isTarget) {
        void panel.offsetWidth;
        panel.classList.add('is-entering');
      }
    });
    if (state.step === 3) syncStep3Dependencies();
    if (state.step === 4) syncStep4LastFMReuse();
    if (state.step === 5) renderStep5();
    updateRail();
    const activeBtn = document.querySelector(`.step-item[data-goto-step="${state.step}"]`);
    if (activeBtn && window.matchMedia('(max-width: 900px)').matches) {
      activeBtn.scrollIntoView({ inline: 'center', block: 'nearest' });
    }
    window.scrollTo(0, 0);
  }

  /* ---- Step Event Bindings ---- */
  ['mb-enabled', 'bgm-enabled', 'lastfm-enabled'].forEach((id) => $(id).addEventListener('change', syncStep1Fields));
  $('mb-contact').addEventListener('input', () => {
    if ($('mb-contact').value.trim()) {
      $('mb-contact').classList.remove('is-invalid');
      $('mb-contact-error').hidden = true;
    }
  });
  $('lastfm-key').addEventListener('input', () => {
    if ($('lastfm-key').value.trim()) {
      $('lastfm-key').classList.remove('is-invalid');
      $('lastfm-key-error').hidden = true;
    }
    syncStep4LastFMReuse();
  });

  $('llm-presets').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-preset]');
    if (!btn) return;
    state.preset = btn.dataset.preset;
    const p = PRESETS[state.preset];
    document.querySelectorAll('#llm-presets .chip').forEach((c) => c.classList.toggle('is-selected', c === btn));
    if (p.baseURL) $('llm-base-url').value = p.baseURL;
    $('llm-model').placeholder = p.modelPlaceholder;
    $('llm-review-model').placeholder = p.reviewPlaceholder;
    if (p.defaultModel) $('llm-model').value = p.defaultModel;
    if (p.defaultReview !== undefined) $('llm-review-model').value = p.defaultReview;
    $('llm-key-hint').textContent = p.keyOptional
      ? '本地 Ollama 通常无需真实密钥，留空或填 ollama 即可'
      : '仅存于本地数据库，保存后不回显明文';
    syncStep2Status();
  });

  ['llm-base-url', 'llm-api-key', 'llm-model', 'llm-review-model', 'clear-llm-key', 'ws-provider', 'ws-tavily-mode', 'ws-tavily-keys', 'clear-tavily-keys', 'ws-deepseek-key', 'clear-deepseek-key'].forEach((id) => {
    $(id).addEventListener('input', syncStep2Status);
    $(id).addEventListener('change', syncStep2Status);
  });

  $('capability-presets').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-cap-preset]');
    if (!btn) return;
    const key = btn.dataset.capPreset;
    document.querySelectorAll('#capability-presets .preset-btn').forEach((b) => b.classList.toggle('is-selected', b === btn));
    const llmOK = isLLMConfigured();
    const wsOK = isWebSearchConfigured();
    const analyzerOK = Boolean(data.deployment.analyzerConfigured);
    if (key === 'minimal') {
      ['feat-ai-review', 'feat-alias', 'feat-avatar-ai', 'feat-bio-ai', 'feat-lyrics-trans', 'feat-daily-mix', 'feat-ai-tagging', 'feat-acoustic'].forEach((id) => { $(id).checked = false; });
    } else if (key === 'acg') {
      $('feat-ai-review').checked = llmOK;
      $('feat-review-mode').value = 'recommend';
      $('feat-review-web').checked = llmOK && wsOK;
      $('feat-alias').checked = llmOK && wsOK;
      $('feat-avatar-ai').checked = false;
      $('feat-bio-ai').checked = llmOK;
      $('feat-lyrics-trans').checked = llmOK;
      $('feat-daily-mix').checked = true;
      $('feat-mix-llm').checked = llmOK;
      $('feat-ai-tagging').checked = false;
      $('feat-acoustic').checked = analyzerOK;
    } else if (key === 'full') {
      $('feat-ai-review').checked = llmOK;
      $('feat-review-mode').value = 'auto';
      $('feat-review-web').checked = llmOK && wsOK;
      $('feat-alias').checked = llmOK && wsOK;
      $('feat-avatar-ai').checked = llmOK && wsOK;
      $('feat-bio-ai').checked = llmOK;
      $('feat-lyrics-trans').checked = llmOK;
      $('feat-daily-mix').checked = true;
      $('feat-mix-llm').checked = llmOK;
      $('feat-ai-tagging').checked = llmOK;
      $('feat-acoustic').checked = analyzerOK;
    }
    syncStep3Dependencies();
  });

  ['feat-ai-review', 'feat-daily-mix'].forEach((id) => {
    $(id).addEventListener('change', () => {
      $('sub-ai-review').hidden = !$('feat-ai-review').checked;
      $('sub-daily-mix').hidden = !$('feat-daily-mix').checked;
    });
  });

  $('lb-enabled').addEventListener('change', () => { $('lb-fields').hidden = !$('lb-enabled').checked; });
  $('lfm-scrobble-enabled').addEventListener('change', () => {
    $('lfm-scrobble-fields').hidden = !$('lfm-scrobble-enabled').checked;
    syncStep4LastFMReuse();
  });
  $('acq-enabled').addEventListener('change', () => {
    const on = $('acq-enabled').checked;
    $('acq-fields').hidden = !on;
    $('acq-status-badge').className = on ? 'badge badge-accent' : 'badge';
    $('acq-status-badge').textContent = on ? '已启用' : '未启用';
  });
  $('watch-enabled').addEventListener('change', () => {
    $('watch-interval').disabled = !$('watch-enabled').checked;
  });

  /* ---- Step Persistence Calls ---- */
  function collectStepPayload(step) {
    if (step === 1) {
      return {
        musicbrainzEnabled: $('mb-enabled').checked,
        musicbrainzContact: $('mb-contact').value.trim(),
        musicbrainzAuto: $('mb-auto').checked,
        bangumiEnabled: $('bgm-enabled').checked,
        bangumiAuto: $('bgm-auto').checked,
        lastfmEnabled: $('lastfm-enabled').checked,
        lastfmApiKey: $('lastfm-key').value.trim(),
        bioTargetLanguage: $('bio-lang').value,
        wikipediaEnabled: $('wiki-enabled').checked,
        fanartApiKey: $('fanart-key').value.trim(),
        clearFanartKey: $('clear-fanart-key').checked,
        cacheDays: Number($('meta-cache-days').value) || 30,
        imgBangumi: $('img-bgm').checked,
        imgDeezer: $('img-deezer').checked,
        imgWikipedia: $('img-wiki').checked
      };
    }
    if (step === 2) {
      return {
        baseUrl: $('llm-base-url').value.trim(),
        apiKey: $('llm-api-key').value.trim(),
        clearApiKey: $('clear-llm-key').checked,
        model: $('llm-model').value.trim(),
        reviewModel: $('llm-review-model').value.trim(),
        fallbackModel: $('llm-fallback').value.trim(),
        proxyUrl: $('llm-proxy').value.trim(),
        clearProxy: $('clear-llm-proxy').checked,
        rpm: Number($('llm-rpm').value) || 60,
        concurrency: Number($('llm-concurrency').value) || 4,
        reasoningEffort: $('llm-reasoning').value,
        visionModel: $('llm-vision-model').value.trim(),
        webSearchProvider: $('ws-provider').value,
        tavilyKeys: $('ws-tavily-keys').value,
        tavilyKeysMode: $('ws-tavily-mode').value,
        clearTavilyKeys: $('clear-tavily-keys').checked,
        deepSeekKey: $('ws-deepseek-key').value.trim(),
        clearDeepSeekKey: $('clear-deepseek-key').checked
      };
    }
    if (step === 3) {
      return {
        aiReviewEnabled: $('feat-ai-review').checked,
        aiReviewMode: $('feat-review-mode').value,
        aiReviewWeb: $('feat-review-web').checked,
        aliasEnabled: $('feat-alias').checked,
        avatarWebAiEnabled: $('feat-avatar-ai').checked,
        bioAiEnabled: $('feat-bio-ai').checked,
        lyricsTransEnabled: $('feat-lyrics-trans').checked,
        dailyMixEnabled: $('feat-daily-mix').checked,
        dailyMixLlmTitles: $('feat-mix-llm').checked,
        aiTaggingEnabled: $('feat-ai-tagging').checked,
        acousticEnabled: $('feat-acoustic').checked
      };
    }
    if (step === 4) {
      return {
        lbEnabled: $('lb-enabled').checked,
        lbServerUrl: $('lb-url').value.trim(),
        lbUserToken: $('lb-token').value.trim(),
        lbNowPlaying: $('lb-nowplaying').checked,
        lbLoveSync: $('lb-love').checked,
        lfmScrobbleEnabled: $('lfm-scrobble-enabled').checked,
        lfmApiKey: $('lfm-scrobble-key').value.trim(),
        lfmSecret: $('lfm-secret').value.trim(),
        lfmNowPlaying: $('lfm-nowplaying').checked,
        lfmLoveSync: $('lfm-love').checked,
        acqEnabled: $('acq-enabled').checked,
        jackettUrl: $('acq-jackett-url').value.trim(),
        jackettIndexer: $('acq-jackett-indexer').value.trim(),
        jackettApiKey: $('acq-jackett-key').value.trim(),
        qbUrl: $('acq-qb-url').value.trim(),
        qbUsername: $('acq-qb-user').value.trim(),
        qbPassword: $('acq-qb-pass').value,
        qbCategory: $('acq-qb-cat').value.trim()
      };
    }
    return {};
  }

  async function submitStep(step, skip, btn) {
    showStepError('');
    if (!skip && step === 1) {
      if ($('mb-enabled').checked && !$('mb-contact').value.trim()) {
        $('mb-contact').classList.add('is-invalid');
        $('mb-contact-error').hidden = false;
        $('mb-contact').focus();
        return;
      }
      if ($('lastfm-enabled').checked && !$('lastfm-key').value.trim() && !data.step1.lastfmKeySet) {
        $('lastfm-key').classList.add('is-invalid');
        $('lastfm-key-error').hidden = false;
        $('lastfm-key').focus();
        return;
      }
    }
    if (btn) btn.disabled = true;
    try {
      const payload = skip ? { skip: true } : collectStepPayload(step);
      const res = await apiJSON('POST', `/admin/setup/step/${step}`, payload);
      if (res.bootstrap) {
        hydrateFromBootstrap(res.bootstrap);
      } else if (res.state) {
        data.state = res.state;
      }
      toast(skip ? `已跳过第 ${step} 步，稍后可随时补配` : `第 ${step} 步设置已保存`);
      goToStep(step + 1);
    } catch (err) {
      if (err.field === 'musicbrainzContact') {
        $('mb-contact').classList.add('is-invalid');
        $('mb-contact-error').textContent = err.message;
        $('mb-contact-error').hidden = false;
        $('mb-contact').focus();
      } else if (err.field === 'lastfmApiKey') {
        $('lastfm-key').classList.add('is-invalid');
        $('lastfm-key-error').textContent = err.message;
        $('lastfm-key-error').hidden = false;
        $('lastfm-key').focus();
      } else {
        showStepError(err.message);
      }
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  document.querySelectorAll('[data-goto-step]').forEach((btn) => {
    btn.addEventListener('click', () => goToStep(Number(btn.dataset.gotoStep)));
  });
  document.querySelectorAll('[data-step-prev]').forEach((btn) => {
    btn.addEventListener('click', () => goToStep(Number(btn.dataset.stepPrev) - 1));
  });
  document.querySelectorAll('[data-step-skip]').forEach((btn) => {
    btn.addEventListener('click', () => submitStep(Number(btn.dataset.stepSkip), true, btn));
  });
  document.querySelectorAll('[data-step-save]').forEach((btn) => {
    btn.addEventListener('click', () => submitStep(Number(btn.dataset.stepSave), false, btn));
  });

  /* ---- Inline Async Tests ---- */
  async function runInlineTest(btn, resultEl, url, payload, onSuccess) {
    btn.disabled = true;
    resultEl.className = 'test-result';
    resultEl.innerHTML = '<svg class="icon icon-sm is-spinning" aria-hidden="true"><use href="#i-loader"></use></svg> 正在测试连接...';
    try {
      const res = await apiJSON('POST', url, payload);
      resultEl.className = res.ok ? 'test-result is-ok' : 'test-result is-err';
      resultEl.textContent = res.message || (res.ok ? '连接正常' : '连接失败');
      if (res.ok && onSuccess) onSuccess(res);
    } catch (err) {
      resultEl.className = 'test-result is-err';
      resultEl.textContent = err.message;
    } finally {
      btn.disabled = false;
    }
  }

  $('test-llm-btn').addEventListener('click', () => {
    runInlineTest($('test-llm-btn'), $('test-llm-result'), '/admin/setup/test/llm', {
      baseUrl: $('llm-base-url').value.trim(),
      apiKey: $('llm-api-key').value.trim(),
      model: $('llm-model').value.trim(),
      reviewModel: $('llm-review-model').value.trim(),
      proxyUrl: $('llm-proxy').value.trim(),
      reasoningEffort: $('llm-reasoning').value
    });
  });

  $('test-ws-btn').addEventListener('click', () => {
    runInlineTest($('test-ws-btn'), $('test-ws-result'), '/admin/setup/test/web-search', {
      provider: $('ws-provider').value,
      tavilyKeys: $('ws-tavily-keys').value,
      clearTavilyKeys: $('clear-tavily-keys').checked,
      deepSeekKey: $('ws-deepseek-key').value.trim(),
      clearDeepSeekKey: $('clear-deepseek-key').checked,
      llmBaseUrl: $('llm-base-url').value.trim(),
      llmApiKey: $('llm-api-key').value.trim(),
      clearLlmKey: $('clear-llm-key').checked,
      llmModel: $('llm-model').value.trim(),
      llmProxyUrl: $('llm-proxy').value.trim(),
      clearLlmProxy: $('clear-llm-proxy').checked
    });
  });

  $('test-lb-btn').addEventListener('click', () => {
    runInlineTest($('test-lb-btn'), $('test-lb-result'), '/admin/setup/test/listenbrainz', {
      serverUrl: $('lb-url').value.trim(),
      userToken: $('lb-token').value.trim()
    }, (res) => {
      data.step4.lbConnected = true;
      data.step4.lbUsername = res.username || '';
      data.step4.lbTokenSet = true;
      $('lb-status-badge').className = 'badge badge-success';
      $('lb-status-badge').textContent = '已连接 · ' + data.step4.lbUsername;
    });
  });

  $('lfm-start-auth-btn').addEventListener('click', () => {
    runInlineTest($('lfm-start-auth-btn'), $('lfm-auth-status'), '/admin/setup/test/lastfm/connect', {
      apiKey: $('lfm-scrobble-key').value.trim() || $('lastfm-key').value.trim(),
      apiSecret: $('lfm-secret').value.trim()
    }, (res) => {
      if (res.authUrl) {
        $('lfm-open-auth-link').href = res.authUrl;
        $('lfm-oauth-box').hidden = false;
      }
    });
  });

  $('lfm-complete-auth-btn').addEventListener('click', () => {
    runInlineTest($('lfm-complete-auth-btn'), $('lfm-auth-status'), '/admin/setup/test/lastfm/complete', {}, (res) => {
      data.step4.lfmConnected = true;
      data.step4.lfmUsername = res.username || '';
      $('lfm-oauth-box').hidden = true;
      $('lfm-scrobble-badge').className = 'badge badge-success';
      $('lfm-scrobble-badge').textContent = '已连接 · ' + data.step4.lfmUsername;
      toast('Last.fm 账号连接成功');
    });
  });

  $('test-jackett-btn').addEventListener('click', () => {
    runInlineTest($('test-jackett-btn'), $('test-acq-result'), '/admin/setup/test/jackett', {
      url: $('acq-jackett-url').value.trim(),
      indexer: $('acq-jackett-indexer').value.trim(),
      apiKey: $('acq-jackett-key').value.trim()
    });
  });

  $('test-qb-btn').addEventListener('click', () => {
    runInlineTest($('test-qb-btn'), $('test-acq-result'), '/admin/setup/test/qb', {
      url: $('acq-qb-url').value.trim(),
      username: $('acq-qb-user').value.trim(),
      password: $('acq-qb-pass').value,
      category: $('acq-qb-cat').value.trim()
    });
  });

  $('recheck-dir-btn').addEventListener('click', async () => {
    const btn = $('recheck-dir-btn');
    const st = $('recheck-dir-status');
    btn.disabled = true;
    st.className = 'test-result';
    st.innerHTML = '<svg class="icon icon-sm is-spinning" aria-hidden="true"><use href="#i-loader"></use></svg> 正在预检目录...';
    try {
      const pf = await apiJSON('GET', '/admin/setup/preflight/library');
      renderPreflight(pf);
      updateRail();
      st.className = pf.ok ? 'test-result is-ok' : 'test-result is-err';
      st.textContent = pf.ok ? '目录挂载检测通过' : '目录仍为空或不可读';
    } catch (err) {
      st.className = 'test-result is-err';
      st.textContent = err.message;
    } finally {
      btn.disabled = false;
    }
  });

  /* ---- Complete & Skip ---- */
  async function finishSetup(startScan, btn) {
    showStepError('');
    btn.disabled = true;
    try {
      const res = await apiJSON('POST', '/admin/setup/complete', {
        watchEnabled: $('watch-enabled').checked,
        watchIntervalSeconds: Number($('watch-interval').value) || 60,
        startScan,
        autoEnrich: $('auto-chain-wrap').hidden || $('launch-auto-chain').checked
      });
      window.location.assign(res.redirectTo || '/admin');
    } catch (err) {
      showStepError(err.message);
      btn.disabled = false;
    }
  }

  $('finish-start-scan-btn').addEventListener('click', () => finishSetup(true, $('finish-start-scan-btn')));
  $('finish-no-scan-btn').addEventListener('click', () => finishSetup(false, $('finish-no-scan-btn')));

  $('skip-all-btn').addEventListener('click', async () => {
    const btn = $('skip-all-btn');
    btn.disabled = true;
    try {
      const res = await apiJSON('POST', '/admin/setup/skip', {});
      window.location.assign(res.redirectTo || '/admin');
    } catch (err) {
      showStepError(err.message);
      btn.disabled = false;
    }
  });

  /* ---- Boot ---- */
  hydrateFromBootstrap(data);
  goToStep(state.step);
})();
