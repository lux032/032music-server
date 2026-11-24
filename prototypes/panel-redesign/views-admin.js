import { WORK_REVIEW_DATA, ARTIST_REVIEW_DATA, DAILY_MIXES, ALBUM_TRACKS, COVERS } from './data.js';

const ic = (name, cls = '') => `<svg class="ic ${cls}" aria-hidden="true"><use href="#${name}"/></svg>`;

export function renderAdminView(route, state) {
  switch (route) {
    case 'admin/dashboard': return renderDashboard(state);
    case 'admin/missing': return renderMissing(state);
    case 'admin/work-review': return renderWorkReview(state);
    case 'admin/artist-review': return renderArtistReview(state);
    case 'admin/series': return renderSeries(state);
    case 'admin/enrichment': return renderEnrichment(state);
    case 'admin/analysis-ai': return renderAnalysisAI(state);
    case 'admin/lyrics': return renderLyrics(state);
    case 'admin/daily-mixes': return renderDailyMixes(state);
    case 'admin/sources': return renderSources(state);
    case 'admin/ai-settings': return renderAISettings(state);
    case 'admin/backups': return renderBackups(state);
    case 'admin/security': return renderSecurity(state);
    default: return renderDashboard(state);
  }
}

// 1. 控制台总览 (/admin)
function renderDashboard(state) {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>控制台总览</h1>
          <p><code>/mnt/nas/music/ACG-Lossless</code> · 本地标签优先，所有编辑与增强保存在覆盖层，源音频文件始终只读。</p>
        </div>
        <div class="admin-header-actions">
          <span class="tag ok"><span class="status-dot" style="width:6px;height:6px;box-shadow:none"></span> 服务正常 · SQLite WAL · v0.18.4 · 运行 12 天</span>
        </div>
      </header>

      <section class="kpi-grid">
        <button type="button" class="kpi-card" data-nav="albums">
          <span class="kpi-label">专辑 ${ic('i-album', 'xs')}</span>
          <strong class="kpi-val tnum">1,751</strong>
          <span class="kpi-sub">本周新增 12 张</span>
        </button>
        <button type="button" class="kpi-card" data-nav="artist">
          <span class="kpi-label">歌手与幕后 ${ic('i-users', 'xs')}</span>
          <strong class="kpi-val tnum">2,418</strong>
          <span class="kpi-sub">已确认 MBID 84%</span>
        </button>
        <button type="button" class="kpi-card" data-nav="albums">
          <span class="kpi-label">歌曲 ${ic('i-music', 'xs')}</span>
          <strong class="kpi-val tnum">17,595</strong>
          <span class="kpi-sub">FLAC 无损占比 92%</span>
        </button>
        <button type="button" class="kpi-card" data-nav="admin/missing">
          <span class="kpi-label">音频文件 / 缺失 ${ic('i-folder-x', 'xs')}</span>
          <strong class="kpi-val tnum">18,042</strong>
          <span class="kpi-sub" style="color:var(--warn)">3 个缺失文件待处理 →</span>
        </button>
      </section>

      <div class="admin-split">
        <div style="display:flex;flex-direction:column;gap:16px">
          <!-- Scanner + Auto Watch Console -->
          <section class="card">
            <div class="card-head">
              <div>
                <div class="card-kicker">曲库扫描引擎</div>
                <h2 class="card-title">扫描状态：${state.scanning ? '正在增量扫描…' : '空闲 · 已就绪'}</h2>
                <p class="card-desc tnum">最后更新：2026-11-24 06:42:10 · 发现 18,042 · 已处理 18,039 · 跳过 17,990 · 失败 0</p>
              </div>
              <div style="display:flex;gap:8px;flex-wrap:wrap">
                <button type="button" class="btn btn-primary btn-sm" data-action="trigger-scan" data-val="incremental">${ic('i-refresh', 'xs')} 增量扫描</button>
                <button type="button" class="btn btn-secondary btn-sm" data-action="trigger-scan" data-val="full">完整重扫</button>
              </div>
            </div>
            <div class="progress-track"><div class="progress-fill ${state.scanning ? '' : 'ok'}" style="width:${state.scanning ? 64 : 100}%"></div></div>
            <p class="mono ell" style="font-size:12px;color:var(--text-faint);margin-top:8px">${state.scanning ? '正在读取：ヨルシカ / 2023 - 幻燈 / 03. 斜陽.flac' : '上次扫描耗时 4.2 秒 · 无读取异常'}</p>

            <!-- Auto Watch -->
            <div class="switch-row" style="margin-top:16px">
              <div>
                <strong>自动入库监控</strong>
                <small>已开启 · 每 5 分钟检查音乐目录变化并自动增量扫描（当前为管理页覆盖，环境变量默认：关闭）</small>
              </div>
              <div style="display:flex;align-items:center;gap:8px;flex:none">
                <select class="select" style="width:116px;height:34px;padding:0 10px;font-size:13px" aria-label="检查间隔">
                  <option>1 分钟</option><option selected>5 分钟</option><option>15 分钟</option><option>30 分钟</option>
                </select>
                <input type="checkbox" class="toggle" checked aria-label="开启自动入库">
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已保存自动入库监控设置">保存</button>
                <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复环境变量默认值">恢复默认</button>
              </div>
            </div>

            <!-- Legacy Creators Split Disclosure -->
            <details class="disclosure" style="margin-top:12px">
              <summary>旧文件兼容：按逗号拆分幕后署名（作词/作曲/编曲/制作） ${ic('i-down', 'xs')}</summary>
              <p style="font-size:12.5px;color:var(--text-dim);margin-bottom:10px">仅本次完整扫描按英文/中文逗号拆分旧标签署名；名字本身含逗号可能误拆，请先在「数据库备份与恢复」创建快照。</p>
              <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap">
                <label style="display:flex;align-items:center;gap:8px;font-size:12.5px"><input type="checkbox" id="legacy-confirm"> 我已备份并接受旧署名可能误拆的风险</label>
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已触发兼容旧幕后署名的完整重扫">兼容旧幕后署名并完整重扫</button>
              </div>
            </details>
          </section>

          <!-- Unified Pipeline & Review Hub (replaces hidden dashboard cards) -->
          <section>
            <div class="section-head" style="margin-bottom:12px">
              <h2 style="font-size:16px">待办审核与后台流水线</h2>
              <span class="section-rule"></span>
              <span class="section-sub">全部管理能力均已在左侧导航按域归类</span>
            </div>
            <div class="hub-grid">
              <button type="button" class="hub-tile" data-nav="admin/work-review">
                <span class="hub-ic">${ic('i-works', 'sm')}</span>
                <div class="ell"><strong>作品关联审核</strong><small>24 条待审 · AI 预审就绪</small></div>
              </button>
              <button type="button" class="hub-tile" data-nav="admin/artist-review">
                <span class="hub-ic">${ic('i-users', 'sm')}</span>
                <div class="ell"><strong>艺术家与别名审核</strong><small>18 条待审 · 含署名拆分与头像</small></div>
              </button>
              <button type="button" class="hub-tile" data-nav="admin/enrichment">
                <span class="hub-ic">${ic('i-sparkle', 'sm')}</span>
                <div class="ell"><strong>元数据自动增强</strong><small>Bangumi 对齐 · 缺失海报 14</small></div>
              </button>
              <button type="button" class="hub-tile" data-nav="admin/analysis-ai">
                <span class="hub-ic">${ic('i-wave', 'sm')}</span>
                <div class="ell"><strong>声学分析与 AI 打标</strong><small>声学 94% · 打标 88% 运行中</small></div>
              </button>
              <button type="button" class="hub-tile" data-nav="admin/lyrics">
                <span class="hub-ic">${ic('i-lyrics', 'sm')}</span>
                <div class="ell"><strong>歌词与翻译</strong><small>在线缓存 4,820 · 翻译 1,290</small></div>
              </button>
              <button type="button" class="hub-tile" data-nav="admin/daily-mixes">
                <span class="hub-ic">${ic('i-sliders', 'sm')}</span>
                <div class="ell"><strong>每日推荐调试</strong><small>今日已生成 4 个歌单</small></div>
              </button>
            </div>
          </section>
        </div>

        <!-- Right Column: MB Release Group Identity + Backup Health -->
        <div style="display:flex;flex-direction:column;gap:16px">
          <section class="card">
            <div class="card-head">
              <div>
                <div class="card-kicker">发现新音乐前置身份</div>
                <h2 class="card-title">MusicBrainz Release Group</h2>
              </div>
              <span class="tag ok">身份已就绪</span>
            </div>
            <p class="tnum" style="font-size:13.5px;color:var(--text-dim)">已重读标签：18,042 / 18,042 · 发现 1,684 个独立发行组</p>
            <p style="font-size:12.5px;color:var(--text-faint);margin-top:8px">手动重读全部音频嵌入标签后就绪，不使用旧缓存回填。</p>
            <details class="disclosure" style="margin-top:12px">
              <summary>查看读取失败的文件（2） ${ic('i-down', 'xs')}</summary>
              <ul class="mono" style="padding-left:16px;font-size:11.5px;color:var(--text-dim);display:grid;gap:6px">
                <li>Extras/bonus_commentary.wav · NO_MBID · 缺少 Release Group 标签</li>
                <li>Doujin/C102/track04.mp3 · TAG_ERR · 非标准 ID3v2 帧</li>
              </ul>
            </details>
            <div class="card-foot">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已启动全量标签重读与身份重建">重建身份（全量扫描）</button>
            </div>
          </section>

          <section class="card">
            <div class="card-head">
              <div>
                <div class="card-kicker">系统与快照健康</div>
                <h2 class="card-title">自动备份与安全</h2>
              </div>
              <span class="tag ok">正常</span>
            </div>
            <dl class="facts-list" style="padding-top:0">
              <div class="fact-row"><dt>上次快照</dt><dd class="tnum">今天 02:30 · 成功 (14.1 MB)</dd></div>
              <div class="fact-row"><dt>两步验证 (2FA)</dt><dd><span class="tag ok">已启用</span></dd></div>
              <div class="fact-row"><dt>登录封禁</dt><dd>0 条封禁 · 2 条白名单</dd></div>
            </dl>
            <div class="card-foot">
              <button type="button" class="btn btn-secondary btn-xs" data-nav="admin/backups">管理备份</button>
              <button type="button" class="btn btn-ghost btn-xs" data-nav="admin/security">账号与安全 →</button>
            </div>
          </section>
        </div>
      </div>
    </div>
  `;
}

// 2. 缺失文件与清理 (/admin/library/missing)
function renderMissing() {
  const rows = [
    { id: 1, title: 'Bonus Track (Live)', meta: 'Aimer · Walpurgis', path: 'Aimer/Walpurgis/15. Bonus.flac', state: '已隐藏', since: '2026-11-22 08:12' },
    { id: 2, title: '夜に駆ける (Old Rip)', meta: 'YOASOBI · THE BOOK', path: 'YOASOBI/THE BOOK/01_old.mp3', state: '仍有其他文件', since: '2026-11-19 19:40' },
    { id: 3, title: '（未关联歌曲）', meta: '—', path: 'Temp/rip_test.wav', state: '已隐藏', since: '2026-11-15 11:04' },
  ];
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>缺失文件与清理</h1>
          <p>3 个音频文件在上次扫描时已不在音乐目录中。仅剩缺失文件的歌曲/专辑已自动对客户端隐藏，文件放回后重扫将自动恢复且保留收藏与播放记录。</p>
        </div>
      </header>

      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-kicker">自动清理策略 · MUSIC_SERVER_PURGE_MISSING</div>
            <h2 class="card-title">扫描后如何处理缺失文件</h2>
            <p class="card-desc">疑似磁盘未挂载（超过一半文件消失）的扫描绝不会触发自动删除。</p>
          </div>
        </div>
        <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
          <select class="select" style="max-width:340px">
            <option selected>仅隐藏，保留记录等待手动清理（推荐）</option>
            <option>缺失满 7 天后自动永久删除</option>
            <option>缺失满 30 天后自动永久删除</option>
            <option>每次完整扫描后立即删除</option>
          </select>
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存缺失文件清理策略">保存策略</button>
          <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已恢复环境变量默认策略">恢复环境变量默认</button>
        </div>
      </section>

      <section class="card">
        <div class="card-head">
          <div><div class="card-kicker">待处理列表</div><h2 class="card-title">缺失音频文件（3）</h2></div>
        </div>
        <div class="data-table-wrap">
          <table class="data-table">
            <thead><tr><th style="width:36px"><input type="checkbox" checked></th><th>曲目与路径</th><th>当前状态</th><th>首次缺失时间</th></tr></thead>
            <tbody>
              ${rows.map((r) => `
                <tr>
                  <td><input type="checkbox" checked></td>
                  <td><strong>${r.title}</strong><small>${r.meta} · <code>${r.path}</code></small></td>
                  <td><span class="tag ${r.state === '已隐藏' ? 'warn' : 'outline'}">${r.state}</span></td>
                  <td class="tnum" style="color:var(--text-faint)">${r.since}</td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
        <div class="card-foot">
          <details class="disclosure danger-disclosure">
            <summary>删除勾选的文件…</summary>
            <p style="font-size:12.5px;margin-bottom:10px">没有其他文件的歌曲会连同收藏、播放记录与歌单条目一并删除，不可撤销。</p>
            <button type="button" class="btn btn-danger btn-sm" data-action="toast" data-msg="已永久删除勾选的缺失文件记录">是，删除勾选项</button>
          </details>
          <details class="disclosure danger-disclosure">
            <summary>全部删除…</summary>
            <p style="font-size:12.5px;margin-bottom:10px">永久清理全部 3 个缺失文件记录及由此产生的空专辑/歌手。</p>
            <button type="button" class="btn btn-danger btn-sm" data-action="toast" data-msg="已清空全部缺失文件记录">是，全部删除</button>
          </details>
        </div>
      </section>
    </div>
  `;
}

// 3. 作品关联审核 (/admin/work-review)
function renderWorkReview(state) {
  const tab = state.workReviewTab;
  const resolved = state.resolvedItems;
  const list = WORK_REVIEW_DATA[tab] || [];

  return `
    <div class="admin-wrap wide">
      <header class="admin-header">
        <div>
          <h1>作品关联审核</h1>
          <p>审核专辑、曲目与 Bangumi 作品的关联候选、作品身份对齐及系列合并建议；人工抑制记录永久生效。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-secondary btn-sm" data-nav="admin/ai-settings">${ic('i-cpu', 'xs')} AI 自治策略</button>
          <button type="button" class="btn btn-secondary btn-sm" data-nav="admin/enrichment">${ic('i-sparkle', 'xs')} 启动自动增强</button>
        </div>
      </header>

      <!-- AI Pre-Review Control Strip -->
      <div class="ai-banner">
        <div class="ai-banner-info">
          <span class="ai-banner-icon">${ic('i-sparkle', 'sm')}</span>
          <div>
            <strong style="font-size:14.5px">AI 初审助手 · 分级自治已开启（≥88 分自动确认）</strong>
            <div style="font-size:12.5px;color:var(--text-dim);margin-top:2px">上轮预审处理 64 项：自动确认 40 项 · 留待人工 24 项 · 安全铁律：AI 永不自动写入永久抑制</div>
          </div>
        </div>
        <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
          <label style="font-size:12.5px;display:inline-flex;align-items:center;gap:6px"><input type="checkbox"> 强制重评已评估项</label>
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已启动 AI 存量待审预审任务">AI 处理存量待审</button>
        </div>
      </div>

      <details class="disclosure" style="margin-bottom:18px">
        <summary>AI 最近自动处理记录（3 条，可一键撤销退回待审） ${ic('i-down', 'xs')}</summary>
        <div style="display:flex;flex-direction:column;gap:8px">
          <div class="cand-item">
            <span><strong>《THE BOOK》→ BEASTARS 第 2 期 (OP)</strong> <span class="tag ok" style="margin-left:6px">95 分自动确认</span></span>
            <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已撤销该条 AI 自动决策，已退回待审队列">${ic('i-undo', 'xs')} 撤销</button>
          </div>
          <div class="cand-item">
            <span><strong>《LANDER》→ 剧场版 鬼灭之刃 无限列车篇 (主题曲)</strong> <span class="tag ok" style="margin-left:6px">97 分自动确认</span></span>
            <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已撤销该条 AI 自动决策，已退回待审队列">${ic('i-undo', 'xs')} 撤销</button>
          </div>
        </div>
      </details>

      <div class="admin-tabs">
        ${[
          ['albums', '专辑候选', 12],
          ['tracks', '曲目候选', 7],
          ['works', '作品对齐候选 · 身份匹配', 3],
          ['series', '系列建议', 2],
        ].map(([k, l, c]) => `<button type="button" class="admin-tab ${tab === k ? 'active' : ''}" data-action="work-review-tab" data-val="${k}">${l} <span class="tab-cnt tnum">${c}</span></button>`).join('')}
      </div>

      ${tab !== 'series' ? `
        <div class="bulk-bar" style="background:var(--surface);border-color:var(--border)">
          <label style="display:inline-flex;align-items:center;gap:8px;font-size:13px;font-weight:600"><input type="checkbox" checked> 全选本页 (${list.length})</label>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已批量接受所选高置信度候选">批量接受</button>
            <button type="button" class="btn btn-danger-soft btn-sm" data-action="toast" data-msg="已批量拒绝并写入永久抑制">批量拒绝并抑制</button>
          </div>
        </div>
      ` : ''}

      <div>
        ${tab === 'series' ? list.map((s) => `
          <article class="review-card ${resolved.has(s.id) ? 'resolved' : ''}">
            <div class="review-head">
              <div><strong>建议合并为系列：《${s.proposedName}》</strong><div style="font-size:12.5px;color:var(--accent-text);margin-top:3px">${s.aiSummary}</div></div>
              <div style="display:flex;gap:8px">
                <button type="button" class="btn btn-primary btn-sm" data-action="resolve-item" data-id="${s.id}" data-msg="已接受系列建议：《${s.proposedName}》">接受建议</button>
                <button type="button" class="btn btn-secondary btn-sm" data-action="resolve-item" data-id="${s.id}" data-msg="已拒绝该系列建议">拒绝</button>
              </div>
            </div>
            <div class="chip-row" style="margin-bottom:12px">${s.members.map((m) => `<span class="tag">${m}</span>`).join('')}</div>
            <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
              <label style="font-size:12.5px;color:var(--text-dim)">合并后系列名：</label>
              <input class="input" style="max-width:260px;height:32px" value="${s.proposedName}">
            </div>
          </article>
        `).join('') : list.map((item) => `
          <article class="review-card ${resolved.has(item.id) ? 'resolved' : ''}">
            <div class="review-head">
              <label style="display:inline-flex;align-items:center;gap:8px;font-weight:650">
                <input type="checkbox" checked>
                <span>${item.localTitle}</span>
                <span class="tag accent">${item.aiSummary}</span>
              </label>
              <div style="display:flex;gap:6px;flex-wrap:wrap">
                <button type="button" class="btn btn-primary btn-xs" data-action="resolve-item" data-id="${item.id}" data-msg="已接受《${item.localTitle}》勾选候选">只接受勾选 (${item.candidates.filter((c) => c.aiPick).length})</button>
                <button type="button" class="btn btn-secondary btn-xs" data-action="resolve-item" data-id="${item.id}" data-msg="已忽略当前全部候选">以上都不是</button>
              </div>
            </div>
            <div class="review-compare">
              <div class="review-source">
                <img src="${item.cover}" alt="">
                <div style="min-width:0">
                  <strong style="display:block;font-size:15px">${item.localTitle}</strong>
                  <small style="display:block;color:var(--text-dim);margin-top:4px">${item.localMeta}</small>
                  <div style="display:flex;gap:6px;margin-top:10px">
                    <input class="input" style="height:32px;padding:0 10px;font-size:12.5px;width:168px" placeholder="手动指定 Bangumi ID">
                    <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已按指定 Bangumi 条目对齐">指定对齐</button>
                  </div>
                </div>
              </div>
              <div class="review-arrow">${ic('i-right', 'sm')}</div>
              <div class="cand-list">
                ${item.candidates.map((c) => `
                  <div class="cand-item ${c.aiPick ? 'ai-pick' : ''}">
                    <div class="cand-item-main">
                      <input type="checkbox" ${c.aiPick ? 'checked' : ''} style="accent-color:var(--accent)">
                      <img class="poster" src="${c.poster}" alt="">
                      <div class="ell">
                        <strong class="ell">${c.title} ${c.aiPick ? '<span class="tag accent" style="margin-left:5px">AI 推荐 ' + c.score + '分</span>' : '<span class="tag outline" style="margin-left:5px">' + c.score + '分</span>'}</strong>
                        <small class="ell">${c.cn} · ${c.meta}</small>
                      </div>
                    </div>
                    <div class="cand-actions">
                      <select class="select" style="height:32px;width:104px;padding:0 10px;font-size:13px">
                        <option ${c.role === 'OP' ? 'selected' : ''}>OP</option>
                        <option ${c.role === 'ED' ? 'selected' : ''}>ED</option>
                        <option ${c.role === '主题曲' ? 'selected' : ''}>主题曲</option>
                        <option>插曲</option><option>角色歌</option><option>原声集 OST</option>
                      </select>
                      <button type="button" class="btn btn-primary btn-xs" data-action="resolve-item" data-id="${item.id}" data-msg="已确认关联《${c.title}》">接受</button>
                      <button type="button" class="btn btn-ghost btn-xs" data-action="resolve-item" data-id="${item.id}" data-msg="已拒绝并抑制《${c.title}》">拒绝并抑制</button>
                    </div>
                  </div>
                `).join('')}
              </div>
            </div>
          </article>
        `).join('')}
      </div>
    </div>
  `;
}

// 4. 艺术家与身份审核 (统一收口: match-review + artist-aliases + 关系候选 + merge-history!)
function renderArtistReview(state) {
  const tab = state.artistReviewTab;
  const resolved = state.resolvedItems;

  return `
    <div class="admin-wrap wide">
      <header class="admin-header">
        <div>
          <h1>艺术家与身份审核</h1>
          <p>将原本分散在 4 个页面的艺术家匹配、复合署名拆分、待审头像、中文别名补全、关系候选与合并历史统一收口到一个治理工作台。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已启动扫描未检查的艺术家">扫描未检查艺术家</button>
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已启动全局头像与简介补全任务">头像/简介补全</button>
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已启动 AI 艺术家预审与复合署名分析">${ic('i-sparkle', 'xs')} AI 预审</button>
        </div>
      </header>

      <div class="admin-tabs">
        ${[
          ['identities', '待审身份', 8],
          ['credits', '复合署名拆分', 2],
          ['avatars', '待审头像', 3],
          ['aliases', '中文别名补全', 3],
          ['relations', '艺术家关系候选', 2],
          ['merges', '合并历史与回退', 3],
        ].map(([k, l, c]) => `<button type="button" class="admin-tab ${tab === k ? 'active' : ''}" data-action="artist-review-tab" data-val="${k}">${l} <span class="tab-cnt tnum">${c}</span></button>`).join('')}
      </div>

      ${tab === 'identities' ? `
        <div class="chip-row" style="margin-bottom:18px">
          <button type="button" class="chip active">全部 (8)</button>
          <button type="button" class="chip">AI 推荐采纳 (5)</button>
          <button type="button" class="chip">AI 建议拒绝 (1)</button>
          <button type="button" class="chip">不确定 / 未评估 (1)</button>
          <button type="button" class="chip">存在身份冲突 (1)</button>
        </div>
        ${ARTIST_REVIEW_DATA.identities.map((it) => `
          <article class="review-card ${resolved.has(it.id) ? 'resolved' : ''}">
            <div class="review-head">
              <div><strong>${it.name}</strong><span style="margin-left:10px;font-size:12.5px;color:var(--text-dim)">${it.meta}</span></div>
              <button type="button" class="btn btn-secondary btn-xs" data-action="resolve-item" data-id="${it.id}" data-msg="已全部拒绝 ${it.name} 的待审候选">全部拒绝</button>
            </div>
            <div class="cand-list">
              ${it.candidates.map((c) => `
                <div class="cand-item ${c.aiPick ? 'ai-pick' : ''}">
                  <div class="cand-item-main">
                    <div class="ell">
                      <strong>${c.name} <span class="tag ${c.aiPick ? 'accent' : 'outline'}" style="margin-left:6px">${c.source} · ${c.score}分</span></strong>
                      <small>${c.detail}</small>
                      ${c.conflict ? `<div style="margin-top:4px"><span class="tag warn">冲突：${c.conflict}</span></div>` : ''}
                    </div>
                  </div>
                  <div class="cand-actions">
                    ${c.conflict
                      ? `<button type="button" class="btn btn-primary btn-xs" data-action="resolve-item" data-id="${it.id}" data-msg="已核对身份冲突并合并歌手">核对并合并</button>`
                      : `<button type="button" class="btn btn-primary btn-xs" data-action="resolve-item" data-id="${it.id}" data-msg="已确认绑定身份：${c.name}">确认${c.aiPick ? '（AI 推荐）' : ''}</button>`}
                    <button type="button" class="btn btn-ghost btn-xs" data-action="resolve-item" data-id="${it.id}" data-msg="已拒绝候选 ${c.name}">拒绝</button>
                  </div>
                </div>
              `).join('')}
            </div>
          </article>
        `).join('')}
      ` : ''}

      ${tab === 'credits' ? `
        ${ARTIST_REVIEW_DATA.credits.map((cs) => `
          <article class="review-card ${resolved.has(cs.id) ? 'resolved' : ''}">
            <div class="review-head">
              <div><strong>原始复合署名：<code>${cs.raw}</code></strong><span class="tag outline" style="margin-left:8px">涉及 ${cs.tracks} 首曲目</span></div>
              <button type="button" class="btn btn-primary btn-sm" data-action="resolve-item" data-id="${cs.id}" data-msg="已一键应用复合署名拆分：${cs.split.join(' + ')}">一键应用拆分</button>
            </div>
            <div class="chip-row" style="margin-bottom:8px">
              <span style="font-size:12.5px;color:var(--text-dim)">拆分预览：</span>
              ${cs.split.map((p) => `<span class="tag accent">${p}</span>`).join('<span>+</span>')}
            </div>
            <p style="font-size:12.5px;color:var(--text-faint)">${cs.reason}</p>
          </article>
        `).join('')}
      ` : ''}

      ${tab === 'avatars' ? `
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px;flex-wrap:wrap;gap:10px">
          <span style="font-size:13px;color:var(--text-dim)">高置信度已自动入库，以下为中低置信度待人工确认候选头像。</span>
          <button type="button" class="btn btn-secondary btn-sm" data-drawer="avatar-strategy">${ic('i-sliders', 'xs')} 头像图源与 VLM 策略设置</button>
        </div>
        <div class="admin-grid-3">
          ${ARTIST_REVIEW_DATA.avatars.map((av) => `
            <article class="card ${resolved.has(av.id) ? 'resolved' : ''}" style="padding:16px">
              <img src="${av.img}" alt="${av.artist}" style="width:100%;aspect-ratio:1;object-fit:cover;border-radius:8px;margin-bottom:12px">
              <strong>${av.artist}</strong>
              <p class="card-desc">${av.source} · ${av.vlm}</p>
              <div style="display:flex;gap:8px;margin-top:12px">
                <button type="button" class="btn btn-primary btn-sm" style="flex:1" data-action="resolve-item" data-id="${av.id}" data-msg="已采纳 ${av.artist} 的头像">采纳</button>
                <button type="button" class="btn btn-secondary btn-sm" data-action="resolve-item" data-id="${av.id}" data-msg="已拒绝候选头像">拒绝</button>
              </div>
            </article>
          `).join('')}
        </div>
      ` : ''}

      ${tab === 'aliases' ? `
        <div class="admin-split">
          <div style="display:flex;flex-direction:column;gap:14px">
            <section class="card">
              <div class="card-head">
                <div><div class="card-kicker">中文别名补全进度</div><h2 class="card-title tnum">已覆盖 1,892 / 2,418 位歌手（78%）</h2></div>
                <div style="display:flex;gap:8px">
                  <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已启动存量歌手中文别名匹配">开始存量匹配</button>
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已暂停自动补全">暂停</button>
                </div>
              </div>
              <div class="progress-track"><div class="progress-fill" style="width:78%"></div></div>
            </section>
            ${ARTIST_REVIEW_DATA.aliases.map((al) => `
              <article class="card ${resolved.has(al.id) ? 'resolved' : ''}" style="padding:16px">
                <div class="card-head" style="margin-bottom:8px">
                  <div><strong>${al.artist}</strong> → <input class="input" style="display:inline-block;width:160px;height:30px;margin-left:6px" value="${al.proposed}"></div>
                  <span class="tag ${al.status === 'accepted' ? 'ok' : 'warn'}">${al.status === 'accepted' ? '已生效' : '待审核'}</span>
                </div>
                <p style="font-size:12.5px;color:var(--text-dim)">${al.source}</p>
                <details class="disclosure" style="margin-top:10px"><summary>查看来源依据 ${ic('i-down', 'xs')}</summary><p style="font-size:12.5px">${al.evidence}</p></details>
                ${al.status === 'pending' ? `
                  <div style="display:flex;gap:8px;margin-top:12px">
                    <button type="button" class="btn btn-primary btn-xs" data-action="resolve-item" data-id="${al.id}" data-msg="已接受并生效中文别名：${al.proposed}">接受并生效</button>
                    <button type="button" class="btn btn-secondary btn-xs" data-action="resolve-item" data-id="${al.id}" data-msg="已拒绝别名：${al.proposed}">拒绝此名称</button>
                  </div>
                ` : ''}
              </article>
            `).join('')}
          </div>
          <section class="card">
            <div class="card-kicker">自动补全与联网配置</div>
            <h2 class="card-title" style="margin-bottom:14px">DeepSeek 联网密钥与限流</h2>
            <div class="form-stack">
              <div class="switch-row"><div><strong>新歌手自动补全</strong><small>入库新外语歌手时自动联网核验中文译名</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="field"><label>DeepSeek 联网 API Key</label><input class="input" type="password" placeholder="已设置（供别名补全与 AI 初审联网补证复用）"></div>
              <div class="form-grid">
                <div class="field"><label>单轮搜索上限</label><input class="input" type="number" value="3"></div>
                <div class="field"><label>单轮网页读取</label><input class="input" type="number" value="2"></div>
                <div class="field"><label>单轮模型调用</label><input class="input" type="number" value="4"></div>
                <div class="field"><label>每分钟 RPM</label><input class="input" type="number" value="20"></div>
              </div>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存中文别名补全设置">保存设置</button>
            </div>
          </section>
        </div>
      ` : ''}

      ${tab === 'relations' ? `
        ${ARTIST_REVIEW_DATA.relations.map((r) => `
          <article class="card ${resolved.has(r.id) ? 'resolved' : ''}">
            <div class="cand-item" style="border:0;padding:0;background:transparent">
              <div>
                <strong>${r.artist} <span style="color:var(--text-faint);margin:0 6px">↔</span> ${r.related}</strong>
                <div style="margin-top:4px;font-size:12.5px;color:var(--text-dim)">${r.source} · <code>${r.type}</code> (${r.direction}) · 置信度 ${r.score}</div>
              </div>
              <div class="cand-actions">
                <button type="button" class="btn btn-primary btn-sm" data-action="resolve-item" data-id="${r.id}" data-msg="已确认艺术家关系">确认关系</button>
                <button type="button" class="btn btn-secondary btn-sm" data-action="resolve-item" data-id="${r.id}" data-msg="已拒绝艺术家关系">拒绝</button>
              </div>
            </div>
          </article>
        `).join('')}
      ` : ''}

      ${tab === 'merges' ? `
        <section class="card">
          <div class="card-head"><div><div class="card-kicker">操作审计</div><h2 class="card-title">合并历史与回退</h2><p class="card-desc">回退只撤销对应合并操作创建的关系，不覆盖合并之后新增的曲目或资料。</p></div></div>
          <div class="data-table-wrap">
            <table class="data-table">
              <thead><tr><th>#</th><th>合并方向</th><th>影响范围</th><th>状态</th><th>时间</th><th>操作</th></tr></thead>
              <tbody>
                ${ARTIST_REVIEW_DATA.merges.map((m) => `
                  <tr>
                    <td class="tnum">#${m.id}</td>
                    <td><strong>${m.src} → ${m.dst}</strong></td>
                    <td>${m.count}</td>
                    <td><span class="tag ${m.status === 'merged' ? 'accent' : 'outline'}">${m.status === 'merged' ? '已合并' : '已回退'}</span></td>
                    <td class="tnum" style="color:var(--text-faint)">${m.at}</td>
                    <td>${m.status === 'merged' ? `<button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已回退合并 #${m.id}：${m.src} → ${m.dst}">回退合并</button>` : '<span style="color:var(--text-faint);font-size:12px">—</span>'}</td>
                  </tr>
                `).join('')}
              </tbody>
            </table>
          </div>
        </section>
      ` : ''}
    </div>
  `;
}

// 5. 系列管理 (/admin/series + /admin/series/:id)
function renderSeries() {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div><h1>系列管理</h1><p>把同一 IP 的多季动画、剧场版与游戏组织为系列，用于首页「作品聚焦」与详情页跨季漫游。</p></div>
      </header>
      <div class="admin-split">
        <div style="display:flex;flex-direction:column;gap:16px">
          <section class="card">
            <div class="card-head"><div><div class="card-kicker">当前系列详情</div><h2 class="card-title">鬼灭之刃（3 部成员作品）</h2></div></div>
            <div style="display:flex;gap:8px;margin-bottom:16px">
              <input class="input" value="鬼灭之刃" aria-label="系列名">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已重命名系列">重命名</button>
            </div>
            <div class="cand-list">
              ${['鬼滅の刃 竈門炭治郎 立志編 · 2019', '劇場版 鬼滅の刃 無限列車編 · 2020', '鬼滅の刃 遊郭編 · 2021'].map((m) => `
                <div class="cand-item">
                  <strong>${m}</strong>
                  <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已将《${m}》移出系列">移出</button>
                </div>
              `).join('')}
            </div>
            <div style="display:flex;gap:8px;margin-top:14px">
              <input class="input" placeholder="搜索作品加入本系列…">
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已将作品加入系列">加入系列</button>
            </div>
            <div class="card-foot">
              <details class="disclosure" style="flex:1">
                <summary>合并其他系列到本系列…</summary>
                <div class="form-stack">
                  <input class="input" placeholder="搜索要并入的系列…">
                  <select class="select"><option>自动命名（按规则）</option><option>保留《鬼灭之刃》</option><option>保留被并入系列的名字</option></select>
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已执行系列合并">合并到本系列</button>
                </div>
              </details>
              <details class="disclosure danger-disclosure">
                <summary>解散系列…</summary>
                <p style="font-size:12px;margin-bottom:8px">解散仅解除分组，不会删除成员作品。</p>
                <button type="button" class="btn btn-danger btn-xs" data-action="toast" data-msg="已解散系列">确认解散</button>
              </details>
            </div>
          </section>
        </div>

        <section class="card">
          <div class="card-kicker">新建系列</div>
          <h2 class="card-title" style="margin-bottom:14px">创建新系列</h2>
          <div class="form-stack">
            <div class="field"><label>系列名（可选，留空按首部作品自动命名）</label><input class="input" placeholder="例如：某科学的超电磁炮"></div>
            <div class="field"><label>选择作品 1</label><input class="input" placeholder="搜索已有作品…"></div>
            <div class="field"><label>第二部作品（可选）</label><input class="input" placeholder="搜索已有作品…"></div>
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已创建新系列">创建系列</button>
          </div>
        </section>
      </div>
    </div>
  `;
}

// 6. 元数据自动增强 (/admin/enrichment)
function renderEnrichment() {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>元数据自动增强</h1>
          <p>以 Bangumi 为来源将专辑、曲目与作品对齐到动画/游戏条目；高置信度自动确认，其余进入作品关联审核。</p>
        </div>
        <div class="admin-header-actions">
          <select class="select" style="width:208px;height:36px;padding:0 10px">
            <option>全部（专辑、曲目及作品）</option>
            <option>动画专辑</option><option>曲目</option><option>作品对齐与系列</option><option>单个专辑</option><option>单个作品</option>
          </select>
          <input class="input" style="width:100px;height:36px" placeholder="目标 ID">
          <label style="font-size:12.5px;display:inline-flex;align-items:center;gap:6px"><input type="checkbox"> 强制刷新</label>
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已启动元数据自动增强任务">启动增强</button>
        </div>
      </header>

      <div class="admin-grid-2" style="margin-bottom:18px">
        <div class="card" style="padding:16px 18px;display:flex;align-items:center;justify-content:space-between;gap:12px">
          <div><div class="card-kicker">作品海报缓存</div><strong class="tnum">已缓存 719 / 733 · 缺失 14 张</strong></div>
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已启动缺失作品海报补全">补全缺失海报</button>
        </div>
        <div class="card" style="padding:16px 18px;display:flex;align-items:center;justify-content:space-between;gap:12px">
          <div><div class="card-kicker">关联待审队列</div><strong class="tnum">24 条待审（专辑 12 · 曲目 7 · 作品 5）</strong></div>
          <button type="button" class="btn btn-primary btn-sm" data-nav="admin/work-review">去审核 →</button>
        </div>
      </div>

      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-kicker">当前运行任务</div>
            <h2 class="card-title">#48 · 全部（专辑、曲目及作品） <span class="tag accent" style="margin-left:8px">运行中</span></h2>
          </div>
          <div style="display:flex;gap:8px">
            <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已暂停增强任务 #48">暂停</button>
            <button type="button" class="btn btn-danger-soft btn-xs" data-action="toast" data-msg="已停止增强任务 #48">停止</button>
          </div>
        </div>
        <div class="progress-track"><div class="progress-fill" style="width:62%"></div></div>
        <p class="tnum" style="font-size:13px;color:var(--text-dim);margin-top:10px">处理 1,086 / 1,751 · 成功 942 · 跳过 118 · 进入审核 24 · 失败 2</p>
        <p style="font-size:12px;color:var(--text-faint);margin-top:4px">当前：ヨルシカ - 幻燈 · Bangumi 限流保护正常（累计等待 12s）</p>

        <details class="disclosure" style="margin-top:16px">
          <summary>历史任务记录（共 47 条） ${ic('i-down', 'xs')}</summary>
          <div class="data-table-wrap">
            <table class="data-table">
              <thead><tr><th>任务</th><th>状态</th><th>进度</th><th>审核产出</th><th>失败</th></tr></thead>
              <tbody>
                <tr><td>#47 · 作品对齐与系列</td><td><span class="tag ok">已完成</span></td><td class="tnum">733 / 733</td><td class="tnum">11</td><td class="tnum">0</td></tr>
                <tr><td>#46 · 动画专辑（强制刷新）</td><td><span class="tag ok">已完成</span></td><td class="tnum">420 / 420</td><td class="tnum">18</td><td class="tnum">1</td></tr>
              </tbody>
            </table>
          </div>
        </details>
      </section>
    </div>
  `;
}

// 7. 声学分析与 AI 打标 (/admin/audio-analysis + /admin/ai-tagging)
function renderAnalysisAI(state) {
  const tab = state.analysisTab;
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>声学分析与 AI 打标</h1>
          <p>本地 Essentia 声学向量与大模型语义打标双轨协同，驱动每日推荐与相似曲目续播。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-secondary btn-sm" data-nav="admin/ai-settings">全局 AI 模型设置 →</button>
        </div>
      </header>

      <div class="admin-tabs">
        <button type="button" class="admin-tab ${tab === 'audio' ? 'active' : ''}" data-action="analysis-tab" data-val="audio">声学分析 (Essentia) <span class="tab-cnt tnum">94%</span></button>
        <button type="button" class="admin-tab ${tab === 'ai' ? 'active' : ''}" data-action="analysis-tab" data-val="ai">AI 曲库打标 <span class="tab-cnt tnum">88%</span></button>
      </div>

      ${tab === 'audio' ? `
        <div class="admin-split">
          <div style="display:flex;flex-direction:column;gap:16px">
            <section class="card">
              <div class="card-head">
                <div><div class="card-kicker">本地分析服务状态 · music-analyzer:8000</div><h2 class="card-title">正在分析 <span class="tag ok" style="margin-left:6px">容器在线</span></h2></div>
                <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已暂停声学分析队列">暂停</button>
              </div>
              <div class="progress-track"><div class="progress-fill ok" style="width:94%"></div></div>
              <p class="tnum" style="font-size:13px;color:var(--text-dim);margin-top:10px">已完成 16,540 / 17,595 首（94.0%）· 预计剩余 42 分钟 · 唤醒度/效价回归校准已完成 (300 首基准)</p>
              <p style="font-size:12px;color:var(--text-faint);margin-top:4px">当前：SawanoHiroyuki[nZk] - Avid.flac · 实时转码或扫描时自动让路</p>
            </section>

            <section class="card">
              <div class="card-head">
                <div><div class="card-kicker">异常曲目</div><h2 class="card-title">无法分析的曲目（2）</h2></div>
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已将全部失败曲目重新加入声学分析队列">全部重试</button>
              </div>
              <div class="cand-list">
                <div class="cand-item"><div><strong>短促过渡音 SE_01</strong><small style="display:block;color:var(--text-faint)"><code>OST/se_01.flac</code> · 时长不足 5 秒，文件变化前不再重试</small></div></div>
                <div class="cand-item"><div><strong>Track 12 (Corrupted)</strong><small style="display:block;color:var(--text-faint)"><code>OldRip/12.mp3</code> · 第 2 次失败，解码器 EOF</small></div></div>
              </div>
            </section>
          </div>

          <section class="card">
            <div class="card-kicker">分析调度设置</div>
            <h2 class="card-title" style="margin-bottom:14px">开关与运行时间窗</h2>
            <div class="form-stack">
              <div class="switch-row"><div><strong>开启声学分析</strong><small>新入库曲目扫描完成后自动分析，关闭保留已有结果</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="field">
                <label>运行时间（北京时间，可跨午夜）</label>
                <label style="font-size:13px;display:flex;align-items:center;gap:8px;margin-top:4px"><input type="radio" name="aa-win"> 不限时间</label>
                <label style="font-size:13px;display:flex;align-items:center;gap:8px;margin-top:6px"><input type="radio" name="aa-win" checked> 仅在 <input class="input" type="time" value="01:00" style="width:118px;height:34px;padding:0 10px"> 到 <input class="input" type="time" value="08:00" style="width:118px;height:34px;padding:0 10px"></label>
              </div>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存声学分析调度设置">保存设置</button>
              <details class="disclosure"><summary>算法与容器部署说明 ${ic('i-down', 'xs')}</summary><p style="font-size:12px;color:var(--text-dim)">每首取 3 段 30 秒生成 400 类风格、56 类情绪、1280 维声学向量（MTG Essentia CC BY-NC-SA 4.0，仅限非商业用途）。</p></details>
            </div>
          </section>
        </div>
      ` : `
        <div class="admin-split">
          <div style="display:flex;flex-direction:column;gap:16px">
            <section class="card">
              <div class="card-head">
                <div><div class="card-kicker">AI 打标进度</div><h2 class="card-title tnum">已完成 15,480 / 17,595 首（88.0%）</h2></div>
                <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已暂停 AI 曲库打标">暂停</button>
              </div>
              <div class="progress-track"><div class="progress-fill" style="width:88%"></div></div>
              <p class="card-desc">使用全局主模型 <code>gemini-2.5-flash-lite</code>，低置信度自动交由 <code>gemini-2.5-flash</code> 复核（复核占比 9.4%）。</p>
            </section>

            <!-- Model Adequacy Sample Comparison -->
            <section class="card">
              <div class="card-head">
                <div>
                  <div class="card-kicker">质量抽检</div>
                  <h2 class="card-title">主模型够不够用？抽样对比复核</h2>
                  <p class="card-desc">随机抽取曲目同时由主模型与强复核模型打标，评估一致率以决定是否需要升级主模型。</p>
                </div>
                <div style="display:flex;gap:8px;align-items:center">
                  <select class="select" style="width:108px;height:36px;padding:0 10px"><option>50 首</option><option selected>100 首</option><option>200 首</option></select>
                  <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已启动 100 首抽样对比任务">开始抽样对比</button>
                </div>
              </div>
              <details class="disclosure" open>
                <summary>查看上次抽样逐首对比（情绪一致率 91% · 风格一致率 87%） ${ic('i-down', 'xs')}</summary>
                <div class="data-table-wrap">
                  <table class="data-table">
                    <thead><tr><th>曲目</th><th>主模型标签</th><th>复核模型标签</th><th>人工修正</th></tr></thead>
                    <tbody>
                      ${ALBUM_TRACKS.slice(0, 3).map((t) => `
                        <tr>
                          <td><strong>${t.title}</strong><small>${t.artist}</small></td>
                          <td>${t.moods.map((m) => `<span class="tag">${m}</span>`).join(' ')}</td>
                          <td>${t.moods.map((m) => `<span class="tag accent">${m}</span>`).join(' ')}</td>
                          <td><button type="button" class="btn btn-ghost btn-xs" data-drawer="track-tags" data-id="${t.id}">修正标签</button></td>
                        </tr>
                      `).join('')}
                    </tbody>
                  </table>
                </div>
              </details>
            </section>
          </div>

          <section class="card">
            <div class="card-kicker">打标专属选项</div>
            <h2 class="card-title" style="margin-bottom:14px">开关与歌词发送</h2>
            <div class="form-stack">
              <div class="switch-row"><div><strong>开启 AI 打标</strong><small>复用 AI 设置中心的连接与限流配置</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="switch-row"><div><strong>发送歌词片段（最多约 600 字）</strong><small>情绪与主题判断更准；歌词文本将发往模型服务</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="switch-row"><div><strong>启用低置信度复核</strong><small>矛盾或把握低时交由复核模型重标</small></div><input type="checkbox" class="toggle" checked></div>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存 AI 打标设置">保存设置</button>
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已重试全部 14 首打标失败曲目">重试全部失败曲目 (14)</button>
            </div>
          </section>
        </div>
      `}
    </div>
  `;
}

// 8. 歌词与翻译 (/admin/online-lyrics + /diagnose + /admin/lyrics-translations)
function renderLyrics(state) {
  const tab = state.lyricsTab;
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>歌词与翻译</h1>
          <p>统一管理在线歌词缓存、逐源抓取诊断与 AI 逐行歌词翻译缓存。</p>
        </div>
      </header>

      <div class="admin-tabs">
        <button type="button" class="admin-tab ${tab === 'online' ? 'active' : ''}" data-action="lyrics-tab" data-val="online">在线歌词缓存 <span class="tab-cnt tnum">4,820</span></button>
        <button type="button" class="admin-tab ${tab === 'diagnose' ? 'active' : ''}" data-action="lyrics-tab" data-val="diagnose">歌词诊断</button>
        <button type="button" class="admin-tab ${tab === 'translate' ? 'active' : ''}" data-action="lyrics-tab" data-val="translate">歌词翻译 <span class="tab-cnt tnum">1,290</span></button>
      </div>

      ${tab === 'online' ? `
        <div class="kpi-grid" style="grid-template-columns:repeat(3,1fr)">
          <div class="kpi-card"><span class="kpi-label">已找到</span><strong class="kpi-val tnum" style="color:var(--ok)">4,820</strong></div>
          <div class="kpi-card"><span class="kpi-label">未找到（冷却缓存）</span><strong class="kpi-val tnum" style="color:var(--warn)">312</strong></div>
          <div class="kpi-card"><span class="kpi-label">已手动清除</span><strong class="kpi-val tnum">18</strong></div>
        </div>
        <section class="card">
          <div class="card-head">
            <div style="display:flex;gap:8px;flex-wrap:wrap">
              <input class="input" style="width:240px" placeholder="搜索曲名或艺人…">
              <select class="select" style="width:130px"><option>全部状态</option><option>已找到</option><option>手动选择</option><option>未找到</option><option>已清除</option></select>
              <button type="button" class="btn btn-secondary btn-sm">筛选</button>
            </div>
            <div style="display:flex;gap:8px">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已清除 312 条“未找到”负缓存，播放时将重新抓取">立即重试全部“未找到”</button>
              <button type="button" class="btn btn-danger-soft btn-sm" data-action="toast" data-msg="已删除选中歌词缓存">删除选中</button>
            </div>
          </div>
          <div class="data-table-wrap">
            <table class="data-table">
              <thead><tr><th style="width:34px"><input type="checkbox"></th><th>曲目</th><th>来源</th><th>状态</th><th>更新时间</th></tr></thead>
              <tbody>
                <tr><td><input type="checkbox"></td><td><strong>斜陽</strong><small>ヨルシカ · 幻燈</small></td><td><code>LRCLIB</code></td><td><span class="tag ok">已找到 · 逐行同步</span></td><td class="tnum">2026-11-22 20:14</td></tr>
                <tr><td><input type="checkbox"></td><td><strong>残響散歌</strong><small>Aimer · 残響散歌</small></td><td><code>NetEase</code></td><td><span class="tag ok">已找到 · 含双语</span></td><td class="tnum">2026-11-21 15:02</td></tr>
                <tr><td><input type="checkbox"></td><td><strong>Before Lights Out</strong><small>澤野弘之</small></td><td>—</td><td><span class="tag warn">未找到</span></td><td class="tnum">2026-11-20 09:44</td></tr>
              </tbody>
            </table>
          </div>
        </section>
      ` : ''}

      ${tab === 'diagnose' ? `
        <section class="card">
          <div class="card-kicker">某首歌没有歌词？</div>
          <h2 class="card-title" style="margin-bottom:12px">单曲歌词来源诊断</h2>
          <div style="display:flex;gap:10px;flex-wrap:wrap;margin-bottom:18px">
            <input class="input" style="max-width:420px" value="ヨルシカ - 斜陽 (#3)">
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已完成多源歌词链路诊断">诊断</button>
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已立即重新在线检索">立即在线查找</button>
          </div>
          <div class="cand-list">
            <div class="cand-item"><div><strong>1. 同名 .lrc 与内嵌标签</strong><small style="display:block;color:var(--text-faint)">未发现同目录 .lrc，FLAC Vorbis 注释无 LYRICS 字段</small></div><span class="tag outline">未命中</span></div>
            <div class="cand-item ai-pick"><div><strong>2. LRCLIB</strong><small style="display:block;color:var(--text-faint)">精确匹配时长 198s · 同步 LRC 34 行 · 已缓存</small></div><span class="tag ok">命中</span></div>
            <div class="cand-item"><div><strong>3. 网易云在线源</strong><small style="display:block;color:var(--text-faint)">候选匹配 1 条（含官方中文译文）</small></div><span class="tag accent">备选可用</span></div>
          </div>
        </section>
      ` : ''}

      ${tab === 'translate' ? `
        <div class="admin-split">
          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">已缓存翻译</div><h2 class="card-title tnum">1,290 条翻译</h2></div>
              <button type="button" class="btn btn-danger-soft btn-xs" data-action="toast" data-msg="已删除选中的翻译缓存">删除选中</button>
            </div>
            <div style="display:flex;gap:8px;margin-bottom:14px">
              <input class="input" placeholder="搜索曲名或艺人…">
              <select class="select" style="width:120px"><option>全部语言</option><option>zh-Hans</option><option>zh-Hant</option><option>en</option></select>
            </div>
            <div class="data-table-wrap">
              <table class="data-table">
                <thead><tr><th style="width:32px"><input type="checkbox"></th><th>曲目</th><th>语言</th><th>行数</th><th>模型</th><th></th></tr></thead>
                <tbody>
                  <tr><td><input type="checkbox"></td><td><strong>斜陽</strong><small>ヨルシカ</small></td><td><code>zh-Hans</code></td><td class="tnum">34</td><td><code>gemini-2.5-flash</code></td><td><button type="button" class="btn btn-ghost btn-xs" data-drawer="translation-detail">查看 / 重译</button></td></tr>
                  <tr><td><input type="checkbox"></td><td><strong>夜に駆ける</strong><small>YOASOBI</small></td><td><code>zh-Hans</code></td><td class="tnum">48</td><td><code>gemini-2.5-flash</code></td><td><button type="button" class="btn btn-ghost btn-xs" data-drawer="translation-detail">查看 / 重译</button></td></tr>
                </tbody>
              </table>
            </div>
          </section>

          <section class="card">
            <div class="card-kicker">歌词翻译模型设置</div>
            <h2 class="card-title" style="margin-bottom:14px">翻译策略</h2>
            <div class="form-stack">
              <div class="switch-row"><div><strong>开启歌词翻译</strong><small>开启后客户端显示翻译开关，缓存永久复用</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="field"><label>翻译模型</label><input class="input" value="gemini-2.5-flash" placeholder="留空使用打标主模型"></div>
              <div class="field"><label>后备模型（可选）</label><input class="input" placeholder="限流或超时自动回退"></div>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存歌词翻译设置">保存设置</button>
            </div>
          </section>
        </div>
      ` : ''}
    </div>
  `;
}

// 9. 每日推荐调试 (/admin/daily-mixes)
function renderDailyMixes() {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>每日推荐调试</h1>
          <p>按情绪、风格、声学特征与播放记录抽样生成每日歌单；模型仅从候选主题中挑选并起标题，不直接选曲。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="正在重新生成今天的 4 个推荐歌单…">${ic('i-refresh', 'xs')} 重新生成今天的歌单</button>
        </div>
      </header>

      <section class="card" style="margin-bottom:18px">
        <div class="card-head" style="margin-bottom:8px">
          <div><div class="card-kicker">推荐日 2026-11-24</div><h2 class="card-title">今日已就绪 · 4 个歌单</h2><p class="card-desc">今天还可换一个 3 次 · 已屏蔽不再推荐曲目 12 首</p></div>
        </div>
        <details class="disclosure">
          <summary>候选主题（按 Thompson 抽样得分排序） ${ic('i-down', 'xs')}</summary>
          <ol class="tnum" style="padding-left:18px;font-size:12.5px;color:var(--text-dim);display:grid;gap:4px">
            <li>清澈叙事 / J-Rock · 抽样 0.91 × 偏好 1.18 = 1.07（已入选）</li>
            <li>史诗壮阔 / Soundtrack · 抽样 0.86 × 偏好 1.12 = 0.96（已入选）</li>
            <li>创作者电台：n-buna · 抽样 0.79 × 偏好 1.15 = 0.91（已入选）</li>
          </ol>
        </details>
      </section>

      <div class="admin-split">
        <div style="display:flex;flex-direction:column;gap:14px">
          ${DAILY_MIXES.slice(0, 2).map((m) => `
            <section class="card">
              <div class="card-head">
                <div style="display:flex;gap:14px;align-items:center">
                  <div class="mosaic" style="width:60px;height:60px;border-radius:6px">${m.covers.map((c) => `<img src="${c}" alt="">`).join('')}</div>
                  <div>
                    <div class="card-kicker">${m.source} · 候选池 ${m.pool} · 得分 ${m.score}</div>
                    <h3 class="card-title">${m.title}</h3>
                    <p class="card-desc tnum">${m.sub} · 熟悉 ${m.familiar} / 探索 ${m.explore}</p>
                  </div>
                </div>
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已替换今日歌单《${m.title}》">换一个</button>
              </div>
              <details class="disclosure">
                <summary>曲目与入选原因表（含起伏 / 相关×偏好×新鲜 / 不再推荐） ${ic('i-down', 'xs')}</summary>
                <div class="data-table-wrap">
                  <table class="data-table">
                    <thead><tr><th>#</th><th>曲目</th><th>分组</th><th>起伏</th><th>相关×偏好×新鲜</th><th>操作</th></tr></thead>
                    <tbody>
                      ${ALBUM_TRACKS.slice(0, 3).map((t, i) => `
                        <tr>
                          <td class="tnum">${i + 1}</td>
                          <td><button type="button" style="text-align:left" data-drawer="track-tags" data-id="${t.id}"><strong>${t.title}</strong><small>${t.artist}</small></button></td>
                          <td><span class="tag ${i < 2 ? 'accent' : 'outline'}">${i < 2 ? '熟悉' : '探索'}</span></td>
                          <td class="tnum">6.${i + 2}</td>
                          <td class="tnum">0.92 × 1.14 × 0.88</td>
                          <td><button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已将《${t.title}》加入不再推荐">不再推荐</button></td>
                        </tr>
                      `).join('')}
                    </tbody>
                  </table>
                </div>
              </details>
            </section>
          `).join('')}
        </div>

        <section class="card">
          <div class="card-kicker">生成参数</div>
          <h2 class="card-title" style="margin-bottom:14px">每日推荐设置</h2>
          <div class="form-stack">
            <div class="switch-row"><div><strong>开启每日推荐</strong><small>每天按预生成时刻自动刷新</small></div><input type="checkbox" class="toggle" checked></div>
            <div class="form-grid">
              <div class="field"><label>歌单数量</label><select class="select"><option>3 个</option><option selected>4 个</option><option>5 个</option></select></div>
              <div class="field"><label>每单曲数</label><input class="input" type="number" value="25"></div>
              <div class="field"><label>探索比例 (%)</label><input class="input" type="number" value="35"></div>
              <div class="field"><label>预生成 (UTC+8)</label><select class="select"><option selected>06:00</option><option>04:00</option></select></div>
              <div class="field full"><label>每天“换一个”上限</label><input class="input" type="number" value="3"></div>
            </div>
            <div class="switch-row"><div><strong>让模型挑选主题并起标题</strong><small>未配置 AI 时回退模板标题</small></div><input type="checkbox" class="toggle" checked></div>
            <div class="switch-row"><div><strong>允许伤感类曲目进入歌单</strong><small>默认关闭以保持日常听感平稳</small></div><input type="checkbox" class="toggle"></div>
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存每日推荐设置">保存设置</button>
          </div>
        </section>
      </div>
    </div>
  `;
}

// 10. 数据来源与同步 (/admin/settings/metadata + /admin/settings/acquisition)
function renderSources(state) {
  const tab = state.sourcesTab;
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>数据来源与同步</h1>
          <p>集中管理元数据外部源、歌手简介策略、Last.fm / ListenBrainz 播放记录与红心同步，以及 Jackett / qBittorrent 资源获取集成。</p>
        </div>
      </header>

      <div class="admin-tabs">
        <button type="button" class="admin-tab ${tab === 'metadata' ? 'active' : ''}" data-action="sources-tab" data-val="metadata">元数据来源与简介策略</button>
        <button type="button" class="admin-tab ${tab === 'scrobble' ? 'active' : ''}" data-action="sources-tab" data-val="scrobble">播放记录与红心同步</button>
        <button type="button" class="admin-tab ${tab === 'acquisition' ? 'active' : ''}" data-action="sources-tab" data-val="acquisition">资源获取集成 (Jackett / qB)</button>
      </div>

      ${tab === 'metadata' ? `
        <div class="admin-grid-2">
          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">身份来源</div><h2 class="card-title">MusicBrainz</h2><p class="card-desc">提供稳定 MBID、正式名称、别名、国家与 Wikipedia 精确入口。</p></div>
              <input type="checkbox" class="toggle" checked aria-label="启用 MusicBrainz">
            </div>
            <div class="form-grid">
              <div class="field"><label>优先级</label><input class="input" type="number" value="10"></div>
              <div class="field"><label>缓存天数</label><input class="input" type="number" value="30"></div>
              <div class="field"><label>应用名称</label><input class="input" value="032 Music Server"></div>
              <div class="field"><label>联系邮箱或项目地址</label><input class="input" value="admin@music.032.local"></div>
            </div>
            <div class="card-foot">
              <label style="font-size:12.5px;display:inline-flex;align-items:center;gap:6px"><input type="checkbox" checked> 参与自动匹配</label>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存 MusicBrainz 设置">保存 MusicBrainz</button>
            </div>
          </section>

          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">内容来源</div><h2 class="card-title">Last.fm</h2><p class="card-desc">用于多语言简介、标签与辅助验证；简介仅保存纯文本并缓存到本地。</p></div>
              <input type="checkbox" class="toggle" checked aria-label="启用 Last.fm">
            </div>
            <div class="form-grid">
              <div class="field full"><label>API Key</label><input class="input" type="password" placeholder="已设置（留空保持不变）"></div>
              <div class="field"><label>优先级</label><input class="input" type="number" value="20"></div>
              <div class="field"><label>默认查询语言</label><input class="input" value="zh"></div>
            </div>
            <div class="card-foot">
              <label style="font-size:12.5px;display:inline-flex;align-items:center;gap:6px"><input type="checkbox" checked> 参与自动匹配</label>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存 Last.fm 设置">保存 Last.fm</button>
            </div>
          </section>

          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">ACG 作品来源</div><h2 class="card-title">Bangumi</h2><p class="card-desc">对齐动画/游戏作品名称、播出年份与 2:3 海报；高置信自动确认。</p></div>
              <input type="checkbox" class="toggle" checked aria-label="启用 Bangumi">
            </div>
            <div class="form-grid-3">
              <div class="field"><label>优先级</label><input class="input" type="number" value="40"></div>
              <div class="field"><label>缓存天数</label><input class="input" type="number" value="30"></div>
              <div class="field"><label>语言</label><input class="input" value="zh"></div>
            </div>
            <div class="card-foot">
              <label style="font-size:12.5px;display:inline-flex;align-items:center;gap:6px"><input type="checkbox" checked> 扫描后自动对齐</label>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存 Bangumi 设置">保存 Bangumi</button>
            </div>
          </section>

          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">歌手简介策略</div><h2 class="card-title">Wikipedia · AI 翻译与联网概括</h2></div>
              <input type="checkbox" class="toggle" checked aria-label="启用 Wikipedia">
            </div>
            <div class="form-grid">
              <div class="field"><label>最终目标语言</label><select class="select"><option selected>简体中文 (zh-Hans)</option><option>繁體中文</option><option>English</option><option>日本語</option></select></div>
              <div class="field"><label>来源优先级</label><select class="select"><option selected>Wikipedia 优先，Last.fm 其次</option><option>Last.fm 优先，Wikipedia 其次</option></select></div>
              <div class="field"><label>原生语言优先级</label><input class="input" value="zh,ja,en"></div>
              <div class="field"><label>简介专用模型（可选）</label><input class="input" placeholder="留空复用全局主模型"></div>
            </div>
            <div style="display:flex;gap:14px;flex-wrap:wrap;margin-top:12px;font-size:12.5px">
              <label><input type="checkbox" checked> 启用 AI 自动翻译最完整源</label>
              <label><input type="checkbox" checked> 允许批量 AI 联网概括</label>
              <label><input type="checkbox" checked> 英文保底</label>
            </div>
            <div class="card-foot">
              <span></span>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存歌手简介策略">保存简介策略</button>
            </div>
          </section>
        </div>
      ` : ''}

      ${tab === 'scrobble' ? `
        <div class="admin-grid-2">
          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">Last.fm 同步</div><h2 class="card-title">Scrobble 与红心回填</h2></div>
              <span class="tag ok">已连接 @lux032</span>
            </div>
            <div class="form-stack">
              <div class="field"><label>Shared Secret</label><input class="input" type="password" placeholder="已设置"></div>
              <div class="switch-row"><div><strong>同步“正在播放”状态</strong><small>播过一半时提交 Scrobble，网络异常自动排队重试</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="switch-row"><div><strong>双向红心同步</strong><small>收藏歌曲自动推送到 Last.fm Loved Tracks</small></div><input type="checkbox" class="toggle" checked></div>
              <div style="padding:12px;border-radius:9px;background:var(--surface-alt);border:1px solid var(--border);display:flex;align-items:center;justify-content:space-between;gap:10px">
                <div style="font-size:12.5px"><strong>历史收藏回填</strong><div style="color:var(--text-faint)">已同步 412 / 412 首收藏曲目</div></div>
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已启动 Last.fm 历史红心回填">重新回填</button>
              </div>
            </div>
            <div class="card-foot">
              <button type="button" class="btn btn-danger-soft btn-xs" data-action="toast" data-msg="已断开 Last.fm 账号">断开账号</button>
              <div style="display:flex;gap:8px">
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已重试提交失败记录">重试队列</button>
                <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存 Last.fm 同步设置">保存设置</button>
              </div>
            </div>
          </section>

          <section class="card">
            <div class="card-head">
              <div><div class="card-kicker">ListenBrainz 同步</div><h2 class="card-title">Scrobble 与红心回填</h2></div>
              <span class="tag outline">未连接</span>
            </div>
            <div class="form-stack">
              <div class="field"><label>Server URL</label><input class="input" value="https://api.listenbrainz.org"></div>
              <div class="field"><label>User Token</label><input class="input" type="password" placeholder="在 ListenBrainz Profile 复制 Token"></div>
              <div class="switch-row"><div><strong>同步“正在播放”状态（缓存 MSID）</strong><small>用于后续红心与听歌画像精准对应</small></div><input type="checkbox" class="toggle" checked></div>
              <div class="switch-row"><div><strong>开启 ListenBrainz 红心同步</strong><small>连接后支持一键回填历史收藏</small></div><input type="checkbox" class="toggle"></div>
            </div>
            <div class="card-foot">
              <span></span>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="正在验证 ListenBrainz Token 并建立连接…">保存并连接</button>
            </div>
          </section>
        </div>
      ` : ''}

      ${tab === 'acquisition' ? `
        <section class="card" style="margin-bottom:18px">
          <div class="switch-row" style="border:0;background:transparent;padding:0">
            <div>
              <strong style="font-size:15px">启用资源获取功能（发现新音乐 → Jackett → qBittorrent）</strong>
              <small>仅通过 Jackett 检索 JPopsuki，下载交由 qBittorrent 做种；停用只停止本站追踪，绝不删除 qBittorrent 任务或源文件。</small>
            </div>
            <input type="checkbox" class="toggle" checked>
          </div>
        </section>
        <div class="admin-grid-2">
          <section class="card">
            <div class="card-head"><div><div class="card-kicker">索引器</div><h2 class="card-title">Jackett</h2></div><span class="tag ok">已连通</span></div>
            <div class="form-stack">
              <div class="field"><label>Jackett 地址</label><input class="input" value="http://jackett:9117"></div>
              <div class="field"><label>Indexer</label><input class="input" value="jpopsuki"></div>
              <div class="field"><label>Jackett API Key</label><input class="input" type="password" placeholder="已设置（只写不读）"></div>
              <label style="font-size:12.5px"><input type="checkbox"> 清除已保存的 API Key</label>
            </div>
            <div class="card-foot">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="Jackett 连接正常，Indexer jpopsuki 响应 210ms">测试 Jackett</button>
            </div>
          </section>

          <section class="card">
            <div class="card-head"><div><div class="card-kicker">下载器</div><h2 class="card-title">qBittorrent</h2></div><span class="tag ok">分类就绪</span></div>
            <div class="form-stack">
              <div class="field"><label>qBittorrent 地址</label><input class="input" value="http://qbittorrent:8080"></div>
              <div class="form-grid">
                <div class="field"><label>用户名</label><input class="input" value="admin"></div>
                <div class="field"><label>密码</label><input class="input" type="password" placeholder="已设置"></div>
              </div>
              <div class="field"><label>下载分类（留空表示无分类）</label><input class="input" value="music-pt"></div>
              <label style="font-size:12.5px"><input type="checkbox"> 清除已保存密码</label>
            </div>
            <div class="card-foot">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="qBittorrent WebUI 登录成功，分类 music-pt 已就绪">测试 qBittorrent 与分类</button>
              <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存获取集成设置">保存获取设置</button>
            </div>
          </section>
        </div>
      ` : ''}
    </div>
  `;
}

// 11. AI 设置中心 (/admin/settings/ai)
function renderAISettings() {
  const domains = [
    ['专辑作品关联', 'auto', '分级自治（≥88 分自动确认）'],
    ['曲目作品关联', 'auto', '分级自治（≥88 分自动确认）'],
    ['作品对齐匹配', 'auto', '分级自治（≥90 分且无占用）'],
    ['艺术家身份匹配', 'recommend', '仅给建议'],
    ['复合署名拆分', 'recommend', '仅给建议（一键应用）'],
    ['作品系列建议', 'auto', '分级自治（≥92 分无冲突）'],
  ];
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>AI 设置中心</h1>
          <p>统一配置 OpenAI 兼容模型服务、全局限流配额、6 项审核域的 AI 初审自治策略与全库 Token 用量。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="主模型、复核模型与审核模型结构化输出测试全部通过（420ms）">测试连接与模型</button>
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存全部 AI 设置">保存全部 AI 设置</button>
        </div>
      </header>

      <div class="admin-grid-2" style="margin-bottom:18px">
        <section class="card">
          <div class="card-kicker">模型服务（OpenAI 兼容）</div>
          <h2 class="card-title" style="margin-bottom:14px">连接、模型分层与限流</h2>
          <div class="form-stack">
            <div class="field"><label>接口地址（支持 Gemini / OpenAI / DeepSeek / 通义 / Ollama）</label><input class="input" value="https://generativelanguage.googleapis.com/v1beta/openai"></div>
            <div class="field"><label>API Key</label><input class="input" type="password" placeholder="已设置（留空保持不变）"></div>
            <div class="form-grid-3">
              <div class="field"><label>日常主模型</label><input class="input" value="gemini-2.5-flash-lite"></div>
              <div class="field"><label>后备模型</label><input class="input" value="deepseek-chat"></div>
              <div class="field"><label>强推理/复核</label><input class="input" value="gemini-2.5-flash"></div>
            </div>
            <div class="field"><label>代理地址（可选）</label><input class="input" type="password" placeholder="例如 http://192.168.1.2:7890"></div>
            <div class="form-grid-3">
              <div class="field"><label>全局 RPM 上限</label><input class="input" type="number" value="120"></div>
              <div class="field"><label>并发数</label><input class="input" type="number" value="4"></div>
              <div class="field"><label>思考强度</label><select class="select"><option>不发送</option><option>none</option><option selected>low</option><option>medium</option><option>high</option></select></div>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-kicker">AI 初审与审核助手</div>
          <h2 class="card-title" style="margin-bottom:14px">分级自治策略矩阵</h2>
          <div class="form-stack">
            <div class="switch-row"><div><strong>开启 AI 初审（含入审前自动预审）</strong><small>仅处理未确定项，绝不覆写人工意图与抑制记录</small></div><input type="checkbox" class="toggle" checked></div>
            <div class="switch-row"><div><strong>不确定时允许联网补证</strong><small>复用中文别名补全的 DeepSeek 联网密钥（当前已就绪）</small></div><input type="checkbox" class="toggle" checked></div>
            <div class="form-grid">
              ${domains.map(([name, mode, label]) => `
                <div class="field">
                  <label>${name}</label>
                  <select class="select">
                    <option ${mode === 'off' ? 'selected' : ''}>关闭</option>
                    <option ${mode === 'recommend' ? 'selected' : ''}>仅给建议</option>
                    ${name !== '复合署名拆分' ? `<option ${mode === 'auto' ? 'selected' : ''}>${label}</option>` : ''}
                  </select>
                </div>
              `).join('')}
            </div>
            <p style="font-size:12px;color:var(--text-faint)">安全铁律：AI 永远不会自动写入永久拒绝/抑制记录；任何自动落库条目均可在审核页一键撤销。</p>
          </div>
        </section>
      </div>

      <section class="card">
        <div class="card-head"><div><div class="card-kicker">全库 AI 用量统计</div><h2 class="card-title">Token 与缓存命中明细</h2></div><span class="tag tnum">本月总计 18,420 次请求 · 缓存命中 61%</span></div>
        <div class="data-table-wrap">
          <table class="data-table tnum">
            <thead><tr><th>模型</th><th>用途</th><th>请求</th><th>输入</th><th>缓存命中</th><th>输出</th><th>思考</th></tr></thead>
            <tbody>
              <tr><td><code>gemini-2.5-flash-lite</code></td><td>AI 曲库打标</td><td>14,210</td><td>18.4M</td><td style="color:var(--ok)">11.8M (64%)</td><td>2.1M</td><td>0</td></tr>
              <tr><td><code>gemini-2.5-flash</code></td><td>AI 初审助手</td><td>2,920</td><td>5.2M</td><td style="color:var(--ok)">3.1M (60%)</td><td>640K</td><td>1.1M</td></tr>
              <tr><td><code>gemini-2.5-flash</code></td><td>歌词翻译</td><td>1,290</td><td>1.8M</td><td>420K</td><td>1.6M</td><td>0</td></tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  `;
}

// 12. 数据库备份与恢复 (/admin/backups)
function renderBackups() {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>数据库备份与恢复</h1>
          <p>一致性 SQLite 快照 · UTC+8 调度 · 快照包含凭据哈希，请妥善保管并由 NAS/异地同步负责异地容灾。</p>
        </div>
        <div class="admin-header-actions">
          <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已创建一致性快照 music-manual-20261124-0710-v64.db">${ic('i-database', 'xs')} 立即备份</button>
        </div>
      </header>

      <div class="admin-grid-2" style="margin-bottom:18px">
        <section class="card">
          <div class="card-head"><div><div class="card-kicker">定时任务 · UTC+8</div><h2 class="card-title">每日快照设置</h2></div><span class="tag ok">上次成功 · 02:30</span></div>
          <div class="form-stack">
            <div class="switch-row"><div><strong>启用每日自动快照</strong><small>每天最多一份，启动时补跑；恢复前自动保留两份 preupgrade</small></div><input type="checkbox" class="toggle" checked></div>
            <div class="form-grid-3">
              <div class="field"><label>每日时间</label><input class="input" type="time" value="02:30"></div>
              <div class="field"><label>daily 保留份数</label><input class="input" type="number" value="7"></div>
              <div class="field"><label>preupgrade 保留</label><input class="input" type="number" value="2"></div>
            </div>
          </div>
          <div class="card-foot">
            <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复环境变量默认备份设置">恢复环境变量默认</button>
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已保存每日备份设置">保存设置</button>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">外部快照恢复</div><h2 class="card-title">上传 SQLite 快照校验</h2></div></div>
          <p class="card-desc" style="margin-bottom:14px">上传后先暂存并校验完整性与 Schema 版本，不会自动覆盖；需输入当前管理员密码二次确认后才执行恢复并重启服务。</p>
          <div class="form-stack">
            <input class="input" type="file" style="padding:6px 10px">
            <div style="display:flex;gap:8px">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="快照校验通过（Schema v64），请在下方确认恢复">${ic('i-upload', 'xs')} 上传并校验</button>
            </div>
            <details class="disclosure danger-disclosure">
              <summary>二次确认恢复演示（需管理员密码）</summary>
              <div class="form-stack">
                <input class="input" type="password" placeholder="再次输入当前管理员密码">
                <div style="display:flex;gap:8px">
                  <button type="button" class="btn btn-danger btn-sm" data-action="toast" data-msg="正在恢复快照并重启服务…">确认恢复并重启</button>
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已取消并删除暂存文件">取消暂存</button>
                </div>
              </div>
            </details>
          </div>
        </section>
      </div>

      <section class="card">
        <div class="card-head"><div><div class="card-kicker">本地快照库</div><h2 class="card-title">已保存快照（3）</h2></div></div>
        <div class="data-table-wrap">
          <table class="data-table">
            <thead><tr><th>快照文件名</th><th>类型</th><th>大小</th><th>创建时间</th><th>操作</th></tr></thead>
            <tbody>
              ${[
                ['music-daily-20261124-023001-v64.db', 'daily', '14.1 MB', '2026-11-24 02:30'],
                ['music-daily-20261123-023001-v64.db', 'daily', '14.0 MB', '2026-11-23 02:30'],
                ['music-preupgrade-20261118-0912-v63.db', 'preupgrade', '13.6 MB', '2026-11-18 09:12'],
              ].map(([f, tp, sz, dt]) => `
                <tr>
                  <td><code>${f}</code></td>
                  <td><span class="tag ${tp === 'daily' ? 'accent' : 'outline'}">${tp}</span></td>
                  <td class="tnum">${sz}</td>
                  <td class="tnum" style="color:var(--text-faint)">${dt}</td>
                  <td>
                    <div style="display:flex;gap:6px;flex-wrap:wrap">
                      <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="下载快照 ${f}">${ic('i-download', 'xs')} 下载</button>
                      <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已选择 ${f} 进入恢复确认">选择恢复</button>
                      <button type="button" class="btn btn-ghost btn-xs" style="color:var(--danger)" data-action="toast" data-msg="请确认删除快照 ${f}">删除</button>
                    </div>
                  </td>
                </tr>
              `).join('')}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  `;
}

// 13. 账号与安全 (/admin/settings/security)
function renderSecurity() {
  return `
    <div class="admin-wrap">
      <header class="admin-header">
        <div>
          <h1>账号与安全</h1>
          <p>管理登录账号、密码、两步验证、防爆破白名单与客户端 API / 媒体 Token。所有敏感修改均需验证当前密码并立即生效。</p>
        </div>
      </header>

      <div class="admin-grid-2">
        <section class="card">
          <div class="card-head"><div><div class="card-kicker">登录账号</div><h2 class="card-title">用户名</h2></div><span class="tag outline">来源：管理页覆盖</span></div>
          <div class="form-grid">
            <div class="field"><label>新用户名（当前：lux032）</label><input class="input" value="lux032"></div>
            <div class="field"><label>当前密码</label><input class="input" type="password" placeholder="验证身份"></div>
          </div>
          <div class="card-foot">
            <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复环境变量默认用户名">恢复为环境变量…</button>
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已修改用户名，其他会话已退出">修改用户名</button>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">认证凭据</div><h2 class="card-title">登录密码</h2></div><span class="tag outline">PBKDF2 哈希</span></div>
          <div class="form-grid-3">
            <div class="field"><label>当前密码</label><input class="input" type="password"></div>
            <div class="field"><label>新密码（≥12 位）</label><input class="input" type="password"></div>
            <div class="field"><label>确认新密码</label><input class="input" type="password"></div>
          </div>
          <div class="card-foot">
            <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复环境变量默认密码">恢复为环境变量…</button>
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="密码已更新，其他设备会话已退出">修改密码</button>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">TOTP 动态口令</div><h2 class="card-title">两步验证（2FA）</h2></div><span class="tag ok">已启用</span></div>
          <p class="card-desc">仅保护 Web 管理面板登录，不影响客户端 API Token 与 Sonos 媒体 Token。</p>
          <div class="card-foot">
            <details class="disclosure danger-disclosure" style="width:100%">
              <summary>停用两步验证…</summary>
              <div style="display:flex;gap:8px;flex-wrap:wrap">
                <input class="input" type="password" style="max-width:220px" placeholder="当前密码">
                <input class="input" style="max-width:140px" placeholder="6 位验证码">
                <button type="button" class="btn btn-danger btn-sm" data-action="toast" data-msg="已停用两步验证">确认停用</button>
              </div>
            </details>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">登录保护与防爆破</div><h2 class="card-title">封禁状态与白名单</h2></div><span class="tag ok">无封禁</span></div>
          <p class="card-desc" style="margin-bottom:10px">当前来源 IP：<code>192.168.1.42</code> · 连续失败 10 次触发阶梯封禁（15 分 → 7 天）。</p>
          <div class="cand-list" style="margin-bottom:10px">
            <div class="cand-item"><span><code>192.168.1.0/24</code> <small style="color:var(--text-faint)">家庭局域网</small></span><button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已移除白名单条目">移除</button></div>
            <div class="cand-item"><span><code>100.64.0.0/10</code> <small style="color:var(--text-faint)">Tailscale</small></span><button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已移除白名单条目">移除</button></div>
          </div>
          <div style="display:flex;gap:8px">
            <input class="input" placeholder="新增 IP 或 CIDR…">
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已加入白名单">加入白名单</button>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">客户端访问（032player-rs 等）</div><h2 class="card-title">API Token</h2></div><span class="tag outline">仅存哈希</span></div>
          <p class="card-desc" style="margin-bottom:12px">通过 <code>Authorization: Bearer</code> 访问 JSON API；新生成 Token 仅展示一次。</p>
          <div style="display:flex;gap:8px;margin-bottom:10px">
            <input class="input" type="password" placeholder="输入当前密码">
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已生成新的 API Token（请立即复制）">生成新 Token</button>
          </div>
          <details class="disclosure"><summary>使用自定义 Token / 恢复环境变量…</summary><div style="display:flex;gap:8px"><input class="input" placeholder="至少 32 位安全字符"><button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="已保存自定义 API Token">保存</button></div></details>
        </section>

        <section class="card">
          <div class="card-head"><div><div class="card-kicker">Sonos / DLNA 投放访问</div><h2 class="card-title">媒体 Token</h2></div><span class="tag outline">仅限音轨/封面/歌词</span></div>
          <p class="card-desc" style="margin-bottom:12px">通过 <code>mediaToken</code> 参数拉取音频流与封面，不可访问其他管理 API。</p>
          <div style="display:flex;gap:8px;margin-bottom:10px">
            <input class="input" type="password" placeholder="输入当前密码">
            <button type="button" class="btn btn-primary btn-sm" data-action="toast" data-msg="已生成新的媒体 Token">生成新 Token</button>
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="当前媒体 Token 已复制到剪贴板">查看当前媒体 Token</button>
            <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复环境变量默认媒体 Token">恢复环境变量</button>
          </div>
        </section>
      </div>
    </div>
  `;
}

export function renderAdminDrawer(type) {
  if (type === 'avatar-strategy') {
    return {
      title: '头像补全数据源与 AI 策略设置',
      body: `
        <div class="form-stack">
          <div class="switch-row"><div><strong>Bangumi Persons</strong><small>ACG 声优与作曲家优先</small></div><input type="checkbox" class="toggle" checked></div>
          <div class="switch-row"><div><strong>Deezer Public API</strong><small>流行音乐高分辨率艺人照</small></div><input type="checkbox" class="toggle" checked></div>
          <div class="switch-row"><div><strong>Wikipedia pageimages</strong><small>基于已确认条目拉取主图</small></div><input type="checkbox" class="toggle" checked></div>
          <div class="switch-row"><div><strong>Fanart.tv v3</strong><small>需配置个人 API Key</small></div><input type="checkbox" class="toggle"></div>
          <div class="switch-row"><div><strong>联网搜索 + AI 选图兜底</strong><small>结构化图源缺失时检索候选</small></div><input type="checkbox" class="toggle" checked></div>
          <div class="switch-row"><div><strong>高置信度自动入库 + VLM 视觉质检</strong><small>校验人脸构图与清晰度，中低置信进入待审头像</small></div><input type="checkbox" class="toggle" checked></div>
          <div class="form-grid">
            <div class="field"><label>选图文本模型</label><input class="input" value="gemini-2.5-flash"></div>
            <div class="field"><label>视觉质检模型</label><input class="input" value="gemini-2.5-flash"></div>
            <div class="field"><label>单轮联网上限 (0=不限)</label><input class="input" type="number" value="50"></div>
            <div class="field"><label>负缓存冷却天数</label><input class="input" type="number" value="30"></div>
          </div>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">取消</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已保存头像补全数据源与 VLM 策略">保存策略</button>`,
    };
  }
  return {
    title: '歌词翻译详情 · ヨルシカ - 斜陽 (zh-Hans)',
    body: `
      <div class="form-stack">
        <div style="font-size:12.5px;color:var(--text-dim)">模型：<code>gemini-2.5-flash</code> · 34 行 · 更新于 2026-11-22 20:16</div>
        <div class="cand-list">
          <div class="cand-item"><div><strong>頬に落ちる夕陽が</strong><small style="display:block;color:var(--text-dim)">落在脸颊上的夕阳</small></div></div>
          <div class="cand-item"><div><strong>君の影を伸ばしていく</strong><small style="display:block;color:var(--text-dim)">将你的身影渐渐拉长</small></div></div>
          <div class="cand-item"><div><strong>斜陽の向こうへ溶けていく</strong><small style="display:block;color:var(--text-dim)">正融化在斜阳的彼端</small></div></div>
        </div>
      </div>`,
    foot: `
      <button type="button" class="btn btn-danger-soft btn-sm" data-action="save-drawer" data-msg="已删除该条翻译缓存">删除这条翻译</button>
      <button type="button" class="btn btn-primary btn-sm" data-action="save-drawer" data-msg="已触发重新翻译">重新翻译</button>`,
  };
}
