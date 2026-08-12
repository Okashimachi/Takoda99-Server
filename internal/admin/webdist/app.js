// タコダ99 観測ダッシュボード — 素の JS。
// サーバーの /admin/ws（読み取り専用の観測ストリーム）を購読し、2タブで盤面を描く。
//   店舗盤面: 99店の体力/評価/順位/行列/提供数/storm対象圏。
//   客フロー: 待機エリア(restPool)と各店の行列を、客属性(ジャンル)別の四角で可視化。
//
// ワイヤ契約（plan-h00 §4 / plan-h02）: /admin/ws は proto.Envelope{type,payload}。
//   h02: type="AdminSnapshot"（本ダッシュボードの主データ）。
//   h01: type="StoreListUpdate"（後方互換。store情報のみ・客フローは出せない）。
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
    storm: $('storm'), stormUntil: $('storm-until'), stormPct: $('storm-pct'),
    // views
    viewBoard: $('view-board'), viewFlow: $('view-flow'),
    grid: $('grid'), legend: $('legend'),
    restCount: $('rest-count'), restPool: $('rest-pool'), flowGrid: $('flow-grid'),
    empty: $('empty'), emptyMsg: $('empty-msg'),
  };

  const LS = { server: 'takoda99.dash.server', token: 'takoda99.dash.token', sort: 'takoda99.dash.sort', tab: 'takoda99.dash.tab' };

  // ── 状態 ──
  const cells = new Map();  // storeId -> board cell refs
  const shops = new Map();  // storeId -> flow shop refs
  let lifeMax = 3;
  let activeTab = 'board';
  let lastSnapshot = null;
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
    if (env.type === 'StoreListUpdate') { onSnapshot(storeListToSnapshot(env.payload)); return; }
    // 未知 type は無視（前方互換）。
  }

  // h01 互換: StoreListUpdate を AdminSnapshot 形へ最小変換（客フロー情報は空）。
  function storeListToSnapshot(p) {
    if (!p || !Array.isArray(p.stores)) return null;
    return {
      matchId: '', elapsedMs: 0, phase: '', heatLevel: 0,
      aliveCount: typeof p.aliveCount === 'number' ? p.aliveCount : p.stores.filter(s => s.alive).length,
      restPool: 0, storm: { warning: false }, customers: {}, restByAttr: {},
      stores: p.stores.map(s => ({
        storeId: s.storeId, displayName: s.displayName, alive: s.alive, rank: s.rank,
        finalRank: s.finalRank, creditLife: s.creditLife, evalNormalized: s.evalNormalized,
        queueLen: 0, servedCount: 0, atRisk: false, queueByAttr: {},
      })),
    };
  }

  function onSnapshot(snap) {
    if (!snap || !Array.isArray(snap.stores)) return;
    lastSnapshot = snap;
    hideEmpty();
    armIdle();
    for (const s of snap.stores) {
      if (typeof s.creditLife === 'number' && s.creditLife > lifeMax) lifeMax = s.creditLife;
    }
    renderMetrics(snap);
    renderActive();
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
    const pct = total > 1 ? (dead / (total - 1)) * 100 : 0;
    el.mProgress.style.width = Math.max(0, Math.min(100, pct)).toFixed(1) + '%';
    el.mUpdate.textContent = new Date().toLocaleTimeString('ja-JP', { hour12: false });

    // storm 予告
    if (snap.storm && snap.storm.warning) {
      el.stormUntil.textContent = snap.storm.untilTick != null ? snap.storm.untilTick : '–';
      el.stormPct.textContent = Math.round((snap.storm.thresholdPct || 0) * 100) + '%';
      el.storm.hidden = false;
    } else {
      el.storm.hidden = true;
    }
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
    else renderFlow(lastSnapshot);
  }

  function switchTab(tab) {
    activeTab = tab;
    localStorage.setItem(LS.tab, tab);
    el.tabs.forEach(t => t.classList.toggle('is-active', t.dataset.tab === tab));
    el.viewBoard.hidden = tab !== 'board';
    el.viewFlow.hidden = tab !== 'flow';
    el.sortControl.style.visibility = tab === 'board' ? 'visible' : 'hidden';
    renderActive();
  }

  // ══ 店舗盤面（tab1） ══
  function lifeState(life, alive) {
    if (!alive) return 'dead';
    if (life <= 1) return 'crit';
    if (life <= Math.max(2, Math.ceil(lifeMax * 0.34))) return 'warn';
    return 'ok';
  }

  function renderBoard(snap) {
    for (const s of snap.stores) upsertCell(s);
    const present = new Set(snap.stores.map(s => String(s.storeId)));
    for (const [id, c] of cells) { if (!present.has(id)) { c.root.remove(); cells.delete(id); } }
    applySort();
  }

  function upsertCell(s) {
    const id = String(s.storeId);
    let c = cells.get(id) || createCell(id);
    const life = Number(s.creditLife) || 0, alive = !!s.alive;
    const evalN = Math.max(0, Math.min(1, Number(s.evalNormalized) || 0));
    const rank = Number(s.rank) || 0;
    const queueLen = Number(s.queueLen) || 0, served = Number(s.servedCount) || 0;

    if (c.prev) {
      if (alive && c.prev.alive && life < c.prev.life) flash(c.root, 'flash-damage');
      if (!alive && c.prev.alive) flash(c.root, 'flash-dead');
    }
    c.name.textContent = s.displayName || id;
    c.name.title = (s.displayName || id) + '（' + id + '）';
    const ls = lifeState(life, alive);
    c.root.dataset.alive = String(alive);
    c.root.dataset.life = (ls === 'ok' || ls === 'dead') ? 'ok' : ls;
    c.root.dataset.leader = String(alive && rank === 1);
    c.root.dataset.atrisk = String(!!s.atRisk);
    c.rank.textContent = alive ? (rank > 0 ? '#' + rank : '–') : '✕';
    const fr = s.finalRank;
    c.finalBadge.textContent = (typeof fr === 'number') ? (fr + '位') : '脱落';
    renderPips(c, life);
    c.lifeNum.textContent = life + '/' + lifeMax;
    c.queueVal.textContent = queueLen;
    c.queueWrap.classList.toggle('queue-warn', alive && queueLen >= 4);
    c.servedVal.textContent = served;
    c.evalFill.style.width = (evalN * 100).toFixed(1) + '%';
    c.evalPct.textContent = Math.round(evalN * 100) + '%';

    c.sortKeys = { rank: alive ? rank : 10000 + (typeof fr === 'number' ? fr : 999), life, queue: queueLen, alive, id };
    c.prev = { life, alive };
    cells.set(id, c);
  }

  function renderPips(c, life) {
    const want = Math.min(lifeMax, 20);
    while (c.pips.length < want) { const p = document.createElement('span'); p.className = 'pip'; c.life.insertBefore(p, c.lifeNum); c.pips.push(p); }
    while (c.pips.length > want) c.pips.pop().remove();
    for (let i = 0; i < c.pips.length; i++) c.pips[i].classList.toggle('empty', i >= life);
  }

  function createCell(id) {
    const root = document.createElement('div');
    root.className = 'cell'; root.dataset.alive = 'true';
    root.innerHTML = `
      <div class="cell-head"><span class="rank">–</span><span class="name"></span></div>
      <div class="life"><span class="life-num"></span></div>
      <div class="cell-meta">
        <span class="queue"><span class="k">行列</span> <b class="queue-val">0</b></span>
        <span class="served"><span class="k">提供</span> <b class="served-val">0</b></span>
      </div>
      <div class="eval">
        <div class="eval-track"><div class="eval-fill"></div></div>
        <div class="eval-row"><span class="eval-label">評価</span><span class="eval-pct">0%</span></div>
      </div>
      <span class="final-badge">脱落</span>`;
    const c = {
      root, rank: root.querySelector('.rank'), name: root.querySelector('.name'),
      life: root.querySelector('.life'), lifeNum: root.querySelector('.life-num'),
      queueWrap: root.querySelector('.queue'), queueVal: root.querySelector('.queue-val'),
      servedVal: root.querySelector('.served-val'),
      evalFill: root.querySelector('.eval-fill'), evalPct: root.querySelector('.eval-pct'),
      finalBadge: root.querySelector('.final-badge'),
      pips: [], prev: null, sortKeys: { rank: 9999, life: 0, queue: 0, alive: true, id },
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
      if (mode === 'life') { if (ka.alive !== kb.alive) return ka.alive ? -1 : 1; if (ka.alive) return (ka.life - kb.life) || (ka.rank - kb.rank); return ka.rank - kb.rank; }
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

  // ══ 客フロー（tab2） ══
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
