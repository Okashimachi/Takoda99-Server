// タコダ99 観測ダッシュボード — 素の JS。
// サーバーの /admin/ws（読み取り専用の観測ストリーム）を購読し、99店の盤面を描く。
//
// ワイヤ契約（plan-h00 §4）: /admin/ws は proto.Envelope{type, payload} を流す。
//   h01: type="StoreListUpdate", payload={stores:[StoreSummary], aliveCount}
//   （h02 で type="AdminSnapshot" が増える。ここでは未知 type は無視して前方互換にする）
'use strict';

(() => {
  const $ = (id) => document.getElementById(id);

  const el = {
    banner: $('banner'),
    connDot: $('conn-dot'),
    aliveCount: $('alive-count'),
    totalCount: $('total-count'),
    deadCount: $('dead-count'),
    progressFill: $('progress-fill'),
    lastUpdate: $('last-update'),
    sortMode: $('sort-mode'),
    settingsBtn: $('settings-btn'),
    settings: $('settings'),
    serverInput: $('server-input'),
    tokenInput: $('token-input'),
    connectBtn: $('connect-btn'),
    grid: $('grid'),
    empty: $('empty'),
    emptyMsg: $('empty-msg'),
  };

  const LS = {
    server: 'takoda99.dash.server',
    token: 'takoda99.dash.token',
    sort: 'takoda99.dash.sort',
  };

  // ── 状態 ──
  const cells = new Map();       // storeId -> { root, els..., prev:{creditLife,alive} }
  let lifeMax = 3;               // 観測した体力の最大値（バーの分母。config で変わりうる）
  let ws = null;
  let reconnectTimer = null;
  let backoff = 1000;            // 再接続バックオフ（ms）
  let manualClose = false;

  // ── 設定の読み書き ──
  function urlToken() {
    return new URLSearchParams(location.search).get('token') || '';
  }
  function getServer() {
    return (el.serverInput.value || localStorage.getItem(LS.server) || '').trim();
  }
  function getToken() {
    return (el.tokenInput.value || urlToken() || localStorage.getItem(LS.token) || '').trim();
  }

  // ── WebSocket URL の組み立て ──
  function buildWsUrl() {
    const token = getToken();
    const server = getServer();
    let base;
    if (server) {
      // 明示サーバー（cross-origin 開発など）。http(s)/ws(s) いずれの入力も受ける。
      base = server.replace(/^http/, 'ws').replace(/\/+$/, '');
      if (!/^wss?:\/\//.test(base)) {
        base = (location.protocol === 'https:' ? 'wss://' : 'ws://') + base;
      }
    } else {
      // このページを配信しているサーバー（/admin 同梱配信）と同一オリジン。
      const scheme = location.protocol === 'https:' ? 'wss://' : 'ws://';
      base = scheme + location.host;
    }
    const q = token ? ('?token=' + encodeURIComponent(token)) : '';
    return base + '/admin/ws' + q;
  }

  // ── 接続状態表示 ──
  function setConn(state) {
    el.connDot.dataset.state = state; // connecting | open | closed
  }
  function showBanner(msg, kind) {
    el.banner.textContent = msg;
    el.banner.dataset.kind = kind || 'warn';
    el.banner.hidden = false;
  }
  function hideBanner() { el.banner.hidden = true; }

  function showEmpty(msg) {
    el.emptyMsg.textContent = msg;
    el.empty.hidden = false;
  }
  function hideEmpty() { el.empty.hidden = true; }

  // ── 接続 ──
  function connect() {
    disconnect(true);
    manualClose = false;
    const token = getToken();
    if (!token) {
      setConn('closed');
      showEmpty('管理トークンが必要です。⚙ から入力してください。');
      openSettings();
      return;
    }

    let url;
    try { url = buildWsUrl(); } catch (e) { showBanner('URL が不正です: ' + e.message, 'error'); return; }

    setConn('connecting');
    showEmpty('サーバーに接続しています…');
    try {
      ws = new WebSocket(url);
    } catch (e) {
      scheduleReconnect('接続に失敗しました');
      return;
    }

    ws.onopen = () => {
      setConn('open');
      hideBanner();
      backoff = 1000;
    };
    ws.onmessage = (ev) => {
      let env;
      try { env = JSON.parse(ev.data); } catch { return; }
      handleEnvelope(env);
    };
    ws.onclose = (ev) => {
      setConn('closed');
      if (manualClose) return;
      // 1008=policy(トークン不一致想定) / 4403 等は再試行しても無駄なので案内を出す。
      if (ev.code === 1008 || ev.code === 4001 || ev.code === 4403) {
        showBanner('認証に失敗しました。トークンを確認してください。', 'error');
        openSettings();
        return;
      }
      scheduleReconnect('接続が切れました。再接続します…');
    };
    ws.onerror = () => { /* onclose に集約 */ };
  }

  function disconnect(silent) {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    if (ws) {
      manualClose = true;
      try { ws.close(); } catch {}
      ws = null;
    }
    if (!silent) setConn('closed');
  }

  function scheduleReconnect(msg) {
    showBanner(msg, 'warn');
    if (reconnectTimer) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, backoff);
    backoff = Math.min(backoff * 1.6, 10000);
  }

  // ── メッセージ処理 ──
  function handleEnvelope(env) {
    if (!env || typeof env.type !== 'string') return;
    if (env.type === 'StoreListUpdate') {
      render(env.payload);
    }
    // 未知 type（h02 の AdminSnapshot 等）は現状無視（前方互換）。
  }

  // ── 描画 ──
  function render(payload) {
    if (!payload || !Array.isArray(payload.stores)) return;
    const stores = payload.stores;
    const aliveCount = typeof payload.aliveCount === 'number' ? payload.aliveCount : stores.filter(s => s.alive).length;

    hideEmpty();

    // 体力の分母を更新（初期体力が config で変わっても追従）。
    for (const s of stores) {
      if (typeof s.creditLife === 'number' && s.creditLife > lifeMax) lifeMax = s.creditLife;
    }

    for (const s of stores) upsertCell(s);

    // 消えた店（再マッチで店IDが変わった等）のセルを掃除。
    const present = new Set(stores.map(s => String(s.storeId)));
    for (const [id, c] of cells) {
      if (!present.has(id)) { c.root.remove(); cells.delete(id); }
    }

    applySort();
    updateStats(stores, aliveCount);
  }

  function lifeState(life, alive) {
    if (!alive) return 'dead';
    if (life <= 1) return 'crit';
    if (life <= Math.max(2, Math.ceil(lifeMax * 0.34))) return 'warn';
    return 'ok';
  }

  function upsertCell(s) {
    const id = String(s.storeId);
    let c = cells.get(id);
    if (!c) c = createCell(id);

    const life = Number(s.creditLife) || 0;
    const alive = !!s.alive;
    const evalN = Math.max(0, Math.min(1, Number(s.evalNormalized) || 0));
    const rank = Number(s.rank) || 0;

    // 遷移アニメ: 体力減 / 脱落
    if (c.prev) {
      if (alive && c.prev.alive && life < c.prev.life) flash(c.root, 'flash-damage');
      if (!alive && c.prev.alive) flash(c.root, 'flash-dead');
    }

    // 名前・順位
    c.name.textContent = s.displayName || id;
    c.name.title = (s.displayName || id) + '（' + id + '）';

    // 体力レール状態 & リーダー
    const ls = lifeState(life, alive);
    c.root.dataset.alive = String(alive);
    c.root.dataset.life = ls === 'ok' || ls === 'dead' ? 'ok' : ls;
    c.root.dataset.leader = String(alive && rank === 1);

    // 順位 or 最終順位
    if (alive) {
      c.rank.textContent = rank > 0 ? '#' + rank : '–';
    } else {
      c.rank.textContent = '✕';
    }
    const fr = s.finalRank;
    c.finalBadge.textContent = (typeof fr === 'number') ? (fr + '位') : '脱落';

    // 体力ピップ
    renderPips(c, life);
    c.lifeNum.textContent = life + '/' + lifeMax;

    // 評価バー
    c.evalFill.style.width = (evalN * 100).toFixed(1) + '%';
    c.evalPct.textContent = Math.round(evalN * 100) + '%';

    // ソート用のキーを保持。脱落店は生存店の下に沈め、最終順位の昇順（2位→…→99位）で
    // 並べる＝優勝の直下に準優勝が来るリーダーボード順。finalRank 欠落は最後尾へ。
    c.sortKeys = { rank: alive ? rank : 10000 + (typeof fr === 'number' ? fr : 999), life, alive, id };
    c.prev = { life, alive };
    cells.set(id, c);
  }

  function renderPips(c, life) {
    // pip 数は lifeMax（上限 20 まで。超過は数値表示に委ねる）。
    const want = Math.min(lifeMax, 20);
    while (c.pips.length < want) {
      const p = document.createElement('span');
      p.className = 'pip';
      c.life.insertBefore(p, c.lifeNum);
      c.pips.push(p);
    }
    while (c.pips.length > want) {
      c.pips.pop().remove();
    }
    for (let i = 0; i < c.pips.length; i++) {
      c.pips[i].classList.toggle('empty', i >= life);
    }
  }

  function createCell(id) {
    const root = document.createElement('div');
    root.className = 'cell';
    root.dataset.alive = 'true';
    root.innerHTML = `
      <div class="cell-head">
        <span class="rank">–</span>
        <span class="name"></span>
      </div>
      <div class="life"><span class="life-num"></span></div>
      <div class="eval">
        <div class="eval-track"><div class="eval-fill"></div></div>
        <div class="eval-row"><span class="eval-label">評価</span><span class="eval-pct">0%</span></div>
      </div>
      <span class="final-badge">脱落</span>
    `;
    const c = {
      root,
      rank: root.querySelector('.rank'),
      name: root.querySelector('.name'),
      life: root.querySelector('.life'),
      lifeNum: root.querySelector('.life-num'),
      evalFill: root.querySelector('.eval-fill'),
      evalPct: root.querySelector('.eval-pct'),
      finalBadge: root.querySelector('.final-badge'),
      pips: [],
      prev: null,
      sortKeys: { rank: 9999, life: 0, alive: true, id },
    };
    el.grid.appendChild(root);
    cells.set(id, c);
    return c;
  }

  let flashRAF = null;
  function flash(node, cls) {
    node.classList.remove(cls);
    // reflow を強制して再アニメーションさせる
    void node.offsetWidth;
    node.classList.add(cls);
    node.addEventListener('animationend', () => node.classList.remove(cls), { once: true });
  }

  // ── ソート（CSS order で並べ替え。DOM ノードは storeId 固定で入替アニメを効かせる） ──
  function applySort() {
    const mode = el.sortMode.value;
    const arr = [...cells.values()];
    arr.sort((a, b) => {
      const ka = a.sortKeys, kb = b.sortKeys;
      if (mode === 'id') return cmpId(ka.id, kb.id);
      if (mode === 'life') {
        // 生存を上に、体力少ない順（＝危ない店が上）。脱落は下。
        if (ka.alive !== kb.alive) return ka.alive ? -1 : 1;
        if (ka.alive) return (ka.life - kb.life) || (ka.rank - kb.rank);
        return ka.rank - kb.rank; // 脱落は最終順位の昇順で下段に
      }
      // rank（既定）: 生存を評価順で上に、脱落は最終順位で下に。
      return ka.rank - kb.rank;
    });
    arr.forEach((c, i) => { c.root.style.order = i; });
  }

  function cmpId(a, b) {
    // "p-3" 形式なら数値比較、それ以外は文字列比較。
    const na = parseInt(String(a).replace(/\D/g, ''), 10);
    const nb = parseInt(String(b).replace(/\D/g, ''), 10);
    if (!isNaN(na) && !isNaN(nb) && na !== nb) return na - nb;
    return String(a).localeCompare(String(b));
  }

  // ── スタッツ ──
  function updateStats(stores, aliveCount) {
    const total = stores.length || 99;
    const dead = total - aliveCount;
    el.aliveCount.textContent = aliveCount;
    el.totalCount.textContent = total;
    el.deadCount.textContent = dead;
    const pct = total > 1 ? (dead / (total - 1)) * 100 : 0; // 1店残ったら 100%
    el.progressFill.style.width = Math.max(0, Math.min(100, pct)).toFixed(1) + '%';
    el.lastUpdate.textContent = new Date().toLocaleTimeString('ja-JP', { hour12: false });
  }

  // ── 設定パネル ──
  function openSettings() { el.settings.hidden = false; }
  function toggleSettings() { el.settings.hidden = !el.settings.hidden; }

  // ── 初期化 ──
  function init() {
    el.serverInput.value = localStorage.getItem(LS.server) || '';
    el.tokenInput.value = urlToken() || localStorage.getItem(LS.token) || '';
    el.sortMode.value = localStorage.getItem(LS.sort) || 'rank';

    el.settingsBtn.addEventListener('click', toggleSettings);
    el.sortMode.addEventListener('change', () => {
      localStorage.setItem(LS.sort, el.sortMode.value);
      applySort();
    });
    el.connectBtn.addEventListener('click', () => {
      localStorage.setItem(LS.server, el.serverInput.value.trim());
      localStorage.setItem(LS.token, el.tokenInput.value.trim());
      el.settings.hidden = true;
      connect();
    });

    // ページ非表示中に切れたら、復帰時に即再接続。
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden && (!ws || ws.readyState > 1)) connect();
    });

    connect();
  }

  init();
})();
