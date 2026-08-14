// タコダ99 観測ダッシュボード — 素の JS。
// サーバーの /admin/ws（読み取り専用の観測ストリーム）を購読し、3タブで盤面を描く。
//   店舗盤面  : 99店のスコア/順位/行列/提供数/足切り対象圏。
//   スコア分布: 順位順の縦棒＋カットライン＋Bot色分け＋足切り履歴。
//   客フロー  : 待機エリア(restPool)と各店の行列を、客属性(ジャンル)別の四角で可視化。
//
// ワイヤ契約（plan-h00 §4 / plan-h02）: /admin/ws は proto.Envelope{type,payload}。
//   type="AdminSnapshot" のみ。
//
// 🔴 本戦（plan-h25）で観測の目的が変わった:
//   「当日のトラブル切り分け」→「残り期間でバランスを詰めるための計測器」。
//   体力・我慢ゲージは廃止され、見るのは **スコア分布と足切りの妥当性**。
//   分布ビューが無いと h26（バランス検証）が回らない。
'use strict';

(() => {
  const $ = (id) => document.getElementById(id);

  // 客属性（ジャンル）のメタ。Bonus=ヒョウ柄おばちゃん / Buzz=JK（proto の定義より）。
  const ATTR_ORDER = ['normal', 'bonus', 'claimer', 'buzz'];
  const ATTR = {
    normal: { label: '通常', glyph: '' },
    bonus: { label: 'おばちゃん', glyph: 'ヒ' },
    claimer: { label: 'クレーマー', glyph: 'ク' },
    buzz: { label: 'JK', glyph: 'J' },
  };
  const SHOP_SQUARE_CAP = 8;   // これを超える行列は「人数表示」に切り替える
  const REST_SQUARE_CAP = 160; // 待機エリアに描く四角の上限（超過は比例縮約）

  const el = {
    banner: $('banner'), connDot: $('conn-dot'),
    tabs: document.querySelectorAll('.tab'),
    sortControl: $('sort-control'), sortMode: $('sort-mode'),
    settingsBtn: $('settings-btn'), settings: $('settings'),
    serverInput: $('server-input'), tokenInput: $('token-input'), connectBtn: $('connect-btn'),
    // metrics
    mAlive: $('m-alive'), mTotal: $('m-total'), mDead: $('m-dead'),
    mPhase: $('m-phase'), mHeat: $('m-heat'), mRest: $('m-rest'),
    mMix: $('m-mix'), mProgress: $('m-progress'), mUpdate: $('m-update'),
    mCullUntil: $('m-cull-until'), mCullStage: $('m-cull-stage'), mCullTotal: $('m-cull-total'),
    // 足切り予告
    cull: $('cull'), cullStage: $('cull-stage'), cullTotal: $('cull-total'),
    cullUntil: $('cull-until'), cullTarget: $('cull-target'), cullLine: $('cull-line'), cullCount: $('cull-count'),
    // views
    viewBoard: $('view-board'), viewDist: $('view-dist'), viewFlow: $('view-flow'),
    grid: $('grid'), legend: $('legend'),
    distBars: $('dist-bars'), distChart: $('dist-chart'), cutline: $('cutline'),
    dTop: $('d-top'), dBottom: $('d-bottom'), dSep: $('d-sep'), dBotShare: $('d-botshare'),
    dTopN: $('d-top-n'), dBottomN: $('d-bottom-n'), dBotN: $('d-bot-n'),
    cullLogBody: $('cull-log-body'),
    restCount: $('rest-count'), restPool: $('rest-pool'), flowGrid: $('flow-grid'),
    empty: $('empty'), emptyMsg: $('empty-msg'),
  };

  const LS = { server: 'takoda99.dash.server', token: 'takoda99.dash.token', sort: 'takoda99.dash.sort', tab: 'takoda99.dash.tab' };

  // ── 状態 ──
  const cells = new Map();  // storeId -> board cell refs
  const shops = new Map();  // storeId -> flow shop refs
  const bars = new Map();   // storeId -> dist bar refs
  let activeTab = 'board';
  let lastSnapshot = null;
  // 足切り履歴。サーバーは履歴を配らないので、スナップショット間の差分から組み立てる
  // （前回生存 → 今回脱落 になった店を数える）。
  let prevAlive = null;     // Map<storeId, {isBot}>
  let cullLog = [];
  let logMatchId = null;    // 履歴がどの試合のものか
  let ws = null, reconnectTimer = null, backoff = 1000, manualClose = false;
  let idleTimer = null;

  // ── 設定 / URL ──
  const urlToken = () => new URLSearchParams(location.search).get('token') || '';
  const getServer = () => (el.serverInput.value || localStorage.getItem(LS.server) || '').trim();
  const getToken = () => (el.tokenInput.value || urlToken() || localStorage.getItem(LS.token) || '').trim();

  function buildWsUrl() {
    const token = getToken(), server = getServer();
    let base;
    if (server) {
      base = server.replace(/^http/, 'ws').replace(/\/+$/, '');
      if (!/^wss?:\/\//.test(base)) base = (location.protocol === 'https:' ? 'wss://' : 'ws://') + base;
    } else {
      base = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host;
    }
    return base + '/admin/ws' + (token ? '?token=' + encodeURIComponent(token) : '');
  }

  // ── 接続状態 ──
  const setConn = (s) => { el.connDot.dataset.state = s; };
  function showBanner(msg, kind) { el.banner.textContent = msg; el.banner.dataset.kind = kind || 'warn'; el.banner.hidden = false; }
  const hideBanner = () => { el.banner.hidden = true; };
  function showEmpty(msg) { el.emptyMsg.textContent = msg; el.empty.hidden = false; }
  const hideEmpty = () => { el.empty.hidden = true; };

  // ── WebSocket ──
  function connect() {
    disconnect(true);
    manualClose = false;
    if (!getToken()) { setConn('closed'); showEmpty('管理トークンが必要です。⚙ から入力してください。'); openSettings(); return; }
    let url;
    try { url = buildWsUrl(); } catch (e) { showBanner('URL が不正です: ' + e.message, 'error'); return; }
    setConn('connecting'); showEmpty('サーバーに接続しています…');
    try { ws = new WebSocket(url); } catch { scheduleReconnect('接続に失敗しました'); return; }

    ws.onopen = () => { setConn('open'); hideBanner(); backoff = 1000; armIdle(); };
    ws.onmessage = (ev) => { let env; try { env = JSON.parse(ev.data); } catch { return; } handleEnvelope(env); };
    ws.onclose = (e) => {
      setConn('closed');
      if (manualClose) return;
      if (e.code === 1008 || e.code === 4001 || e.code === 4403) { showBanner('認証に失敗しました。トークンを確認してください。', 'error'); openSettings(); return; }
      scheduleReconnect('接続が切れました。再接続します…');
    };
    ws.onerror = () => {};
  }
  function disconnect(silent) {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (ws) { manualClose = true; try { ws.close(); } catch {} ws = null; }
    if (!silent) setConn('closed');
  }
  function scheduleReconnect(msg) {
    showBanner(msg, 'warn');
    if (reconnectTimer) return;
    reconnectTimer = setTimeout(() => { reconnectTimer = null; connect(); }, backoff);
    backoff = Math.min(backoff * 1.6, 10000);
  }

  // 試合が走っていないと room が居らず配信が止まる（plan-h02 N7）。無音を検知して案内する。
  function armIdle() {
    if (idleTimer) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => { showEmpty('試合が走っていません（待機中）'); }, 4000);
  }

  // ── メッセージ ──
  function handleEnvelope(env) {
    if (!env || typeof env.type !== 'string') return;
    if (env.type === 'AdminSnapshot') { onSnapshot(env.payload); return; }
    // 未知 type は無視（前方互換）。
  }

  function onSnapshot(snap) {
    if (!snap || !Array.isArray(snap.stores)) return;
    trackCull(snap);
    lastSnapshot = snap;
    hideEmpty();
    armIdle();
    renderMetrics(snap);
    renderActive();
  }

  // ── 足切り履歴（P4 の観測）──
  // 前回スナップショットで生存していて今回脱落している店を「このステージで切られた店」とみなす。
  function trackCull(snap) {
    // 試合が替わったら履歴をリセットする。
    // /admin/ws は試合をまたいで繋ぎっぱなしなので、これが無いと前の試合の足切りが
    // 次の試合の履歴に残り続ける（当日ここを見て誤診する）。
    const mid = snap.matchId || '';
    if (mid !== logMatchId) {
      logMatchId = mid;
      cullLog = [];
      prevAlive = null;
      if (activeTab === 'dist') renderCullLog();
    }

    const now = new Map();
    for (const s of snap.stores) if (s.alive) now.set(String(s.storeId), { isBot: !!s.isBot });

    if (prevAlive) {
      let human = 0, bot = 0;
      for (const [id, info] of prevAlive) {
        if (!now.has(id)) { if (info.isBot) bot++; else human++; }
      }
      if (human + bot > 0) {
        // 直前に「向かっていた」ステージ番号が、今切られたステージ。
        const stage = cullLog.length + 1;
        cullLog.push({
          stage, atMs: snap.elapsedMs || 0, cut: human + bot, human, bot, alive: snap.aliveCount,
        });
        if (activeTab === 'dist') renderCullLog();
      }
    }
    prevAlive = now;
  }

  // ── メトリクス帯 ──
  function mixTotal(m) { return (m.normal || 0) + (m.bonus || 0) + (m.claimer || 0) + (m.buzz || 0); }

  function renderMetrics(snap) {
    const total = snap.stores.length || 99;
    const dead = total - snap.aliveCount;
    el.mAlive.textContent = snap.aliveCount;
    el.mTotal.textContent = total;
    el.mDead.textContent = dead;
    el.mPhase.textContent = phaseLabel(snap.phase);
    el.mHeat.textContent = snap.heatLevel != null ? snap.heatLevel : '–';
    el.mRest.textContent = snap.restPool != null ? snap.restPool : '–';
    renderMix(el.mMix, snap.customers || {});
    // 本戦は全店が脱落して終わるので、進捗は「脱落数 / 全店」。
    const pct = total > 0 ? (dead / total) * 100 : 0;
    el.mProgress.style.width = Math.max(0, Math.min(100, pct)).toFixed(1) + '%';
    el.mUpdate.textContent = new Date().toLocaleTimeString('ja-JP', { hour12: false });

    renderCull(snap);
  }

  // 足切り予告。本戦は時刻スケジュールなので**常時**表示（「予告中かどうか」は無い）。
  function renderCull(snap) {
    const c = snap.cull;
    if (!c || !c.stageIndex) { // 全ステージ消化済み（試合終了）
      el.cull.hidden = true;
      el.mCullUntil.textContent = '–';
      el.mCullStage.textContent = '–';
      return;
    }
    const sec = Math.max(0, Math.ceil((c.untilMs || 0) / 1000));
    const atRisk = (snap.stores || []).filter(s => s.atRisk).length;

    el.mCullUntil.textContent = sec;
    el.mCullStage.textContent = c.stageIndex;
    el.mCullTotal.textContent = c.stageTotal || 6;

    el.cullStage.textContent = c.stageIndex;
    el.cullTotal.textContent = c.stageTotal || 6;
    el.cullUntil.textContent = sec;
    el.cullTarget.textContent = c.targetAliveCount != null ? c.targetAliveCount : '–';
    el.cullLine.textContent = c.cutLineRank != null ? c.cutLineRank : '–';
    el.cullCount.textContent = atRisk + ' 店が対象';
    el.cull.dataset.imminent = String(sec <= 5);
    el.cull.hidden = false;
  }

  function phaseLabel(p) {
    return { Early: '序盤', Mid: '中盤', Late: '終盤' }[p] || (p || '–');
  }

  // 客内訳のミニ積み上げバー
  function renderMix(container, mix) {
    const total = mixTotal(mix) || 1;
    if (!container._segs) {
      container._segs = {};
      for (const a of ATTR_ORDER) {
        const seg = document.createElement('div');
        seg.className = 'mix-seg'; seg.dataset.attr = a;
        seg.title = ATTR[a].label;
        container.appendChild(seg); container._segs[a] = seg;
      }
    }
    for (const a of ATTR_ORDER) {
      const v = mix[a] || 0;
      container._segs[a].style.width = (v / total * 100).toFixed(1) + '%';
      container._segs[a].title = `${ATTR[a].label}: ${v}`;
    }
  }

  // ── タブ ──
  function renderActive() {
    if (!lastSnapshot) return;
    if (activeTab === 'board') renderBoard(lastSnapshot);
    else if (activeTab === 'dist') { renderDist(lastSnapshot); renderCullLog(); }
    else renderFlow(lastSnapshot);
  }

  function switchTab(tab) {
    activeTab = tab;
    localStorage.setItem(LS.tab, tab);
    el.tabs.forEach(t => t.classList.toggle('is-active', t.dataset.tab === tab));
    el.viewBoard.hidden = tab !== 'board';
    el.viewDist.hidden = tab !== 'dist';
    el.viewFlow.hidden = tab !== 'flow';
    el.sortControl.style.visibility = tab === 'board' ? 'visible' : 'hidden';
    renderActive();
  }

  // ══ 店舗盤面（tab1） ══
  const fmtScore = (n) => (n > 0 ? '+' : '') + n.toLocaleString('ja-JP');

  function renderBoard(snap) {
    for (const s of snap.stores) upsertCell(s);
    const present = new Set(snap.stores.map(s => String(s.storeId)));
    for (const [id, c] of cells) { if (!present.has(id)) { c.root.remove(); cells.delete(id); } }
    applySort();
  }

  function upsertCell(s) {
    const id = String(s.storeId);
    let c = cells.get(id) || createCell(id);
    const alive = !!s.alive;
    const score = Number(s.score) || 0;
    const rank = Number(s.rank) || 0;
    const queueLen = Number(s.queueLen) || 0, served = Number(s.servedCount) || 0;
    const tako = Number(s.takoyakiCount) || 0, miss = Number(s.missCount) || 0;

    if (c.prev) {
      if (alive && c.prev.alive && score > c.prev.score) flash(c.root, 'flash-gain');
      if (!alive && c.prev.alive) flash(c.root, 'flash-dead');
    }
    c.name.textContent = s.displayName || id;
    c.name.title = (s.displayName || id) + '（' + id + '）';
    c.root.dataset.alive = String(alive);
    c.root.dataset.leader = String(alive && rank === 1);
    c.root.dataset.atrisk = String(!!s.atRisk);
    c.root.dataset.bot = String(!!s.isBot);
    c.rank.textContent = alive ? (rank > 0 ? '#' + rank : '–') : '✕';
    const fr = s.finalRank;
    c.finalBadge.textContent = (typeof fr === 'number') ? (fr + '位') : '脱落';
    // スコアが主表示。負値もそのまま出す（0 でクランプしていないので実際に負になる）。
    c.score.textContent = fmtScore(score);
    c.score.dataset.sign = score < 0 ? 'neg' : 'pos';
    c.takoVal.textContent = tako;
    c.missVal.textContent = miss;
    c.queueVal.textContent = queueLen;
    c.queueWrap.classList.toggle('queue-warn', alive && queueLen === 0);
    c.servedVal.textContent = served;

    c.sortKeys = {
      rank: alive ? rank : 10000 + (typeof fr === 'number' ? fr : 999),
      score, queue: queueLen, alive, atRisk: !!s.atRisk, id,
    };
    c.prev = { score, alive };
    cells.set(id, c);
  }

  function createCell(id) {
    const root = document.createElement('div');
    root.className = 'cell'; root.dataset.alive = 'true';
    root.innerHTML = `
      <div class="cell-head"><span class="rank">–</span><span class="name"></span><span class="bot-tag" title="Bot（CPU補完）">BOT</span></div>
      <div class="score" data-sign="pos">0</div>
      <div class="cell-meta">
        <span class="tako"><span class="k">たこ</span> <b class="tako-val">0</b></span>
        <span class="miss"><span class="k">ミス</span> <b class="miss-val">0</b></span>
      </div>
      <div class="cell-meta">
        <span class="queue"><span class="k">行列</span> <b class="queue-val">0</b></span>
        <span class="served"><span class="k">提供</span> <b class="served-val">0</b></span>
      </div>
      <span class="final-badge">脱落</span>`;
    const c = {
      root, rank: root.querySelector('.rank'), name: root.querySelector('.name'),
      score: root.querySelector('.score'),
      takoVal: root.querySelector('.tako-val'), missVal: root.querySelector('.miss-val'),
      queueWrap: root.querySelector('.queue'), queueVal: root.querySelector('.queue-val'),
      servedVal: root.querySelector('.served-val'),
      finalBadge: root.querySelector('.final-badge'),
      prev: null, sortKeys: { rank: 9999, score: 0, queue: 0, alive: true, atRisk: false, id },
    };
    el.grid.appendChild(root); cells.set(id, c); return c;
  }

  function flash(node, cls) {
    node.classList.remove(cls); void node.offsetWidth; node.classList.add(cls);
    node.addEventListener('animationend', () => node.classList.remove(cls), { once: true });
  }

  function applySort() {
    const mode = el.sortMode.value;
    const arr = [...cells.values()];
    arr.sort((a, b) => {
      const ka = a.sortKeys, kb = b.sortKeys;
      if (mode === 'id') return cmpId(ka.id, kb.id);
      // 「切られる順」= 次の足切り対象を先頭へ。当日「今まさに危ない店」を見るための並び。
      if (mode === 'risk') { if (ka.alive !== kb.alive) return ka.alive ? -1 : 1; if (ka.atRisk !== kb.atRisk) return ka.atRisk ? -1 : 1; return kb.rank - ka.rank; }
      if (mode === 'queue') { if (ka.alive !== kb.alive) return ka.alive ? -1 : 1; if (ka.alive) return (kb.queue - ka.queue) || (ka.rank - kb.rank); return ka.rank - kb.rank; }
      return ka.rank - kb.rank;
    });
    arr.forEach((c, i) => { c.root.style.order = i; });
  }

  function cmpId(a, b) {
    const na = parseInt(String(a).replace(/\D/g, ''), 10), nb = parseInt(String(b).replace(/\D/g, ''), 10);
    if (!isNaN(na) && !isNaN(nb) && na !== nb) return na - nb;
    return String(a).localeCompare(String(b));
  }

  // ══ スコア分布（tab2・h25 の主目的）══
  //
  // 見たいのは3つ（plan-h25 §2.3）:
  //   1. 上位と下位が**分離しているか**（団子だと足切りがタイブレーク頼みになる）
  //   2. **カットラインが分布のどこ**に来るか
  //   3. **Bot が上位を占めていないか**（P1）
  //
  // 凝ったグラフは要らない。順位順に並べた縦棒＋カットラインの横線で十分読める。
  function renderDist(snap) {
    const stores = [...snap.stores];
    // 順位順（生存店は現在順位、脱落店はその後ろへ確定順位で）。
    stores.sort((a, b) => rankKeyOf(a) - rankKeyOf(b));

    const scores = stores.map(s => Number(s.score) || 0);
    const max = Math.max(1, ...scores.map(Math.abs));

    // 棒を差分更新する（毎tick作り直すと 99 要素の再生成で描画が重い）。
    const present = new Set();
    stores.forEach((s, i) => {
      const id = String(s.storeId);
      present.add(id);
      let b = bars.get(id) || createBar(id);
      const score = Number(s.score) || 0;
      const alive = !!s.alive;
      // 高さは |score| / max。負値は下向きに描くと軸が要るので、ここでは
      // 「0 は高さ0・負は赤で最小高」に潰して読みやすさを優先する。
      const h = Math.max(2, Math.round((Math.abs(score) / max) * 100));
      b.root.style.order = i;
      b.root.style.height = h + '%';
      b.root.dataset.alive = String(alive);
      b.root.dataset.bot = String(!!s.isBot);
      b.root.dataset.cut = String(!!s.atRisk);
      b.root.dataset.neg = String(score < 0);
      b.root.title = `${s.displayName || id}\n順位 ${alive ? (s.rank || '–') : (s.finalRank || '–') + '（確定）'}\n` +
        `スコア ${fmtScore(score)}\nたこ焼き ${s.takoyakiCount || 0} / ミス ${s.missCount || 0}` +
        (s.isBot ? '\nBot' : '');
      bars.set(id, b);
    });
    for (const [id, b] of bars) { if (!present.has(id)) { b.root.remove(); bars.delete(id); } }

    renderCutline(snap, stores);
    renderDistStats(stores);
  }

  function rankKeyOf(s) {
    if (s.alive) return Number(s.rank) || 9999;
    return 10000 + (typeof s.finalRank === 'number' ? s.finalRank : 999);
  }

  function createBar(id) {
    const root = document.createElement('i');
    root.className = 'dist-bar';
    el.distBars.appendChild(root);
    const b = { root };
    bars.set(id, b);
    return b;
  }

  // カットラインを分布上に重ねる。生存店の並びの中で「何本目の右側が切られるか」を示す。
  function renderCutline(snap, stores) {
    const c = snap.cull;
    const alive = stores.filter(s => s.alive);
    if (!c || !c.stageIndex || c.targetAliveCount == null || alive.length === 0) {
      el.cutline.hidden = true;
      return;
    }
    const keep = Math.min(c.targetAliveCount, alive.length);
    if (keep >= alive.length) { el.cutline.hidden = true; return; }
    // 棒は全店ぶん並んでいるので、位置は「残す本数 / 全店数」。
    const pct = (keep / stores.length) * 100;
    el.cutline.style.left = pct.toFixed(2) + '%';
    el.cutline.hidden = false;
  }

  // 分布の要約。「団子になっていないか」を数字でも見えるようにする。
  function renderDistStats(stores) {
    const alive = stores.filter(s => s.alive);
    if (alive.length === 0) {
      el.dTop.textContent = el.dBottom.textContent = el.dSep.textContent = el.dBotShare.textContent = '–';
      return;
    }
    // 上位と下位が重ならないようにする。生存が少ないと slice(0,10) と slice(-10) が
    // 同じ集合になり、分離度が常に 0 になって指標として死ぬ（終盤に必ず起きる）。
    const n = Math.max(1, Math.min(10, Math.floor(alive.length / 2)));
    const avg = (arr) => Math.round(arr.reduce((a, s) => a + (Number(s.score) || 0), 0) / arr.length);
    const top = alive.slice(0, n), bottom = alive.slice(-n);
    const tAvg = avg(top), bAvg = avg(bottom);
    el.dTop.textContent = fmtScore(tAvg);
    el.dBottom.textContent = fmtScore(bAvg);
    el.dSep.textContent = fmtScore(tAvg - bAvg);
    el.dTopN.textContent = el.dBottomN.textContent = n;
    // P1: ボットが上位を占めていないか。
    const botsInTop = top.filter(s => s.isBot).length;
    el.dBotShare.textContent = botsInTop + '/' + n;
    el.dBotN.textContent = n;
    el.dBotShare.dataset.warn = String(botsInTop > n / 2);
  }

  function renderCullLog() {
    if (cullLog.length === 0) {
      el.cullLogBody.innerHTML = '<tr class="cull-log-empty"><td colspan="6">まだ足切りは起きていません</td></tr>';
      return;
    }
    el.cullLogBody.innerHTML = cullLog.map(r =>
      `<tr><td>${r.stage}</td><td>${(r.atMs / 1000).toFixed(0)}s</td>` +
      `<td><b>${r.cut}</b></td><td>${r.human}</td><td>${r.bot}</td><td>${r.alive}</td></tr>`
    ).join('');
  }

  // ══ 客フロー（tab3） ══
  function renderFlow(snap) {
    renderLegend(snap.customers || {});
    // 待機エリア（restPool）
    el.restCount.textContent = snap.restPool != null ? snap.restPool : mixTotal(snap.restByAttr || {});
    reconcileSquares(el.restPool, snap.restByAttr || {}, REST_SQUARE_CAP);
    // 各店の行列
    for (const s of snap.stores) upsertShop(s);
    const present = new Set(snap.stores.map(s => String(s.storeId)));
    for (const [id, sh] of shops) { if (!present.has(id)) { sh.root.remove(); shops.delete(id); } }
    // 評価順に並べる（強い店が上）
    const arr = [...shops.values()];
    arr.sort((a, b) => (a.rankKey - b.rankKey));
    arr.forEach((sh, i) => { sh.root.style.order = i; });
  }

  function renderLegend(mix) {
    if (!el.legend._built) {
      el.legend._built = true; el.legend._counts = {};
      for (const a of ATTR_ORDER) {
        const item = document.createElement('div'); item.className = 'legend-item';
        const sq = document.createElement('span'); sq.className = 'legend-sq'; sq.style.background = `var(--attr-${a})`; sq.textContent = ATTR[a].glyph;
        const lab = document.createElement('span'); lab.innerHTML = `${ATTR[a].label} <b data-c="${a}">0</b>`;
        item.append(sq, lab); el.legend.appendChild(item); el.legend._counts[a] = lab.querySelector('b');
      }
    }
    for (const a of ATTR_ORDER) el.legend._counts[a].textContent = mix[a] || 0;
  }

  function upsertShop(s) {
    const id = String(s.storeId);
    let sh = shops.get(id) || createShop(id);
    const alive = !!s.alive, rank = Number(s.rank) || 0, queueLen = Number(s.queueLen) || 0;
    sh.root.dataset.alive = String(alive);
    sh.root.dataset.atrisk = String(!!s.atRisk);
    sh.root.dataset.leader = String(alive && rank === 1);
    sh.rank.textContent = alive ? (rank > 0 ? '#' + rank : '–') : '✕';
    sh.name.textContent = s.displayName || id;
    sh.name.title = (s.displayName || id) + '（' + id + '）';
    sh.served.textContent = '提供 ' + (Number(s.servedCount) || 0);
    sh.rankKey = alive ? rank : 10000 + (typeof s.finalRank === 'number' ? s.finalRank : 999);

    const mix = s.queueByAttr || {};
    if (queueLen === 0) {
      // 空
      sh.queue.hidden = true; sh.count.hidden = true; sh.emptyEl.hidden = false;
      clearSquares(sh.queue);
    } else if (queueLen <= SHOP_SQUARE_CAP) {
      // 個々の四角
      sh.emptyEl.hidden = true; sh.count.hidden = true; sh.queue.hidden = false;
      reconcileSquares(sh.queue, mix, SHOP_SQUARE_CAP);
    } else {
      // 人数表示（多い場合）
      sh.emptyEl.hidden = true; sh.queue.hidden = true; sh.count.hidden = false;
      clearSquares(sh.queue);
      sh.countNum.textContent = queueLen;
      renderCountBars(sh.countBars, mix);
    }
    shops.set(id, sh);
  }

  function createShop(id) {
    const root = document.createElement('div');
    root.className = 'shop'; root.dataset.alive = 'true';
    root.innerHTML = `
      <div class="shop-head">
        <span class="shop-rank">–</span>
        <span class="shop-name"></span>
        <span class="shop-served">提供 0</span>
      </div>
      <div class="shop-queue"></div>
      <div class="shop-count" hidden><div class="bars"></div><b>0</b><span>人</span></div>
      <div class="shop-empty" hidden>空き</div>`;
    const sh = {
      root, rank: root.querySelector('.shop-rank'), name: root.querySelector('.shop-name'),
      served: root.querySelector('.shop-served'),
      queue: root.querySelector('.shop-queue'), count: root.querySelector('.shop-count'),
      countNum: root.querySelector('.shop-count b'), countBars: root.querySelector('.shop-count .bars'),
      emptyEl: root.querySelector('.shop-empty'), rankKey: 9999,
    };
    el.flowGrid.appendChild(root); shops.set(id, sh); return sh;
  }

  // count モードの属性内訳ミニ棒
  function renderCountBars(container, mix) {
    const total = mixTotal(mix) || 1;
    container.innerHTML = '';
    for (const a of ATTR_ORDER) {
      const v = mix[a] || 0; if (v === 0) continue;
      const bar = document.createElement('i');
      bar.style.background = `var(--attr-${a})`;
      bar.style.height = Math.max(4, (v / total) * 22).toFixed(0) + 'px';
      bar.title = `${ATTR[a].label}: ${v}`;
      container.appendChild(bar);
    }
  }

  // 客四角の増減をコンテナ内で属性別に調整する（差分更新でアニメを効かせる）。
  // 合計が cap を超える場合は各属性を比例縮約して cap 以内に収める。
  function reconcileSquares(container, mix, cap) {
    const raw = ATTR_ORDER.map(a => ({ a, v: mix[a] || 0 }));
    let total = raw.reduce((s, x) => s + x.v, 0);
    let want = raw;
    if (total > cap) {
      const scale = cap / total;
      want = raw.map(x => ({ a: x.a, v: Math.round(x.v * scale) }));
    }
    for (const { a, v } of want) {
      let have = container.querySelectorAll(`.cust[data-attr="${a}"]:not(.leaving)`);
      let n = have.length;
      while (n < v) { container.appendChild(makeCust(a)); n++; }
      for (let i = v; i < have.length; i++) removeCust(have[i]);
    }
  }

  function makeCust(a) {
    const sq = document.createElement('span');
    sq.className = 'cust'; sq.dataset.attr = a; sq.textContent = ATTR[a].glyph;
    sq.title = ATTR[a].label;
    return sq;
  }
  function removeCust(node) {
    node.classList.add('leaving');
    node.addEventListener('animationend', () => node.remove(), { once: true });
    // フォールバック（animationend が来ない環境）
    setTimeout(() => node.remove(), 400);
  }
  function clearSquares(container) {
    const live = container.querySelectorAll('.cust:not(.leaving)');
    live.forEach(removeCust);
  }

  // ── 設定パネル ──
  const openSettings = () => { el.settings.hidden = false; };
  const toggleSettings = () => { el.settings.hidden = !el.settings.hidden; };

  // ── 初期化 ──
  function init() {
    el.serverInput.value = localStorage.getItem(LS.server) || '';
    el.tokenInput.value = urlToken() || localStorage.getItem(LS.token) || '';
    el.sortMode.value = localStorage.getItem(LS.sort) || 'rank';

    el.tabs.forEach(t => t.addEventListener('click', () => switchTab(t.dataset.tab)));
    el.settingsBtn.addEventListener('click', toggleSettings);
    el.sortMode.addEventListener('change', () => { localStorage.setItem(LS.sort, el.sortMode.value); applySort(); });
    el.connectBtn.addEventListener('click', () => {
      localStorage.setItem(LS.server, el.serverInput.value.trim());
      localStorage.setItem(LS.token, el.tokenInput.value.trim());
      el.settings.hidden = true; connect();
    });
    document.addEventListener('visibilitychange', () => { if (!document.hidden && (!ws || ws.readyState > 1)) connect(); });

    switchTab(localStorage.getItem(LS.tab) || 'board');
    connect();
  }

  init();
})();
