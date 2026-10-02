(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  const state = {
    view: "bench",          // bench | manage | registry | products | sets
    manageQ: "",
    devices: [],
    selectedId: null,
    instanceId: null,
    // Phase 8：这台设备在盘上的历次运行；tombSource 区分「墓碑（内存，有 TTL）」
    // 与「盘上历史（跨重启还在）」。
    instances: [],
    tombSource: "tomb",
    connGeneration: null,
    live: null,
    tombstone: false,
    config: null,
    turns: [],
    events: [],
    seenSeq: new Set(),
    newestSeq: 0,
    occupiedTurnId: null,
    myTurns: new Set(),
    // turn_id → { name, assetId }：本会话送出时记下，历史 turn 没有这层信息。
    turnMeta: {},
    ws: null,
    wsKey: "",
    products: [],
    productForm: null,     // null | {mode:"new"} | {mode:"edit", id}
    // 音频集（Phase 13）。setEdit：正在编辑的集 id，"new" = 新建表单；setSaving：PUT 在途，
    // 编辑区禁用，连点 ↑↓ 不会叠请求。setRun：正在跑的那一组（开跑时钉住设备、实例与条目）；
    // setLast：上一次跑完的汇总，留在送话条的 src-meta 里。
    audioSets: [],
    setEdit: null,
    setSaving: false,
    setRun: null,
    setLast: null,
    attachProduct: "",
    registry: [],
    regSel: { env: "", ent: "", typ: "" },
    regEdit: "",           // 正在行内编辑的层级：env / ent / typ
    busy: false,
    filterEnv: "",
    filterEnterprise: "",
    filterType: "",
    rosterQ: "",
    checked: new Set(),
    convMode: "bubble",
    convSigs: [],
    tapeOpen: new Set(),
    tapeScope: "all",
    tapeQ: "",
    tapeFollow: true,
    tapeRows: [],
    framesByTurn: {},
    needFrames: new Set(),
    framesDebounce: 0,
    framePoll: 0,
    flashTimer: 0,
    sideTab: "tape",
    // 送话源：'' | sample:<url> | set:<id> | asset:<id> | rec | local
    srcKey: "",
    // 带图送话（phase14）：'' 或图片资产 id，随每次送话带上
    imgKey: "",
    srcName: "",
    samples: [],
    syncWait: false,
    // 抽屉：null | new | config | assets | help | scenarios | faults | sheet | runs
    drawer: null,
    newMode: "single",
    adding: null,
    globalWs: null,
    globalEvents: [],
    globalNewest: 0,
    // newLang：下一次导入要标注的语言，和上面的 language（筛选用）是两回事。
    lib: { rows: [], format: "", language: "", kind: "", tag: "", newLang: "", editingId: null, armedId: null },
    // 麦克风录制：mr 活着即在录；file 是录完封装的 WAV，可选为音源。
    rec: { mr: null, stream: null, chunks: [], file: null, name: "" },
  };

  // 全局带最多留这么多条；调试面板不是归档，翻更早的去 /ws/events/global 拿回放。
  const GLOBAL_MAX = 500;

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    }[c]));
  }

  // ——— 幂等渲染 ———
  // 事件带一秒能来几十条 tts_chunk。无条件 innerHTML 会把用户正在填的
  // 配置表单、正在翻的历史一起冲掉，所以所有整块渲染都先比签名。
  const sigCache = new WeakMap();

  function setHTML(el, html) {
    if (!el || sigCache.get(el) === html) return false;
    sigCache.set(el, html);
    el.innerHTML = html;
    return true;
  }

  function setText(el, text) {
    if (el && el.textContent !== text) el.textContent = text;
  }

  function setNum(el, n) {
    if (el) setText(el, String(n || 0));
  }

  function show(el, on) {
    if (el) el.hidden = !on;
  }

  function setDisabled(el, off) {
    if (el && el.disabled !== !!off) el.disabled = !!off;
  }

  // ingestEvent 是高频路径，合帧后再画；用户动作仍走同步渲染，
  // 免得按钮 disabled 晚一帧被点第二下。
  const dirty = { roster: false, stage: false, conv: false, tape: false, turns: false, global: false };
  let frame = 0;
  let frameTimer = 0;

  function flushPaint() {
    if (!frame) return;
    cancelAnimationFrame(frame);
    clearTimeout(frameTimer);
    frame = 0;
    frameTimer = 0;
    const d = { ...dirty };
    dirty.roster = dirty.stage = dirty.conv = dirty.tape = dirty.turns = dirty.global = dirty.deck = false;
    if (d.roster) renderRoster();
    if (d.stage) renderStage();
    if (d.conv) renderConv();
    if (d.tape) renderTape();
    if (d.turns) renderTurns();
    if (d.global) renderGlobalTape();
    // 时间轴跟着 conv/stage 的数据走，另有 1s 心跳推 NOW 与静默区。
    if (d.deck || d.conv || d.stage) renderDeck();
  }

  function paint(...keys) {
    for (const k of keys) dirty[k] = true;
    if (frame) return;
    frame = requestAnimationFrame(flushPaint);
    // 后台标签页里 rAF 不触发，嵌入式 webview 还可能直接把它饿死。
    // 给条定时兜底，画面就不会停在半路。
    frameTimer = setTimeout(flushPaint, 250);
  }

  function flash(msg, kind) {
    const box = $("flash");
    clearTimeout(state.flashTimer);
    setText($("flash-text"), msg || "");
    setText($("flash-kind"), kind || "info");
    box.dataset.kind = msg ? (kind || "info") : "";
    box.hidden = !msg;
    // 出错的话留着让人看清；成功提示自己退场。
    if (msg && kind !== "err") {
      state.flashTimer = setTimeout(() => { box.hidden = true; }, 4000);
    }
  }

  function setWsNote(msg, tone) {
    setText($("ws-note-text"), msg || "未连接事件流");
    $("ws-note").dataset.tone = tone || "idle";
  }

  async function api(method, path, body, extra) {
    const opt = { method, headers: {} };
    if (body instanceof FormData) {
      opt.body = body;
    } else if (body !== undefined) {
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    const res = await fetch(path, opt);
    const text = await res.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = { raw: text };
      }
    }
    if (extra && extra.raw) return { ok: res.ok, status: res.status, data, text, headers: res.headers };
    if (!res.ok) {
      const err = (data && data.error) || text || res.statusText;
      const e = new Error(err);
      e.status = res.status;
      e.data = data;
      throw e;
    }
    return data;
  }

  function apiErr(err) {
    flash((err.status ? err.status + " " : "") + err.message, "err");
  }

  function identityEditable() {
    if (state.tombstone || !state.live) return false;
    const st = state.live.instance_state;
    return st === "created" || st === "stopped";
  }

  function backlogEnabled() {
    const beh = (state.config && state.config.behavior) || {};
    return Number(beh.speak_backlog_depth) > 0;
  }

  // blockedReason 是「送话不可用」那行的唯一真源；空串表示可以送。
  function blockedReason() {
    if (!state.selectedId) return "先在左边选一台设备";
    if (state.tombstone) {
      return state.tombSource === "disk"
        ? "在看以往的运行，只读；要送话先在「历史运行」里切回本次运行"
        : "连接已结束，重新启动后可再送出";
    }
    const st = (state.live && state.live.instance_state) || "created";
    if (st === "starting") return "Starting 时不能说话，等 connection_state 走到 ready";
    if (st === "failed") return "连接已结束，重新启动后可再送出";
    if (st !== "running") return "启动并 Ready 后可送出";
    if (state.occupiedTurnId && !backlogEnabled()) return "槽被占用且 speak_backlog_depth = 0，送出会 409";
    return "";
  }

  function speakableUI() {
    return !blockedReason();
  }

  function interruptableUI() {
    if (state.tombstone || !state.live || !state.instanceId) return false;
    return state.live.instance_state === "running";
  }

  function parseHash() {
    const raw = (location.hash || "").replace(/^#\/?/, "");
    if (!raw) return { id: null, ins: null };
    const [idPart, qs] = raw.split("?");
    const id = decodeURIComponent(idPart || "");
    const params = new URLSearchParams(qs || "");
    return { id: id || null, ins: params.get("ins") };
  }

  function writeHash() {
    if (!state.selectedId) {
      history.replaceState(null, "", location.pathname + location.search);
      return;
    }
    let h = "#" + encodeURIComponent(state.selectedId);
    if (state.tombstone && state.instanceId) h += "?ins=" + encodeURIComponent(state.instanceId);
    if (location.hash !== h) history.replaceState(null, "", h);
  }

  // 通道条用的设备短名：前三位 + … + 后四位（AGX0447383XVIL → AGX…XVIL）。
  function shortDev(s) {
    s = String(s || "");
    return s.length > 9 ? s.slice(0, 3) + "…" + s.slice(-4) : s;
  }

  function shortId(s) {
    const t = String(s || "");
    if (t.length <= 18) return t;
    return t.slice(0, 10) + "…" + t.slice(-4);
  }

  function shortTurn(s) {
    const t = String(s || "");
    return t.length <= 20 ? t : t.slice(0, 18) + "…";
  }

  function fmtTime(iso) {
    if (!iso) return "—";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString();
  }

  function fmtClock(iso) {
    if (!iso) return "—";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    const p = (n, w = 2) => String(n).padStart(w, "0");
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
  }

  // 名册里的时间不带毫秒：值只在真有活动时变，签名才稳得住。
  function fmtHMS(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return "";
    const p = (n) => String(n).padStart(2, "0");
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
  }

  function fmtBytes(n) {
    const v = Number(n) || 0;
    if (v < 1024) return v + " B";
    if (v < 1024 * 1024) {
      const kb = v / 1024;
      return (kb < 10 ? kb.toFixed(1) : kb.toFixed(0)) + " KB";
    }
    return (v / (1024 * 1024)).toFixed(1) + " MB";
  }

  function fmtDurMs(ms) {
    const v = Number(ms) || 0;
    if (v < 1000) return v + " ms";
    return (v / 1000).toFixed(1) + " s";
  }

  function uniqueSorted(arr) {
    return [...new Set(arr.filter((v) => v != null && String(v) !== ""))].sort((a, b) => String(a).localeCompare(String(b), "zh"));
  }

  function insTone(v) {
    return v === "running" ? "ok" : v === "failed" ? "err" : v === "starting" ? "warn" : (v === "deleted" || v === "archived") ? "mute" : "";
  }

  function connTone(v) {
    return v === "ready" ? "ok" : (!v || v === "disconnected") ? "mute" : "warn";
  }

  function tagCls(tone) {
    return tone ? "tag tag--" + tone : "tag";
  }

  // 按 device_id 稳定排序：名册每 2 秒重拉一次，跟着 last_activity 排的话
  // 鼠标底下的那一行会自己跳走，点错设备。
  function filteredDevices() {
    const q = state.rosterQ;
    return state.devices.filter((d) => {
      if (state.filterEnv && (d.environment || "") !== state.filterEnv) return false;
      if (state.filterEnterprise && (d.enterprise || "") !== state.filterEnterprise) return false;
      if (state.filterType && (d.device_type || "") !== state.filterType) return false;
      if (q) {
        const hay = [d.device_id, d.environment, d.enterprise, d.device_type, d.product, d.instance_id].join(" ").toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    }).sort((a, b) => String(a.device_id).localeCompare(String(b.device_id), "zh"));
  }

  // ——— 配置树（环境 → 厂商 → 设备类型） ———

  function regEnv() {
    return state.registry.find((e) => e.name === state.regSel.env) || null;
  }

  function regEnts() {
    const env = regEnv();
    return (env && env.enterprises) || [];
  }

  function regEnt() {
    return regEnts().find((x) => x.short_name === state.regSel.ent) || null;
  }

  function regTypes() {
    const ent = regEnt();
    return (ent && ent.device_types) || [];
  }

  async function loadRegistry() {
    try {
      const data = await api("GET", "/registry");
      state.registry = data.environments || [];
    } catch {
      state.registry = [];
    }
    // 选中项失效则回落到第一项，保证三选级联始终指向真实节点。
    if (!regEnv()) state.regSel.env = state.registry[0] ? state.registry[0].name : "";
    if (!regEnt()) state.regSel.ent = regEnts()[0] ? regEnts()[0].short_name : "";
    if (!regTypes().some((x) => x.short_name === state.regSel.typ)) {
      state.regSel.typ = regTypes()[0] ? regTypes()[0].short_name : "";
    }
    renderRegistry();
    if (!state.attachProduct) state.attachProduct = typeDefaultProduct();
    if (state.drawer === "new" || state.drawer === "config") renderDrawer();
    if (state.view === "bench") renderStage();
  }

  function attachRefs() {
    return {
      environment: state.regSel.env,
      enterprise: state.regSel.ent,
      device_type: state.regSel.typ,
    };
  }

  function typeDefaultProduct() {
    const t = regTypes().find((x) => x.short_name === state.regSel.typ);
    return (t && t.default_product) || "";
  }

  function productLabel(id) {
    const p = state.products.find((x) => x.id === id);
    return p ? (p.name ? `${p.name}（${p.id}）` : p.id) : id;
  }

  function productOpts(cur, ph) {
    const rows = state.products.map((p) => ({ v: p.id, t: p.name ? `${p.name}（${p.id}）` : p.id }));
    if (cur && !rows.some((r) => r.v === cur)) rows.push({ v: cur, t: cur });
    return selOpts(rows, cur, ph);
  }

  function audioLib() {
    return state.lib.rows.filter((a) => (a.kind || "audio") !== "image");
  }

  function imageLib() {
    return state.lib.rows.filter((a) => a.kind === "image");
  }

  function visibleLib() {
    return state.lib.rows.filter((a) => {
      const kind = a.kind || "audio";
      if (state.lib.kind && kind !== state.lib.kind) return false;
      if (state.lib.format && a.format !== state.lib.format) return false;
      if (state.lib.language && a.language !== state.lib.language) return false;
      const tagQ = (state.lib.tag || "").toLowerCase();
      if (tagQ && !(a.tags || []).join(" ").toLowerCase().includes(tagQ)) return false;
      return true;
    });
  }

  async function loadAudioSets() {
    try {
      const data = await api("GET", "/audio_sets");
      state.audioSets = data.audio_sets || [];
    } catch {
      state.audioSets = [];
    }
    renderSrc();
    if (state.view === "sets") renderSets();
  }

  async function loadProducts() {
    try {
      const data = await api("GET", "/products");
      state.products = data.products || [];
    } catch {
      state.products = [];
    }
    if (!state.attachProduct) state.attachProduct = typeDefaultProduct();
    if (state.view === "products") renderProducts();
    if (state.view === "registry") renderRegistry();
    if (state.view === "bench") renderStage();
    if (state.drawer === "config") renderDrawer();
  }

  async function refreshList() {
    const data = await api("GET", "/devices");
    state.devices = data.devices || [];
    // 名册里没有的选中项要撤掉，批量条才不会指向幽灵设备。
    for (const id of [...state.checked]) {
      if (!state.devices.some((d) => d.device_id === id)) state.checked.delete(id);
    }
    renderRoster();
    if (state.view === "manage") renderManage();
    if (state.selectedId && !state.tombstone) {
      const row = state.devices.find((d) => d.device_id === state.selectedId);
      if (row) {
        state.live = row;
        if (row.instance_id) state.instanceId = row.instance_id;
        if (row.conn_generation != null) state.connGeneration = row.conn_generation;
        renderStage();
      } else if (state.instanceId) {
        state.tombstone = true;
        state.live = null;
        closeWS(false);
        writeHash();
        renderStage();
      }
    }
  }

  // ——— 左栏 · 名册 ———

  function renderFilters() {
    const envSel = $("filter-env");
    const entSel = $("filter-enterprise");
    const typeSel = $("filter-type");
    if (!envSel || !entSel || !typeSel) return;
    const envs = uniqueSorted(state.devices.map((d) => d.environment));
    if (state.filterEnv && !envs.includes(state.filterEnv)) state.filterEnv = "";
    const entSrc = state.filterEnv
      ? state.devices.filter((d) => (d.environment || "") === state.filterEnv)
      : state.devices;
    const enterprises = uniqueSorted(entSrc.map((d) => d.enterprise));
    if (state.filterEnterprise && !enterprises.includes(state.filterEnterprise)) state.filterEnterprise = "";
    const typeSrc = state.filterEnterprise
      ? entSrc.filter((d) => (d.enterprise || "") === state.filterEnterprise)
      : entSrc;
    const types = uniqueSorted(typeSrc.map((d) => d.device_type));
    if (state.filterType && !types.includes(state.filterType)) state.filterType = "";
    const opts = (all, rows) => `<option value="">${all}</option>` +
      rows.map((v) => `<option value="${esc(v)}">${esc(v)}</option>`).join("");
    const set = (sel, html, val) => {
      if (sel.dataset.sig !== html) {
        sel.innerHTML = html;
        sel.dataset.sig = html;
      }
      if (document.activeElement !== sel) sel.value = val;
    };
    set(envSel, opts("全部环境", envs), state.filterEnv);
    set(entSel, opts("全部厂商", enterprises), state.filterEnterprise);
    set(typeSel, opts("全部类型", types), state.filterType);
  }

  function renderRoster() {
    renderFilters();
    const shown = filteredDevices();
    setNum($("roster-count"), state.devices.length);
    setNum($("lib-count"), state.lib.rows.length);

    const noneKind = state.devices.length === 0 ? "none" : shown.length === 0 ? (state.rosterQ ? "search" : "filter") : "";
    show($("roster-empty"), !!noneKind);
    if (noneKind === "none") {
      setText($("roster-empty-title"), "还没有设备");
      setText($("roster-empty-body"), "先在配置树上挂一个环境 → 厂商 → 设备类型，再建第一台设备。");
      setText($("roster-empty-cta"), "新建第一台设备");
    } else if (noneKind === "search") {
      setText($("roster-empty-title"), "没有匹配搜索的设备");
      setText($("roster-empty-body"), `「${state.rosterQ}」在 device_id、环境、厂商、类型、instance_id 里都没有命中。`);
      setText($("roster-empty-cta"), "清空搜索");
    } else if (noneKind === "filter") {
      setText($("roster-empty-title"), "没有匹配筛选的设备");
      setText($("roster-empty-body"), "当前三级筛选下没有设备，放宽一级看看。");
      setText($("roster-empty-cta"), "清空筛选");
    }
    $("roster-empty").dataset.kind = noneKind;

    setHTML($("roster-list"), shown.map((d) => {
      const on = d.device_id === state.selectedId && !state.tombstone;
      const st = String(d.instance_state || "created");
      const picked = state.checked.has(d.device_id);
      const path = [d.environment, d.enterprise, d.device_type].filter(Boolean).join(" · ");
      const tip = [shortId(d.instance_id), fmtTime(d.last_activity)].filter(Boolean).join(" · ");
      return `<li class="device${on ? " is-on" : ""}" data-id="${esc(d.device_id)}" title="${esc(tip)}" tabindex="0" role="button">
        <div class="device__top">
          <button type="button" class="device__box${picked ? " is-on" : ""}" data-check="${esc(d.device_id)}" title="多选以批量启停删" aria-pressed="${picked}">${picked ? "✓" : ""}</button>
          <span class="led led--${esc(st)}" aria-hidden="true"></span>
          <span class="device__id">${esc(d.device_id)}</span>
          <span class="device__sid">${esc(shortDev(d.device_id))}</span>
          <span class="grow"></span>
          <span class="device__act">${esc(fmtHMS(d.last_activity))}</span>
        </div>
        <div class="device__body">
          ${path ? `<div class="device__path">${esc(path)}</div>` : ""}
          <div class="device__tags">
            <span class="${tagCls(insTone(st))}">${esc(st)}</span>
            <span class="${tagCls(connTone(d.connection_state))}">${esc(d.connection_state || "—")}</span>
            ${d.product ? `<span class="tag tag--acc" title="${esc(d.product)}">${esc(d.product)}</span>` : ""}
            ${d.overridden ? `<span class="tag tag--warn">临时覆盖</span>` : ""}
            ${d.fault ? `<span class="tag tag--warn" title="已注入故障，行为会偏离正常设备">${esc(d.fault)}</span>` : ""}
            ${d.last_error ? `<span class="tag tag--err" title="${esc(d.last_error)}">error</span>` : ""}
          </div>
        </div>
      </li>`;
    }).join(""));

    const n = state.checked.size;
    show($("batch-bar"), n > 0);
    setNum($("batch-count"), n);
  }

  // ——— 中栏 · 状态区 ———

  function renderStage() {
    const has = !!state.selectedId;
    const live = state.live || {};
    const st = !has ? "" : state.tombstone ? (state.tombSource === "disk" ? "archived" : "deleted") : (live.instance_state || "created");
    const conn = live.connection_state || "—";

    $("stage-led").className = "led" + (st ? " led--" + st : "");
    setText($("stage-name"), state.selectedId || "—");
    const kind = $("stage-kind");
    show(kind, has);
    if (has) {
      setText(kind, state.tombstone ? "历史" : "实时");
      kind.className = state.tombstone ? "tag tag--mute" : "tag";
    }

    const backlog = Number(live.speak_backlog_len) || 0;
    setHTML($("talk-facts"), !has ? "" : `
      <span class="${tagCls(insTone(st))}">${esc(st)}</span>
      <span class="${tagCls(connTone(conn))}">${esc(conn)}</span>
      <span class="sep">|</span>
      <span title="${esc(state.instanceId || "")}">${esc(shortId(state.instanceId) || "—")}</span>
      <span class="sep">|</span>
      <span title="conn_generation · 这条连接的第几代">gen ${esc(state.connGeneration ?? live.conn_generation ?? "—")}</span>
      <span class="sep">|</span>
      <span title="speak backlog 排队数">backlog ${backlog}</span>
      ${live.product ? `<span class="sep">|</span><span class="tag tag--acc" title="本次运行的产品">${esc(live.product)}</span>` : ""}
      ${live.overridden ? `<span class="tag tag--warn">临时覆盖</span>` : ""}
      ${live.fault ? `<span class="sep">|</span><span class="tag tag--warn" title="已注入故障，行为会偏离正常设备">fault: ${esc(live.fault)}</span>` : ""}
    `);

    // last_error 原来只藏在 title 里，工位上根本看不见。
    show($("talk-error"), !!live.last_error);
    setText($("talk-error-text"), live.last_error || "");

    const slot = !!state.occupiedTurnId;
    show($("slot-banner"), slot);
    if (slot) {
      const q = backlog > 0 ? ` · 队列 ${backlog} 项` : "";
      setText($("slot-text"), `槽占用 ${state.occupiedTurnId} · 等 turn_terminal${q}${downIdleNote()}`);
    }

    show($("tomb-banner"), state.tombstone);
    if (state.tombstone) {
      const disk = state.tombSource === "disk";
      setText($("tomb-kind"), disk ? "历史运行 · 只读回看" : "墓碑态 · 只读回看");
      setText($("tomb-meta"), state.tombSource === "disk"
        ? `instance_id ${state.instanceId || "—"} · 盘上的旧运行 · 只读`
        : `instance_id ${state.instanceId || "—"} · 事件与 turn 只读 · TTL 24 小时`);
    }

    setHTML($("attach-bar"), attachBarHTML());
    // 靠 2 秒一次的名册轮询把静默倒计时推着走——下行静默那段时间没有任何事件
    // 进来，不主动重画的话界面会停在最后一包上不动。renderConv 按签名增量重画，
    // 没变化时是空转，所以这里不加条件（加了反而漏：occupiedTurnId 不一定在）。
    renderConv();
    renderDeck();
    setDisabled($("btn-start"), !has || state.tombstone || state.busy || !identityEditable() || !state.attachProduct);
    setDisabled($("btn-stop"), !has || state.tombstone || state.busy);
    setDisabled($("btn-delete"), !has || state.tombstone || state.busy);
    setDisabled($("btn-config"), !has);
    setDisabled($("btn-faults"), !has || state.tombstone);
    setDisabled($("btn-runs"), !has);

    const blocked = has ? blockedReason() : "先在左边选一台设备";
    show($("send-blocked"), !!blocked);
    setText($("send-blocked-text"), blocked);
    const can = !blocked && !state.busy && !!state.srcKey;
    setDisabled($("btn-speak"), !can);
    $("btn-speak").title = state.occupiedTurnId && backlogEnabled()
      ? "槽占用 · 送出将进队列" : "Ctrl/⌘ + Enter 送出";
    // 跑音频集时 busy 一直为真，但打断必须能点：它同时是「这一条跑完就停」的开关。
    setDisabled($("btn-interrupt"), !interruptableUI() || (state.busy && !state.setRun));
    $("form-speak").classList.toggle("is-ready", !blocked);
    renderSrc();
    if (state.drawer === "config") renderDrawer();
  }

  // ——— 中栏 · 对话 ———

  // convTurns 用事件里的 turn_id 首次出现顺序拉出轮次，再拿 /turns 的终态补齐。
  // 好处是「刚 speak_permit、还没终态」的 turn 也立刻长在这条竖轴上。
  function convTurns() {
    const byId = new Map();
    for (const ev of state.events) {
      const tid = ev.turn_id;
      if (!tid) continue;
      let t = byId.get(tid);
      if (!t) {
        t = { turn_id: tid, ts: ev.ts, evCount: 0, hasAsr: false, photoCmd: "", photoUp: "", photoSkip: "", seq: ev.event_seq };
        byId.set(tid, t);
      }
      t.evCount++;
      if (ev.event_type === "asr_result") t.hasAsr = true;
      if (ev.event_type === "photo_command") t.photoCmd = ev.reason || "拍照指令";
      if (ev.event_type === "photo_uploaded") t.photoUp = ev.reason || "已传图";
      if (ev.event_type === "photo_skipped") t.photoSkip = ev.reason || "跳过";
    }
    for (const rec of state.turns) {
      if (rec.turn_id && !byId.has(rec.turn_id)) {
        byId.set(rec.turn_id, { turn_id: rec.turn_id, ts: "", evCount: 0, hasAsr: false, photoCmd: "", photoUp: "", photoSkip: "", seq: Number.MAX_SAFE_INTEGER });
      }
    }
    const recById = new Map(state.turns.map((r) => [r.turn_id, r]));
    return [...byId.values()].sort((a, b) => a.seq - b.seq).map((t) => {
      const rec = recById.get(t.turn_id) || {};
      const f = state.framesByTurn[t.turn_id] || { up: [], down: [] };
      const upBytes = f.up.reduce((s, p) => s + (Number(p.payload_len) || 0), 0);
      const downBytes = f.down.reduce((s, p) => s + (Number(p.payload_len) || 0), 0);
      const meta = state.turnMeta[t.turn_id] || {};
      const done = !!rec.turn_end_reason;
      const liveNow = state.occupiedTurnId === t.turn_id;
      let phase = null;
      if (!done && liveNow) phase = f.down.length ? "tts" : t.hasAsr ? "asr" : "up";
      return {
        id: t.turn_id,
        clock: fmtHMS(t.ts),
        evCount: t.evCount,
        up: f.up.length, upBytes,
        down: f.down.length, downBytes,
        hasAsr: t.hasAsr,
        photoCmd: t.photoCmd, photoUp: t.photoUp, photoSkip: t.photoSkip,
        file: meta.name || "",
        assetId: meta.assetId || "",
        kind: rec.reply_kind || "",
        end: rec.turn_end_reason || "",
        uplinkEnd: rec.uplink_end_reason || "",
        done, phase,
      };
    });
  }

  function bars(n, cap, live, cls) {
    const k = Math.min(Number(n) || 0, cap);
    if (!k && !live) return "";
    return `<div class="bars${cls}">${"<i></i>".repeat(k)}${live ? `<i class="is-live"></i>` : ""}</div>`;
  }

  function audioHref(turnId, dir) {
    return `/devices/${encodeURIComponent(state.selectedId || "")}/turns/${encodeURIComponent(turnId)}/audio/${dir}?instance_id=${encodeURIComponent(state.instanceId || "")}`;
  }

  function downlinkHref(turnId) {
    return audioHref(turnId, "downlink");
  }

  function termTone(t) {
    if (!t.done) return "mute";
    return t.end === "idle" ? "acc" : t.end === "interrupted" ? "warn" : "";
  }

  function turnActs(t, playLabel) {
    const hasTts = String(t.kind || "").includes("tts");
    return [
      hasTts ? `<button type="button" class="btn btn--ok btn--tiny" data-play="down" data-turn="${esc(t.id)}">▶ ${playLabel}</button>` : "",
      t.up ? `<button type="button" class="btn btn--icon btn--tiny" data-play="up" data-turn="${esc(t.id)}">▶ 上行</button>` : "",
      hasTts ? `<a class="btn btn--icon btn--tiny" href="${esc(downlinkHref(t.id))}" download="${esc(t.id)}-downlink.wav">↓ WAV</a>` : "",
    ].filter(Boolean).join("");
  }

  // 气泡主行原本永远在讲 asr_result——而这些服务端根本不发这条（协议 §8.2：
  // 没有稳定的下行 Stage=2，也没规定必发 asr_result）。真正在变的是「收到哪了、
  // 还差几秒收尾」，所以主行改讲状态，asr 只在真有的时候占主行。
  // 拍照两条路（phase14）：带图送话 = 先传图再说话，回复是普通 TTS；
  // 指令拍照 = 指令 → 传图 → 图片分析的语音回复。
  function isSpeakPhoto(t) {
    return !t.photoCmd && t.photoUp.startsWith("source=speak");
  }

  // photoHref 传图留档；uuid 取自 photo_uploaded 的 reason。
  function photoHref(t) {
    const m = /(?:^| )uuid=(\d+)/.exec(t.photoUp || "");
    return `/devices/${encodeURIComponent(state.selectedId || "")}/turns/${encodeURIComponent(t.id)}/photo?instance_id=${encodeURIComponent(state.instanceId || "")}${m ? "&uuid=" + m[1] : ""}`;
  }

  function photoThumb(t, title) {
    return `<a class="bub__photo" href="${esc(photoHref(t))}" target="_blank" rel="noopener" title="${esc(title)} · 点开看原图">` +
      `<img class="thumb" src="${esc(photoHref(t))}" alt="未留档（save_uplink_audio 关着）" loading="lazy"></a>`;
  }

  function photoLine(t) {
    if (!t.photoCmd && !t.photoUp && !t.photoSkip) return "";
    if (isSpeakPhoto(t)) return "";
    const bits = ["photo_command" + (t.photoCmd && t.photoCmd !== "拍照指令" ? " " + t.photoCmd : "")];
    if (t.photoUp) bits.push("photo_uploaded " + t.photoUp);
    else if (t.photoSkip) bits.push("photo_skipped " + t.photoSkip);
    else bits.push("传图中");
    if (t.down || String(t.kind || "").includes("tts")) bits.push("图片分析的语音回复");
    return bits.join(" → ");
  }

  function replyPhase(t) {
    if (t.hasAsr) {
      return { sub: "asr_result", line: "服务端已返回 asr_result（协议不带识别文本，原文见右栏事件）" };
    }
    if (t.done) {
      return { sub: "已收尾", line: `本轮没有 asr_result；下行 ${t.down} 包 · ${fmtBytes(t.downBytes)}` };
    }
    if (t.down) {
      const idle = idleNoteFor(t.id);
      return {
        sub: "接收中",
        line: idle
          ? `正在接收下行 · 已 ${t.down} 包 · ${fmtBytes(t.downBytes)}${idle}`
          : `正在接收下行 · 已 ${t.down} 包 · ${fmtBytes(t.downBytes)}`,
      };
    }
    return { sub: "等回话", line: "已送完上行，等服务端回话…" };
  }

  function bubbleHTML(t) {
    const upMeta = t.up
      ? `${t.up} 包 · ${fmtBytes(t.upBytes)}`
      : (t.phase === "up" ? "受理中" : "没有帧日志");
    const ttsMeta = t.down
      ? `tts_chunk ${t.down} 包 · ${fmtBytes(t.downBytes)}${t.done ? " · tts_done" : ""}`
      : (t.done ? "本轮没有下行音频" : "等待下行");
    const reply = t.hasAsr || t.down || t.done || t.photoCmd || t.photoUp || t.photoSkip ? `
      <div class="bub bub--reply">
        <div class="bub__in">
          <div class="bub__head">
            <span class="bub__who">服务端回复</span>
            <span class="bub__sub">${esc(replyPhase(t).sub)}</span>
          </div>
          <p class="bub__asr${t.hasAsr || !t.done ? "" : " bub__asr--none"}">${esc(replyPhase(t).line)}</p>
          <div class="bub__rule"></div>
          ${bars(t.down, 30, t.phase === "tts", " bars--tts")}
          <div class="bub__meta"><span>${esc(ttsMeta)}</span></div>
          ${photoLine(t) ? `<div class="bub__meta"><span>${esc(photoLine(t))}</span>${t.photoUp ? photoThumb(t, "指令拍照传出的图") : ""}</div>` : ""}
          ${t.done ? `<div class="bub__acts">${turnActs(t, "播放下行")}</div>` : ""}
        </div>
      </div>` : "";
    return `<div class="turn" data-turn="${esc(t.id)}">
      <div class="bub bub--mine">
        <div class="bub__in">
          <div class="bub__head">
            <span class="bub__who">我送出</span>
            <span class="bub__file">${esc(t.file || "（本会话之外送出的音频）")}</span>
          </div>
          ${bars(t.up, 26, t.phase === "up", "")}
          <div class="bub__meta">
            <span>${esc(upMeta)}</span>
            ${t.assetId ? `<span class="sep">|</span><span>${esc(t.assetId)}</span>` : ""}
          </div>
          ${isSpeakPhoto(t) ? `<div class="bub__meta"><span>带图送话 · 先传图再说话</span>${photoThumb(t, "带图送话传出的图")}</div>` : ""}
        </div>
      </div>
      ${reply}
      <div class="turn__end">
        <span class="rule"></span>
        <span class="${tagCls(termTone(t))}">${esc(t.done
          ? `turn_terminal · ${t.kind || "—"} · ${t.end} · ${t.uplinkEnd || "—"}`
          : "进行中")}</span>
        <span class="turn__id" title="${esc(t.id)}">${esc(shortTurn(t.id))}</span>
        <button type="button" class="btn btn--ghost" data-jump="${esc(t.id)}">原始事件 ${t.evCount}</button>
        <span class="rule"></span>
      </div>
    </div>`;
  }

  function stageRow(cls, label, text) {
    return `<div class="stg ${cls}">
      <div class="stg__dot"><i></i></div>
      <div class="stg__label">${esc(label)}</div>
      <div class="stg__val">${text}</div>
    </div>`;
  }

  function cardHTML(t) {
    const rows = [
      stageRow(
        (t.up ? "stg--acc " : "") + (t.phase === "up" ? "stg--live" : ""),
        "上行推包",
        `${bars(t.up, 26, t.phase === "up", "")}<span class="stg__text">${esc(t.up ? `${t.up} 包 · ${fmtBytes(t.upBytes)}` : "等待受理")}</span>`,
      ),
      stageRow(
        (t.hasAsr ? "stg--fg " : "") + (t.phase === "asr" ? "stg--live" : ""),
        "asr_result",
        `<span class="stg__text stg__text--big">${t.hasAsr ? "已返回" : "—"}</span>`,
      ),
      ...(photoLine(t) ? [stageRow(
        t.photoSkip ? "stg--warn" : (t.photoUp ? "stg--ok" : "stg--acc"),
        "拍照",
        `<span class="stg__text">${esc(photoLine(t))}</span>`,
      )] : []),
      stageRow(
        (t.down ? "stg--ok " : "") + (t.phase === "tts" ? "stg--live" : ""),
        "tts_chunk → tts_done",
        `${bars(t.down, 30, t.phase === "tts", " bars--tts")}<span class="stg__text">${esc(t.down ? `${t.down} 包 · ${fmtBytes(t.downBytes)}` : "—")}</span>`,
      ),
      stageRow(
        t.done ? "stg--acc" : "",
        "turn_terminal",
        `<span class="stg__text">${esc(t.done ? `reply_kind ${t.kind || "—"} · turn_end_reason ${t.end}` : "进行中")}</span>`,
      ),
    ].join("");
    return `<div class="card${t.done ? "" : " is-live"}" data-turn="${esc(t.id)}">
      <div class="card__head">
        <span class="card__id" title="${esc(t.id)}">${esc(shortTurn(t.id))}</span>
        ${t.kind ? `<span class="tag tag--acc">${esc(t.kind)}</span>` : ""}
        <span class="${tagCls(termTone(t))}">${esc(t.done ? t.end : "running")}</span>
        <span class="grow"></span>
        <span class="card__clock">${esc(t.clock)}</span>
      </div>
      <div class="card__stages">${rows}</div>
      <div class="card__foot">
        ${turnActs(t, "下行")}
        <span class="grow"></span>
        <span class="card__clock">${esc(t.uplinkEnd || "—")}</span>
        <button type="button" class="btn btn--ghost" data-jump="${esc(t.id)}">原始事件 ${t.evCount}</button>
      </div>
    </div>`;
  }

  // 逐条比签名换 DOM：tts_chunk 高频刷新时只重画变了的那一轮。
  function renderConv() {
    const box = $("conv");
    const rows = state.selectedId ? convTurns() : [];
    const bubble = state.convMode === "bubble";
    box.classList.toggle("is-cards", !bubble);
    show($("conv-empty"), rows.length === 0);
    show(box, rows.length > 0);
    if (!rows.length) {
      state.convSigs = [];
      box.innerHTML = "";
      const st = (state.live && state.live.instance_state) || "";
      if (!state.selectedId) {
        setText($("conv-empty-title"), "从左边选一台设备");
        setText($("conv-empty-body"), "选一台设备 → 启动并等待 Ready → 送一段音频 → 看服务端回了什么。四步都在这一条竖轴上。");
      } else if (st === "running") {
        setText($("conv-empty-title"), "这条连接还没有 turn");
        setText($("conv-empty-body"), "在下面选一段音频按 Ctrl+Enter 送出，我送了什么、服务端回了什么、这轮怎么结束的都会按顺序落在这里。");
      } else if (state.tombstone) {
        setText($("conv-empty-title"), "这个实例没有留下 turn");
        setText($("conv-empty-body"), "墓碑态只读回看；右栏事件带里仍能看到这次实例的全部原始事件。");
      } else {
        setText($("conv-empty-title"), "启动后这里长出对话");
        setText($("conv-empty-body"), "选一台设备 → 启动并等待 Ready → 送一段音频 → 看服务端回了什么。四步都在这一条竖轴上。");
      }
      return;
    }
    const sigs = rows.map((t) => [
      bubble ? "b" : "c", t.id, t.up, t.upBytes, t.down, t.downBytes,
      t.hasAsr ? 1 : 0, t.done ? 1 : 0, t.phase || "", t.kind, t.end, t.uplinkEnd, t.evCount, t.file,
      // 静默倒计时进签名，否则没有新事件时这一格永远不重画。
      t.done ? "" : idleNoteFor(t.id),
    ].join("|"));
    const stick = box.parentElement.scrollHeight - box.parentElement.scrollTop - box.parentElement.clientHeight <= 80;
    const html = (t) => (bubble ? bubbleHTML(t) : cardHTML(t));
    if (state.convSigs.length !== sigs.length) {
      box.innerHTML = rows.map(html).join("");
    } else {
      for (let i = 0; i < sigs.length; i++) {
        if (sigs[i] === state.convSigs[i]) continue;
        const tpl = document.createElement("template");
        tpl.innerHTML = html(rows[i]);
        box.replaceChild(tpl.content.firstElementChild, box.children[i]);
      }
    }
    state.convSigs = sigs;
    if (stick) box.parentElement.scrollTop = box.parentElement.scrollHeight;
  }

  // ——— 送话源 ———

  function srcInfo() {
    const k = state.srcKey;
    if (k.startsWith("sample:")) {
      const s = state.samples.find((x) => x.url === k.slice(7));
      return { name: (s && s.name) || "夹具", meta: "夹具 GET /samples · 入库时按设备音频规格转码" };
    }
    if (k.startsWith("set:")) {
      const s = state.audioSets.find((x) => x.id === k.slice(4));
      if (!s) return { name: "（音频集已删除）", meta: "" };
      // 进度与上次汇总都从状态算：renderSrc 每次渲染都会重写这一行。
      const r = state.setRun;
      if (r && r.id === s.id) {
        return { name: s.name, meta: `正在送第 ${r.i + 1}/${r.ids.length} 条 · ${r.deviceId}${r.stop ? " · 这一条结束后停" : " · 打断 = 这一条结束后停"}` };
      }
      const last = state.setLast && state.setLast.id === s.id ? " · 上次：" + state.setLast.text : "";
      return { name: s.name, meta: `音频集 · ${s.asset_ids.length} 条 · 逐条送、每条等终态${last}` };
    }
    if (k.startsWith("asset:")) {
      const a = state.lib.rows.find((x) => x.asset_id === k.slice(6));
      if (!a) return { name: "（素材已不在库里）", meta: "" };
      return {
        name: a.name,
        meta: [a.format, a.sample_rate ? a.sample_rate + " Hz" : "", fmtDurMs(a.duration_ms),
          a.tags && a.tags.length ? "标签 " + a.tags.join("、") : "", "格式不符自动 ffmpeg 转码"]
          .filter(Boolean).join(" · "),
      };
    }
    if (k === "local") {
      const f = $("wav-file").files[0];
      if (f) return { name: f.name, meta: `${fmtBytes(f.size)} · 送出时入库为 asset` };
      return { name: "未选择文件", meta: "点这里从本机挑一个 .wav，或直接拖到中栏" };
    }
    if (k === "rec") {
      const f = state.rec.file;
      if (f) return { name: f.name, meta: `麦克风录音 · ${fmtBytes(f.size)} · 送出时入库为 asset` };
      return { name: "未录制", meta: "点 ● 录制 开始" };
    }
    return { name: "—", meta: "" };
  }

  function renderSrc() {
    const sel = $("src-select");
    const html = `<option value="">选音频源…</option>` +
      state.samples.map((s) => `<option value="sample:${esc(s.url)}">夹具 · ${esc(s.name)}</option>`).join("") +
      state.audioSets.map((s) => `<option value="set:${esc(s.id)}">音频集 · ${esc(s.name)}（${s.asset_ids.length} 条）</option>`).join("") +
      audioLib().map((a) => `<option value="asset:${esc(a.asset_id)}">素材库 · ${esc(a.name)}${a.tags && a.tags.length ? " · " + esc(a.tags.join("/")) : ""}</option>`).join("") +
      (state.rec.file ? `<option value="rec">录音 · ${esc(state.rec.name)}</option>` : "") +
      `<option value="local">本机文件 · 选择 .wav</option>`;
    if (state.srcKey.startsWith("asset:") && !audioLib().some((a) => a.asset_id === state.srcKey.slice(6))) {
      state.srcKey = "";
    }
    if (state.srcKey.startsWith("set:") && !state.audioSets.some((s) => s.id === state.srcKey.slice(4))) {
      state.srcKey = "";
    }
    if (sel.dataset.sig !== html) {
      sel.innerHTML = html;
      sel.dataset.sig = html;
    }
    if (sel.value !== state.srcKey) sel.value = state.srcKey;
    const imgSel = $("img-select");
    const imgHtml = `<option value="">不带图</option>` +
      imageLib().map((a) => `<option value="${esc(a.asset_id)}">带图 · ${esc(a.name || a.asset_id)}</option>`).join("");
    if (state.imgKey && !imageLib().some((a) => a.asset_id === state.imgKey)) state.imgKey = "";
    if (imgSel.dataset.sig !== imgHtml) {
      imgSel.innerHTML = imgHtml;
      imgSel.dataset.sig = imgHtml;
    }
    if (imgSel.value !== state.imgKey) imgSel.value = state.imgKey;
    const info = srcInfo();
    setText($("src-name"), info.name);
    setText($("src-meta"), info.meta + (state.imgKey ? " · 先传图再说话" : ""));
  }

  // speakBody 送话请求体：带图送话时加 image_asset_id（phase14）。
  function speakBody(assetId) {
    return state.imgKey ? { asset_id: assetId, image_asset_id: state.imgKey } : { asset_id: assetId };
  }

  // ——— 右栏 · Turn 页签 ———

  function renderTurns() {
    const ul = $("turns");
    const rows = convTurns();
    setNum($("turns-count"), rows.length);
    if (!rows.length) {
      setHTML(ul, `<li class="side__blank">这个实例还没有 turn</li>`);
      return;
    }
    setHTML(ul, rows.map((t) => `<li class="turnrow${t.done ? "" : " is-live"}">
      <div class="turnrow__top">
        <span title="${esc(t.id)}">${esc(shortTurn(t.id))}</span>
        ${t.done ? "" : `<span class="tag tag--live">活动中</span>`}
      </div>
      <div class="turnrow__tags">
        ${t.kind ? `<span class="tag tag--acc">${esc(t.kind)}</span>` : ""}
        <span class="${tagCls(termTone(t))}">${esc(t.end || "running")}</span>
        <span class="tag tag--mute">${esc(t.uplinkEnd || "—")}</span>
      </div>
      <div class="turnrow__acts">${turnActs(t, "下行")}</div>
    </li>`).join(""));
  }

  // ——— 事件带 ———

  const ERR_TYPES = new Set(["connection_failed", "protocol_error", "device_deleted"]);
  const AUDIO_TYPES = new Set(["tts_chunk", "tts_done", "asr_result", "turn_terminal", "speak_permit", "interrupted"]);

  function isErrEvent(ev) {
    const t = String(ev.event_type || "");
    if (ERR_TYPES.has(t)) return true;
    return /error|fail|timeout|reject|denied/i.test(t + " " + (ev.turn_end_reason || ""));
  }

  function evMatches(ev, q) {
    return [ev.event_type, ev.turn_id, ev.reply_kind, ev.turn_end_reason, ev.reason]
      .filter(Boolean).join(" ").toLowerCase().includes(q);
  }

  function rowPasses(row) {
    const q = state.tapeQ;
    const scope = state.tapeScope;
    if (row.kind === "event") {
      const ev = row.ev;
      if (scope === "audio" && !AUDIO_TYPES.has(String(ev.event_type))) return false;
      if (scope === "err" && !isErrEvent(ev)) return false;
      if (q && !evMatches(ev, q)) return false;
      return true;
    }
    if (scope === "key" || scope === "err") return false;
    if (q && !(String(row.turn_id || "").toLowerCase().includes(q) || row.label.includes(q))) return false;
    return true;
  }

  function evRowHTML(cls, time, type, meta, tail, extra) {
    return `<li class="ev${cls}">
      <div class="ev__top">
        <span class="ev__t">${esc(time)}</span>
        <span class="ev__type t-${esc(type)}">${esc(type)}</span>
        <span class="grow"></span>
        ${tail || ""}
      </div>
      ${meta ? `<div class="ev__meta">${esc(meta)}</div>` : ""}
      ${extra || ""}
    </li>`;
  }

  function burstHTML(row) {
    const open = state.tapeOpen.has(row.key);
    const live = row.live ? `<span class="tag tag--live">进行中</span>` : "";
    const caret = `<button type="button" class="btn btn--ghost" data-burst="${esc(row.key)}">${open ? "收起" : "逐包"}</button>`;
    const sum = row.packets.length
      ? `${row.label} ${row.packets.length} 包 · ${fmtBytes(row.bytes)}${row.turn_id ? " · " + row.turn_id : ""}`
      : (row.live ? row.label + " 进行中" : row.label + " 0 包");
    const frames = open ? `<div class="ev__frames">
      ${row.packets.length
        ? row.packets.map((p, i) => `<div class="ev__frame"><span>#${i + 1}</span><span>${esc(fmtBytes(p.payload_len))}</span><span>${esc(fmtClock(p.ts))}</span></div>`).join("")
        : `<div class="ev__frame"><span>—</span><span>还没有写入帧日志</span></div>`}
      <div class="ev__api">GET /devices/{id}/turns/{turn_id}/frames · JSONL</div>
    </div>` : "";
    const type = row.kind === "up" ? "uplink_frames" : "downlink_frames";
    return evRowHTML("", fmtClock(row.ts), type, sum, live + caret, frames);
  }

  function eventHTML(ev) {
    const meta = [ev.turn_id, ev.reply_kind, ev.turn_end_reason, ev.uplink_end_reason, ev.reason].filter(Boolean).join(" · ");
    return evRowHTML(isErrEvent(ev) ? " is-err" : "", fmtClock(ev.ts), String(ev.event_type || ""), meta, "", "");
  }

  function rowHTML(row) {
    return row.kind === "event" ? eventHTML(row.ev) : burstHTML(row);
  }

  function downPackets(evs, turnId) {
    const frames = (state.framesByTurn[turnId] || {}).down || [];
    return evs.map((ev, i) => ({
      ts: ev.ts,
      payload_len: ev.payload_len || (frames[i] && frames[i].payload_len) || 0,
      seq: ev.event_seq,
    }));
  }

  function sumBytes(pkts) {
    return pkts.reduce((s, p) => s + (Number(p.payload_len) || 0), 0);
  }

  function groupedTape() {
    const events = state.events;
    const turnFirst = new Map();
    for (let i = 0; i < events.length; i++) {
      const tid = events[i].turn_id;
      if (tid && !turnFirst.has(tid)) turnFirst.set(tid, i);
    }
    const insertedUp = new Set();
    const rows = [];

    function pushUp(turnId, fallbackTs) {
      if (!turnId || insertedUp.has(turnId)) return;
      const pkts = (state.framesByTurn[turnId] || {}).up || [];
      const live = state.occupiedTurnId === turnId;
      if (pkts.length === 0 && !live) return;
      insertedUp.add(turnId);
      const key = "up:" + turnId;
      const bytes = sumBytes(pkts);
      rows.push({
        kind: "up",
        key,
        sig: `${key}|${pkts.length}|${bytes}|${live ? 1 : 0}|${state.tapeOpen.has(key) ? 1 : 0}`,
        label: "上传",
        turn_id: turnId,
        ts: (pkts[0] && pkts[0].ts) || fallbackTs,
        packets: pkts,
        bytes,
        live,
      });
    }

    let i = 0;
    while (i < events.length) {
      const ev = events[i];
      if (ev.turn_id && turnFirst.get(ev.turn_id) === i) {
        pushUp(ev.turn_id, ev.ts);
      }
      if (ev.event_type === "tts_chunk") {
        const pkts = [ev];
        let j = i + 1;
        while (j < events.length && events[j].event_type === "tts_chunk" && events[j].turn_id === ev.turn_id) {
          pkts.push(events[j]);
          j++;
        }
        const key = "down:" + (ev.turn_id || "") + ":" + ev.event_seq;
        const packets = downPackets(pkts, ev.turn_id);
        const bytes = sumBytes(packets);
        rows.push({
          kind: "down",
          key,
          sig: `${key}|${packets.length}|${bytes}|${state.tapeOpen.has(key) ? 1 : 0}`,
          label: "下发",
          turn_id: ev.turn_id,
          ts: ev.ts,
          packets,
          bytes,
          live: false,
        });
        i = j;
        continue;
      }
      rows.push({ kind: "event", key: "e" + ev.event_seq, sig: "e" + ev.event_seq, ev });
      i++;
    }
    if (state.occupiedTurnId) pushUp(state.occupiedTurnId, null);
    return rows;
  }

  function nearBottom(el) {
    return el.scrollHeight - el.scrollTop - el.clientHeight <= 48;
  }

  function setFollow(on) {
    state.tapeFollow = !!on;
    const btn = $("btn-follow");
    btn.setAttribute("aria-pressed", String(state.tapeFollow));
    btn.classList.toggle("btn--ok", state.tapeFollow);
    btn.classList.toggle("btn--icon", !state.tapeFollow);
    if (state.tapeFollow) {
      const ol = $("tape");
      ol.scrollTop = ol.scrollHeight;
    }
  }

  // 只重画变了的尾巴。事件按 event_seq 追加，实践中每次只动最后一两行，
  // 于是往回翻历史时上面的 DOM 不会被拆掉。
  function renderTape() {
    const ol = $("tape");
    const rows = groupedTape().filter(rowPasses);
    setNum($("tape-count"), rows.length);
    setNum($("fab-count"), state.events.length);

    if (!rows.length) {
      ol.classList.remove("tape--live");
      state.tapeRows = [];
      const none = state.events.length
        ? "当前范围 / 过滤下没有事件。"
        : "还没有事件。启动设备后 WS /ws/events 会从 oldest 回放。";
      const html = `<li class="ev__blank">${esc(none)}</li>`;
      if (ol.dataset.none !== none) {
        ol.dataset.none = none;
        ol.innerHTML = html;
      }
      return;
    }
    delete ol.dataset.none;
    if (!ol.classList.contains("tape--live")) {
      ol.classList.add("tape--live");
      // 从空态切过来时 DOM 里是占位行，得先清干净。
      ol.innerHTML = "";
      state.tapeRows = [];
    }

    const stick = state.tapeFollow;
    const prev = state.tapeRows;
    let i = 0;
    while (i < rows.length && i < prev.length && rows[i].sig === prev[i]) i++;
    while (ol.children.length > i) ol.removeChild(ol.lastElementChild);
    if (i < rows.length) {
      const tpl = document.createElement("template");
      tpl.innerHTML = rows.slice(i).map(rowHTML).join("");
      ol.appendChild(tpl.content);
    }
    state.tapeRows = rows.map((r) => r.sig);
    if (stick) ol.scrollTop = ol.scrollHeight;
  }

  function visibleEventsJSONL() {
    const q = state.tapeQ;
    const scope = state.tapeScope;
    return state.events.filter((ev) => {
      if (scope === "key" && ev.event_type === "tts_chunk") return false;
      if (scope === "audio" && !AUDIO_TYPES.has(String(ev.event_type))) return false;
      if (scope === "err" && !isErrEvent(ev)) return false;
      if (q && !evMatches(ev, q)) return false;
      return true;
    }).map((ev) => JSON.stringify(ev)).join("\n");
  }

  async function copyText(text) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch { /* 落到 execCommand */ }
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand("copy"); } catch { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }

  async function copyTape() {
    const text = visibleEventsJSONL();
    if (!text) {
      flash("没有可复制的事件", "err");
      return;
    }
    const n = text.split("\n").length;
    const ok = await copyText(text);
    flash(ok ? `已复制 ${n} 条筛选结果为 JSONL` : "复制失败，请手动选中", ok ? "ok" : "err");
  }

  async function loadFrames(turnId) {
    if (!turnId || !state.selectedId || !state.instanceId) return false;
    const url = `/devices/${encodeURIComponent(state.selectedId)}/turns/${encodeURIComponent(turnId)}/frames?instance_id=${encodeURIComponent(state.instanceId)}`;
    try {
      const res = await fetch(url);
      if (res.status === 404) {
        const prev = state.framesByTurn[turnId];
        if (!prev) state.framesByTurn[turnId] = { up: [], down: [] };
        return !prev;
      }
      if (!res.ok) return false;
      const text = await res.text();
      const up = [];
      const down = [];
      for (const line of text.split("\n")) {
        const s = line.trim();
        if (!s) continue;
        let row;
        try { row = JSON.parse(s); } catch { continue; }
        if (Number(row.stage) !== 1) continue;
        const pkt = { ts: row.ts, payload_len: row.payload_len || 0, seq: row.seq };
        if (row.direction === "outbound") up.push(pkt);
        else if (row.direction === "inbound") down.push(pkt);
      }
      const prev = state.framesByTurn[turnId];
      const same = prev && prev.up.length === up.length && prev.down.length === down.length;
      state.framesByTurn[turnId] = { up, down };
      return !same;
    } catch {
      return false;
    }
  }

  function queueFrames(turnId) {
    if (!turnId) return;
    state.needFrames.add(turnId);
    clearTimeout(state.framesDebounce);
    state.framesDebounce = setTimeout(async () => {
      const ids = [...state.needFrames];
      state.needFrames.clear();
      await Promise.all(ids.map(loadFrames));
      renderTape();
      renderConv();
    }, 120);
  }

  function stopFramePoll() {
    if (state.framePoll) {
      clearInterval(state.framePoll);
      state.framePoll = 0;
    }
  }

  function startFramePoll(turnId) {
    stopFramePoll();
    if (!turnId) return;
    paint("tape", "conv");
    const tick = async () => {
      if (state.occupiedTurnId !== turnId) {
        stopFramePoll();
        return;
      }
      const changed = await loadFrames(turnId);
      if (changed) paint("tape", "conv");
    };
    tick();
    state.framePoll = setInterval(tick, 400);
  }

  function resetTapeAux() {
    stopFramePoll();
    state.myTurns = new Set();
    state.tapeOpen = new Set();
    state.framesByTurn = {};
    state.needFrames = new Set();
    state.tapeRows = [];
    state.convSigs = [];
    clearTimeout(state.framesDebounce);
  }

  function applyLiveFromEvent(ev) {
    if (!ev || state.tombstone || !state.live) return;
    const typ = ev.event_type;
    if (typ === "ready") {
      state.live.connection_state = "ready";
      state.live.instance_state = "running";
      return;
    }
    if (typ === "connected" || typ === "registering" || typ === "registered" || typ === "reporting") {
      state.live.connection_state = typ;
      return;
    }
    if (typ === "connection_stopped" || typ === "connection_failed") {
      state.live.connection_state = "disconnected";
      state.live.instance_state = "stopped";
      state.occupiedTurnId = null;
      stopFramePoll();
      return;
    }
    // backlog 计数只在 GET /devices/{id} 里，事件流不带；
    // 排队相关事件到了就按语义就地修正，等下一次拉取纠偏。
    if (typ === "speak_queued") {
      state.live.speak_backlog_len = (Number(state.live.speak_backlog_len) || 0) + 1;
      return;
    }
    if (typ === "speak_dequeued" || typ === "speak_backlog_dropped") {
      state.live.speak_backlog_len = Math.max(0, (Number(state.live.speak_backlog_len) || 0) - 1);
    }
  }

  // 事件按 event_seq 递增到，绝大多数直接接在尾巴上；
  // 迟到的才二分插进去，省掉每条都全量 sort。
  function insertEvent(ev) {
    const arr = state.events;
    const seq = ev.event_seq;
    if (!arr.length || seq > arr[arr.length - 1].event_seq) {
      arr.push(ev);
      return;
    }
    let lo = 0;
    let hi = arr.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (arr[mid].event_seq < seq) lo = mid + 1;
      else hi = mid;
    }
    arr.splice(lo, 0, ev);
  }

  function ingestEvent(ev) {
    if (!ev || ev.event_seq == null) return;
    if (state.instanceId && ev.instance_id && ev.instance_id !== state.instanceId) return;
    if (state.seenSeq.has(ev.event_seq)) return;
    state.seenSeq.add(ev.event_seq);
    insertEvent(ev);
    if (ev.event_seq > state.newestSeq) state.newestSeq = ev.event_seq;
    applyLiveFromEvent(ev);
    if (ev.turn_id) queueFrames(ev.turn_id);
    if (ev.event_type === "turn_terminal") {
      if (state.occupiedTurnId && ev.turn_id === state.occupiedTurnId) {
        state.occupiedTurnId = null;
        stopFramePoll();
      }
      refreshTurnsQuiet();
      maybePlayDownlink(ev);
      const extra = [ev.reply_kind, ev.turn_end_reason].filter(Boolean).join(" / ");
      // 跑音频集时每轮都弹会挤掉最后的汇总；进度在送话条上。
      if (!state.setRun) flash("本轮结束" + (extra ? " · " + extra : "") + " · 可再送出", "ok");
    }
    if (ev.event_type === "speak_dequeued" && ev.turn_id) {
      // backlog 出队即上槽：跟上新 turn，帧轮询与横幅都指向它。
      state.occupiedTurnId = ev.turn_id;
      startFramePoll(ev.turn_id);
      flash("排队请求出队 · " + ev.turn_id, "ok");
    }
    if (ev.event_type === "connection_failed") {
      flash("连接失败" + (ev.reason ? " · " + ev.reason : "") + "。重新启动后可再送出", "err");
    }
    if (ev.event_type === "connection_stopped") {
      flash("已停止。重新启动后可再送出", "ok");
    }
    if (ev.event_type === "device_deleted") {
      state.tombstone = true;
      state.live = null;
      writeHash();
      setWsNote("墓碑回放结束，WS 已关", "done");
    }
    paint("tape", "stage", "conv", "turns");
  }

  async function refreshTurnsQuiet() {
    if (!state.selectedId || !state.instanceId) return;
    try {
      const data = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/turns?instance_id=${encodeURIComponent(state.instanceId)}`);
      state.turns = data.turns || [];
      paint("turns", "conv");
    } catch {
      /* tombstone 过期时保持现有列表 */
    }
  }

  function maybePlayDownlink(ev) {
    const kind = ev.reply_kind || "";
    if (!kind.includes("tts") || !ev.turn_id || !state.instanceId || !state.selectedId) return;
    // 只自动播本页面会话发起的 turn：切换设备后 WS 从 oldest 回放的
    // 历史 turn_terminal（含回放期间的 speak_dequeued）一律不自动播。
    if (!state.myTurns.has(ev.turn_id)) return;
    state.myTurns.delete(ev.turn_id);
    playHref(downlinkHref(ev.turn_id), { label: "下行 · " + shortTurn(ev.turn_id), download: ev.turn_id + "-downlink.wav" });
  }

  // resetPlayer 停止并隐藏播放器，切换设备/实例时旧音频不残留不续播。
  function resetPlayer() {
    const audio = $("downlink-audio");
    try { audio.pause(); } catch { /* ignore */ }
    audio.removeAttribute("src");
    $("player-box").hidden = true;
  }

  function playHref(href, opts) {
    const o = opts || {};
    const audio = $("downlink-audio");
    $("player-box").hidden = false;
    setText($("player-label"), o.label || "播放");
    const dl = $("player-dl");
    dl.hidden = false;
    // 试听可能是服务端解码出来的 wav，下载按钮仍给原始文件（dlHref）。
    dl.href = o.dlHref || href;
    dl.setAttribute("download", o.download || "audio.wav");
    audio.src = href;
    const p = audio.play();
    if (p && p.catch) p.catch(() => {});
  }

  function closeWS(reconnect) {
    if (state.ws) {
      state.ws.onclose = null;
      state.ws.onerror = null;
      state.ws.onmessage = null;
      try { state.ws.close(); } catch { /* ignore */ }
      state.ws = null;
    }
    if (!reconnect) state.wsKey = "";
  }

  function connectWS(opts) {
    if (!state.selectedId || !state.instanceId) return;
    if (state.tombstone && state.events.some((e) => e.event_type === "device_deleted")) return;
    const fromOldest = !opts || opts.fromOldest;
    const key = state.selectedId + "\0" + state.instanceId;
    if (state.ws && state.wsKey === key && state.ws.readyState <= 1) return;
    closeWS(false);
    const q = new URLSearchParams({
      device_id: state.selectedId,
      instance_id: state.instanceId,
    });
    // 省略 after_event_seq ≡ 从 oldest 回放。只要未来才传 newest。
    if (!fromOldest && state.newestSeq > 0) q.set("after_event_seq", String(state.newestSeq));
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/ws/events?${q.toString()}`);
    state.ws = ws;
    state.wsKey = key;
    setWsNote(fromOldest ? "事件从 oldest 回放" : "只收新事件", fromOldest ? "replay" : "live");
    ws.onmessage = (e) => {
      let ev;
      try { ev = JSON.parse(e.data); } catch { return; }
      ingestEvent(ev);
    };
    ws.onclose = () => {
      if (state.tombstone) {
        setWsNote("墓碑回放结束，WS 已关", "done");
        return;
      }
      if (state.ws === ws) state.ws = null;
    };
    ws.onerror = () => {
      setWsNote("事件 WS 失败（缺 ID 为 400，过期游标 410）", "err");
    };
  }

  async function selectDevice(id, insHint) {
    disarmDelete();
    flash("");
    try {
      const live = await api("GET", `/devices/${encodeURIComponent(id)}`);
      if (insHint && insHint !== live.instance_id) {
        await openTombstone(id, insHint);
        return;
      }
      const instanceChanged = state.tombstone || state.selectedId !== id || state.instanceId !== live.instance_id;
      state.selectedId = id;
      state.tombstone = false;
      state.tombSource = "tomb";
      state.live = live;
      state.instanceId = live.instance_id;
      state.connGeneration = live.conn_generation;
      if (instanceChanged) {
        state.occupiedTurnId = null;
        resetTapeAux();
      }
      await loadConfigAndTurns(instanceChanged);
      writeHash();
      renderRoster();
      renderStage();
      renderConv();
    } catch (err) {
      if (err.status === 404) {
        await openTombstone(id, insHint || state.instanceId);
        return;
      }
      apiErr(err);
    }
  }

  async function openTombstone(id, ins, src) {
    const instanceChanged = state.selectedId !== id || state.instanceId !== ins;
    state.selectedId = id;
    state.tombstone = true;
    state.tombSource = src === "disk" ? "disk" : "tomb";
    state.live = null;
    state.instanceId = ins || state.instanceId;
    state.occupiedTurnId = null;
    resetTapeAux();
    closeWS(false);
    await loadConfigAndTurns(instanceChanged || state.events.length === 0);
    writeHash();
    renderRoster();
    renderStage();
    renderConv();
    flash(state.tombSource === "disk"
      ? "在看盘上的旧运行（只读）。要送话请切回本次运行。"
      : "live 已摘除。TTL 内用同一 instance_id 看历史，不要换到新实例。", "info");
  }

  async function loadConfigAndTurns(resetTape) {
    if (resetTape) {
      state.events = [];
      state.seenSeq = new Set();
      state.newestSeq = 0;
      resetTapeAux();
      resetPlayer();
      closeWS(false);
      setFollow(true);
      renderTape();
    }
    if (state.selectedId && !state.tombstone) {
      try {
        state.config = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/config`);
      } catch (err) {
        state.config = null;
        apiErr(err);
      }
    }
    if (state.selectedId && state.instanceId) {
      try {
        const data = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/turns?instance_id=${encodeURIComponent(state.instanceId)}`);
        state.turns = data.turns || [];
        if (state.tombstone) state.tombSource = data.source === "disk" ? "disk" : "tomb";
        state.turns.forEach((t) => { if (t.turn_id) queueFrames(t.turn_id); });
      } catch {
        state.turns = [];
      }
      if (state.tombSource === "disk" && state.tombstone) await loadDiskEvents();
      else connectWS({ fromOldest: resetTape || state.events.length === 0 });
    }
    renderTurns();
  }

  // 盘上的旧运行没有 live WS（/ws/events 等的是将来的事件，对它 404 才是对的），
  // 事件一次性从 GET /events 补齐，仍走 ingestEvent 这条通道，渲染逻辑没有第二套。
  async function loadDiskEvents() {
    try {
      const data = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/events?instance_id=${encodeURIComponent(state.instanceId)}`);
      (data.events || []).forEach(ingestEvent);
      setWsNote("盘上历史 · 事件已读完，无实时流", "done");
    } catch (err) {
      setWsNote("盘上历史的事件读取失败", "err");
      apiErr(err);
    }
    renderTape();
  }

  // 挂靠条（phase11/12）：设备册条目不带三级，启动时才决定挂靠和产品。
  // 复用配置树页那套 state.regSel / changeTree，选中项跨页面共用一份。
  function attachBarHTML() {
    const sel = state.regSel;
    const opt = (rows, cur, ph, dis) =>
      `<select data-tree="${dis.k}"${dis.off ? " disabled" : ""}>${selOpts(rows, cur, ph)}</select>`;
    const live = state.live || {};
    const warn = live.product && state.attachProduct && live.product !== state.attachProduct && live.overridden
      ? `<span class="attach__warn">启动后临时覆盖会清空</span>` : "";
    const mustPick = sel.typ && !typeDefaultProduct();
    return `<span class="attach__label">挂靠</span>` +
      opt(state.registry.map((r) => ({ v: r.name, t: r.name })), sel.env, "环境", { k: "env", off: false }) +
      opt(regEnts().map((r) => ({ v: r.short_name, t: r.short_name })), sel.ent, "厂商", { k: "ent", off: !sel.env }) +
      opt(regTypes().map((r) => ({ v: r.short_name, t: r.short_name })), sel.typ, "类型", { k: "typ", off: !sel.ent }) +
      `<span class="attach__label">产品</span>` +
      `<select data-attach="product">${productOpts(state.attachProduct, mustPick ? "必须手选产品" : "选产品")}</select>` +
      warn;
  }

  // 不少服务端不发显式的「TTS 结束」标记，设备只能等「下行静默满
  // downlink_idle_timeout_sec」再收尾。那段等待里界面只说「等 turn_terminal」，
  // 看着像卡死——把已经静默了多久摆出来。
  function downIdleNote() {
    return idleNoteFor(state.occupiedTurnId);
  }

  function idleNoteFor(turnId) {
    let lastDown = 0;
    for (let i = state.events.length - 1; i >= 0; i--) {
      const ev = state.events[i];
      if (ev.turn_id !== turnId) continue;
      if (ev.event_type === "tts_chunk" || ev.event_type === "tts_done") {
        lastDown = Date.parse(ev.ts);
        break;
      }
    }
    if (!lastDown) return "";
    const idle = Math.max(0, Math.round((Date.now() - lastDown) / 1000));
    const budget = Number(((state.config || {}).behavior || {}).downlink_idle_timeout_sec) || 0;
    return budget
      ? ` · 下行已静默 ${idle}s，满 ${budget}s 收尾`
      : ` · 下行已静默 ${idle}s`;
  }

  // ——— 生命周期 ———

  async function startAndWait() {
    if (!state.selectedId) return;
    const refs = attachRefs();
    if (!refs.environment || !refs.enterprise || !refs.device_type) {
      flash("先在「挂靠」里把 环境 / 厂商 / 设备类型 选全", "err");
      return;
    }
    if (!state.attachProduct) {
      flash("类型没配默认产品，请在启动条手选一个产品", "err");
      return;
    }
    const live = state.live || {};
    if (live.product && live.product !== state.attachProduct && live.overridden) {
      flash("启动后临时覆盖会清空", "info");
    }
    state.busy = true;
    renderStage();
    const startBtn = $("btn-start");
    setText(startBtn, "启动中…");
    flash("正在启动…", "info");
    try {
      const started = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/start`, {
        ...refs, product: state.attachProduct,
      });
      state.instanceId = started.instance_id;
      state.connGeneration = started.conn_generation;
      if (state.live) {
        state.live.instance_id = started.instance_id;
        state.live.conn_generation = started.conn_generation;
        state.live.instance_state = "starting";
      }
      connectWS({ fromOldest: state.events.length === 0 });
      renderStage();
      // wait_ready 最长 30s，按钮上给秒数免得盲等。
      const t0 = Date.now();
      const tick = setInterval(() => {
        setText(startBtn, `等待 Ready… ${Math.round((Date.now() - t0) / 1000)}s`);
      }, 500);
      let ready;
      try {
        ready = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/wait_ready`, {
          instance_id: started.instance_id,
          conn_generation: started.conn_generation,
        });
      } finally {
        clearInterval(tick);
      }
      flash("wait_ready 返回 · " + state.selectedId + " 已 " + (ready.connection_state || "ready"), "ok");
      await refreshList();
      try {
        state.live = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}`);
        state.instanceId = state.live.instance_id;
        state.connGeneration = state.live.conn_generation;
      } catch (err) {
        apiErr(err);
      }
      await loadConfigAndTurns(false);
    } catch (err) {
      apiErr(err);
      await refreshList();
    } finally {
      setText($("btn-start"), "启动并等待 Ready");
      state.busy = false;
      renderStage();
      renderConv();
    }
  }

  async function stopDevice() {
    if (!state.selectedId) return;
    state.busy = true;
    renderStage();
    try {
      await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/stop`);
      state.occupiedTurnId = null;
      stopFramePoll();
      flash("已停止 " + state.selectedId + "（不是故障）", "info");
      await refreshList();
      try {
        state.live = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}`);
      } catch (err) {
        if (err.status === 404) {
          await openTombstone(state.selectedId, state.instanceId);
          return;
        }
        throw err;
      }
      await loadConfigAndTurns(false);
    } catch (err) {
      apiErr(err);
    } finally {
      state.busy = false;
      renderStage();
    }
  }

  function disarmDelete() {
    const btn = $("btn-delete");
    if (!btn) return;
    btn.dataset.armed = "0";
    setText(btn, "删除");
  }

  async function deleteDevice() {
    if (!state.selectedId) return;
    const btn = $("btn-delete");
    if (btn.dataset.armed !== "1") {
      btn.dataset.armed = "1";
      setText(btn, "再点一次确认");
      setTimeout(() => { if (btn.dataset.armed === "1") disarmDelete(); }, 4000);
      return;
    }
    disarmDelete();
    const ins = state.instanceId;
    try {
      await api("DELETE", `/devices/${encodeURIComponent(state.selectedId)}`);
      state.tombstone = true;
      state.instanceId = ins;
      writeHash();
      flash("已删除，进入墓碑态；地址栏已带上 ?ins=" + shortId(ins), "info");
      await refreshList();
      renderStage();
    } catch (err) {
      apiErr(err);
    }
  }

  async function batchAct(kind) {
    const ids = [...state.checked];
    if (!ids.length) return;
    const label = { start: "启动", stop: "停止", delete: "删除" }[kind];
    try {
      const body = { device_ids: ids };
      if (kind === "start") {
        const refs = attachRefs();
        if (!refs.environment || !refs.enterprise || !refs.device_type) {
          flash("先在「挂靠」里把 环境 / 厂商 / 设备类型 选全", "err");
          return;
        }
        if (!state.attachProduct) {
          flash("类型没配默认产品，请在启动条手选一个产品", "err");
          return;
        }
        Object.assign(body, refs, { product: state.attachProduct });
      }
      const res = await api("POST", `/devices/batch/${kind}`, body);
      const ok = (res.succeeded || []).length;
      const bad = (res.failed || []).length;
      state.checked.clear();
      await refreshList();
      // 先重选再报结果：selectDevice 会清提示条，顺序反了结果就被自己冲掉。
      if (state.selectedId && ids.includes(state.selectedId)) {
        await selectDevice(state.selectedId);
      }
      flash(`POST /devices/batch/${kind} · ${ok} 台${label}成功${bad ? `，${bad} 台失败：${res.failed.map((f) => f.device_id + " " + f.error).join("；")}` : ""}`,
        bad ? "err" : "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  // ——— 送话 ———

  async function speakAsset(assetId, name) {
    const wait = state.syncWait;
    const path = `/devices/${encodeURIComponent(state.selectedId)}/${wait ? "speak_and_wait" : "speak"}`;
    if (wait) flash("已送出，等 turn_terminal…", "info");
    const spoken = await api("POST", path, speakBody(assetId));
    if (spoken.turn_id) {
      // 登记为本会话发起的 turn：终态时才允许自动播放下行。
      state.myTurns.add(spoken.turn_id);
      state.turnMeta[spoken.turn_id] = { name: name || "", assetId };
    }
    if (spoken.queued) {
      // Phase 4 backlog：槽占用时进队列，终态后自动出队。
      flash(`已排队 ${spoken.turn_id} · 位置 ${spoken.queue_position} · 终态后自动出队`, "ok");
    } else if (wait) {
      state.occupiedTurnId = null;
      stopFramePoll();
      flash(`speak_and_wait 返回 · reply_kind ${spoken.reply_kind || "—"} · turn_end_reason ${spoken.turn_end_reason || "—"}`, "ok");
      await refreshTurnsQuiet();
    } else {
      state.occupiedTurnId = spoken.turn_id;
      setFollow(true);
      startFramePoll(spoken.turn_id);
      flash("已受理 " + spoken.turn_id + " · 等 turn_terminal", "ok");
    }
    paint("conv", "turns", "stage");
  }

  // 上传一个本地/夹具文件入库再送出。带 device_id：入库时即按设备音频规格转码。
  async function uploadAndSpeak(file) {
    const fd = new FormData();
    fd.append("file", file, file.name);
    if (state.selectedId) fd.append("device_id", state.selectedId);
    const asset = await api("POST", "/assets", fd);
    await speakAsset(asset.asset_id, file.name);
    loadLibrary().catch(() => {});
  }

  // 音频集：逐条 speak_and_wait。开跑时钉住设备与实例、拍一份条目快照——跑到一半
  // 改集不影响这一组；切了设备、点了打断、遇到第一个错误就停（人在旁边看着，不必分类）。
  async function runSet(id) {
    const set = state.audioSets.find((x) => x.id === id);
    if (!set || !set.asset_ids.length) {
      flash("这个音频集是空的，先去「音频集」视图加条目", "err");
      return;
    }
    const run = state.setRun = {
      id, name: set.name, deviceId: state.selectedId, instanceId: state.instanceId,
      ids: set.asset_ids.slice(), i: 0, stop: false,
    };
    const ends = {};
    let done = 0;
    let why = "";
    try {
      for (; run.i < run.ids.length; run.i++) {
        if (run.stop) { why = "已打断"; break; }
        if (state.selectedId !== run.deviceId || state.instanceId !== run.instanceId) { why = "换了设备"; break; }
        const blocked = blockedReason();
        if (blocked) { why = blocked; break; }
        renderSrc();
        const assetId = run.ids[run.i];
        const a = state.lib.rows.find((x) => x.asset_id === assetId);
        const res = await api("POST", `/devices/${encodeURIComponent(run.deviceId)}/speak_and_wait`, speakBody(assetId));
        if (res.turn_id) {
          state.myTurns.add(res.turn_id);
          state.turnMeta[res.turn_id] = { name: `[${run.name} ${run.i + 1}/${run.ids.length}] ${a ? a.name : assetId}`, assetId };
        }
        const end = res.turn_end_reason || "—";
        ends[end] = (ends[end] || 0) + 1;
        done++;
        await refreshTurnsQuiet();
        paint("conv", "turns", "stage");
      }
    } catch (err) {
      why = (err.status ? err.status + " " : "") + err.message;
    } finally {
      const text = `${done}/${run.ids.length} 条` +
        Object.entries(ends).map(([k, n]) => ` · ${k} ${n}`).join("") + (why ? ` · 停：${why}` : "");
      state.setRun = null;
      state.setLast = { id, text };
      flash(`音频集 ${run.name} · ${text}`, why ? "err" : "ok");
    }
  }

  async function speak(ev) {
    if (ev) ev.preventDefault();
    if (state.setRun) return; // 一次只跑一组
    const blocked = blockedReason();
    if (blocked) {
      flash(blocked, "err");
      return;
    }
    const k = state.srcKey;
    if (!k) {
      flash("先在左边的下拉里选一个音频源", "err");
      return;
    }
    state.busy = true;
    renderStage();
    try {
      if (k.startsWith("set:")) {
        await runSet(k.slice(4));
      } else if (k.startsWith("asset:")) {
        const id = k.slice(6);
        const a = state.lib.rows.find((x) => x.asset_id === id);
        await speakAsset(id, a ? a.name : "");
      } else if (k.startsWith("sample:")) {
        const url = k.slice(7);
        const s = state.samples.find((x) => x.url === url);
        const name = (s && s.name) || "sample.wav";
        const res = await fetch(url);
        if (!res.ok) throw new Error("读不到夹具 " + name);
        const blob = await res.blob();
        await uploadAndSpeak(new File([blob], name, { type: "audio/wav" }));
      } else if (k === "rec") {
        if (!state.rec.file) {
          flash("还没有录音，点 ● 录制", "err");
          return;
        }
        await uploadAndSpeak(state.rec.file);
      } else {
        const f = $("wav-file").files[0];
        if (!f) {
          flash("先选一个本机 .wav 文件", "err");
          return;
        }
        await uploadAndSpeak(f);
      }
    } catch (err) {
      const extra = err.data && err.data.instance_state
        ? ` (${err.data.instance_state}/${err.data.connection_state})`
        : "";
      flash((err.status ? err.status + " " : "") + err.message + extra, "err");
    } finally {
      state.busy = false;
      renderStage();
      renderConv();
    }
  }

  async function interrupt() {
    if (state.setRun) {
      state.setRun.stop = true; // 当前这一条被打断后，不再往下送
      renderSrc();
    }
    if (!state.instanceId) return;
    try {
      const res = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/interrupt`, {
        instance_id: state.instanceId,
      });
      if (res.interrupted) {
        flash("已打断 " + res.turn_id + " · 连接保持，槽位等 turn_terminal", "ok");
      } else {
        flash("没有活动 Turn", "info");
      }
    } catch (err) {
      apiErr(err);
    }
  }

  async function loadSamples() {
    try {
      const data = await api("GET", "/samples");
      state.samples = data.samples || [];
    } catch {
      state.samples = [];
    }
    renderSrc();
  }

  // ——— 素材库 ———
  // 音频试听走 ?decode=1；图片用 /assets/{id}/content 原字节做缩略图。

  let libReqSeq = 0;

  async function loadLibrary() {
    const seq = ++libReqSeq;
    let rows = [];
    try {
      const data = await api("GET", "/assets");
      rows = data.assets || [];
    } catch {
      rows = [];
    }
    if (seq !== libReqSeq) return;
    state.lib.rows = rows;
    setNum($("lib-count"), rows.length);
    renderSrc();
    if (state.drawer === "assets" || state.drawer === "config") renderDrawer();
  }

  async function importAsset(file, name, lang) {
    const fd = new FormData();
    fd.append("file", file, file.name);
    if (name) fd.append("name", name);
    if (lang) fd.append("language", lang);
    const a = await api("POST", "/assets", fd);
    const extra = a.kind === "image" ? a.format : `${a.format} · ${fmtDurMs(a.duration_ms)}`;
    flash(`已导入 ${a.name}（${extra}）`, "ok");
    await loadLibrary();
  }

  // ——— 设备管理视图 ———
  // 这里编辑的是落盘的「定义」；调试台的配置抽屉改的是本次运行的当前值。

  function setView(v) {
    state.view = ["manage", "registry", "products", "sets"].includes(v) ? v : "bench";
    closeDrawer();
    state.adding = null;
    state.regEdit = "";
    $("manage").hidden = state.view !== "manage";
    $("registry").hidden = state.view !== "registry";
    $("products").hidden = state.view !== "products";
    $("sets").hidden = state.view !== "sets";
    document.querySelector("main.bench").hidden = state.view !== "bench";
    $("segwrap-mode").hidden = state.view !== "bench";
    for (const view of ["bench", "manage", "registry", "products", "sets"]) {
      const on = state.view === view;
      $("btn-view-" + view).classList.toggle("is-on", on);
      $("btn-view-" + view).setAttribute("aria-pressed", String(on));
    }
    if (state.view === "manage") renderManage();
    if (state.view === "registry") renderRegistry();
    if (state.view === "products") renderProducts();
    if (state.view === "sets") {
      renderSets();
      loadAudioSets(); // agent 可能刚经 REST 改过
    }
  }

  function renderRegistry() {
    setHTML($("registry-body"), registryHTML());
  }

  function pickTree(tree, value) {
    if (tree === "env") state.regSel = { env: value, ent: "", typ: "" };
    else if (tree === "ent") { state.regSel.ent = value; state.regSel.typ = ""; }
    else state.regSel.typ = value;
    state.adding = null;
    state.regEdit = "";
    state.attachProduct = typeDefaultProduct();
  }

  function changeTree(t) {
    const tree = t.getAttribute("data-tree");
    if (!tree) return false;
    pickTree(tree, t.value);
    return true;
  }

  function bindRegistry() {
    $("btn-view-registry").addEventListener("click", () => setView("registry"));
    $("btn-view-products").addEventListener("click", () => setView("products"));
    $("registry-devices").addEventListener("click", () => setView("manage"));
    $("registry-body").addEventListener("click", (e) => {
      const button = e.target.closest("button");
      if (!button) return;
      const d = button.dataset;
      if (d.treePick) { pickTree(d.treePick, d.v); renderRegistry(); }
      else if (d.treeAdd) { state.adding = state.adding === d.treeAdd ? null : d.treeAdd; state.regEdit = ""; renderRegistry(); }
      else if (d.treeCancel) { state.adding = null; state.regEdit = ""; renderRegistry(); }
      else if (d.treeOk) treeAdd(d.treeOk);
      else if (d.treeRename) { state.regEdit = state.regEdit === d.treeRename ? "" : d.treeRename; state.adding = null; renderRegistry(); }
      else if (d.treeSave) treeSave(d.treeSave);
      else if (d.treeDel) armThen(button, () => treeDelete(d.treeDel));
    });
  }

  const PLAYING_MODE_OPTS = [{ v: "1", t: "1 按键" }, { v: "2", t: "2 连续" }, { v: "3", t: "3 唤醒" }];
  const AUDIO_SPEC_OPTS = ["pcm/16000", "wav/16000", "mp3/16000", "amr/16000", "amr/8000", "aac/16000"];
  const ID_RISK = "换产品或改 ICCID 会让真实服务端重新校验这台设备，校验不过会把它标成不可用";

  function blankProduct() {
    return {
      id: "", name: "",
      playing_modes: [1, 2, 3],
      audio_formats: ["pcm/16000", "wav/16000", "mp3/16000", "amr/16000", "aac/16000"],
      defaults: {
        action: "chatbot", firmware_version: "1.0.0", nic_type: "wifi", nic_iccid: "8986xxxxxxxxxx",
        playing_mode: 1,
        audio: { format: "pcm", sample_rate: 16000, channels: 1, sample_format: "s16le", slice_ms: 100, max_payload_size: 51200, bitrate_kbps: 0 },
        uuid: { min: 1, max: 2147483647 },
        behavior: {
          keepalive_interval_sec: 60, first_reply_timeout_sec: 20, speak_backlog_depth: 0,
          silence_probe: false, interrupt_on_disconnect: false,
        },
        recording: { enable_frame_log: true, save_uplink_audio: true, save_downlink_audio: true, output_dir: "" },
        features: { photo: { enabled: false, image: "", server_default_reply: false, slice_interval_ms: 0, reply_timeout_sec: 0 } },
      },
    };
  }

  function renderProducts() {
    const form = state.productForm;
    const rows = state.products;
    setNum($("products-count"), rows.length);
    const table = rows.length ? `<table class="grid">
      <thead><tr><th>id</th><th>名称</th><th>对话模式</th><th>音频格式</th><th>默认格式</th><th>拍照</th><th class="grid__ops">操作</th></tr></thead>
      <tbody>${rows.map((p) => {
        const d = p.defaults || {};
        const a = d.audio || {};
        const photo = ((d.features || {}).photo || {}).enabled;
        return `<tr data-id="${esc(p.id)}">
          <td class="grid__id">${esc(p.id)}</td>
          <td>${esc(p.name || "")}</td>
          <td class="mono">${esc((p.playing_modes || []).join(" / ") || "—")}</td>
          <td class="mono">${esc((p.audio_formats || []).join(", ") || "—")}</td>
          <td class="mono">${esc(a.format ? `${a.format}/${a.sample_rate}` : "—")}</td>
          <td>${photo ? `<span class="${tagCls("ok")}">开</span>` : `<span class="grid__dim">关</span>`}</td>
          <td class="grid__ops">
            <button type="button" class="btn btn--sub btn--tiny" data-pact="edit">编辑</button>
            <button type="button" class="btn btn--danger btn--tiny" data-pact="del" data-armed="0">删除</button>
          </td>
        </tr>`;
      }).join("")}</tbody></table>` : `<div class="blank"><p class="blank__title">还没有产品</p><p class="blank__body">先建一个产品，启动设备时再选它。</p></div>`;
    setHTML($("products-body"), table + (form ? productFormHTML(form) : ""));
  }

  function productFormHTML(form) {
    const isNew = form.mode === "new";
    const p = isNew ? blankProduct() : (state.products.find((x) => x.id === form.id) || blankProduct());
    const d = p.defaults || {};
    const a = d.audio || {};
    const beh = d.behavior || {};
    const rec = d.recording || {};
    const photo = ((d.features || {}).photo) || {};
    const modes = new Set((p.playing_modes || []).map(Number));
    const specs = new Set(p.audio_formats || []);
    const extraSpecs = [...specs].filter((s) => !AUDIO_SPEC_OPTS.includes(s));
    const specList = AUDIO_SPEC_OPTS.concat(extraSpecs);
    const curSpec = a.format ? `${a.format}/${a.sample_rate}` : "pcm/16000";
    const imgOpts = imageLib().map((x) => ({ v: x.asset_id, t: x.name || x.asset_id }));
    const groups = [
      { title: "清单", fields: [
        { name: "id", zh: "产品 id", v: p.id, ph: "mh8w", ro: !isNew, note: isNew ? "建后不可改" : "不可改" },
        { name: "name", zh: "名称", v: p.name, ph: "默认产品" },
        { name: "playing_modes", zh: "对话模式清单", checks: PLAYING_MODE_OPTS.map((o) => ({ ...o, on: modes.has(Number(o.v)) })) },
        { name: "audio_formats", zh: "音频格式清单", checks: specList.map((s) => ({ v: s, t: s, on: specs.has(s) })) },
        { name: "playing_mode", zh: "默认对话模式", v: String(d.playing_mode || 1), select: PLAYING_MODE_OPTS.filter((o) => modes.has(Number(o.v))) },
        { name: "audio_spec", zh: "默认音频格式", v: curSpec, select: specList.filter((s) => specs.has(s)).map((s) => ({ v: s, t: s })) },
      ]},
      { title: "身份", fields: [
        { name: "action", zh: "动作", v: d.action || "chatbot" },
        { name: "firmware_version", zh: "固件版本", v: d.firmware_version || "", note: ID_RISK },
        { name: "nic_type", zh: "网卡类型", v: d.nic_type || "", note: ID_RISK },
        { name: "nic_iccid", zh: "SIM ICCID", v: d.nic_iccid || "", note: ID_RISK },
        { name: "uuid.min", zh: "UUID 下界", v: (d.uuid || {}).min ?? 1, num: true },
        { name: "uuid.max", zh: "UUID 上界", v: (d.uuid || {}).max ?? 2147483647, num: true },
      ]},
      { title: "音频", fields: [
        { name: "audio.bitrate_kbps", zh: "码率", v: a.bitrate_kbps ?? 0, num: true, note: "0 = 按格式取默认" },
        { name: "audio.slice_ms", zh: "切片长度", v: a.slice_ms ?? 100, num: true },
        { name: "audio.max_payload_size", zh: "单包上限", v: a.max_payload_size ?? 51200, num: true },
      ]},
      { title: "行为", fields: [
        { name: "behavior.keepalive_interval_sec", zh: "心跳间隔", v: beh.keepalive_interval_sec ?? 60, num: true },
        { name: "behavior.first_reply_timeout_sec", zh: "首包超时", v: beh.first_reply_timeout_sec ?? 20, num: true },
        { name: "behavior.speak_backlog_depth", zh: "送话排队深度", v: beh.speak_backlog_depth ?? 0, num: true },
        { name: "behavior.silence_probe", zh: "静默探针", check: true, on: !!beh.silence_probe, v: "timeout 静默终态后发一个探针 report" },
        { name: "behavior.interrupt_on_disconnect", zh: "断线即打断", check: true, on: !!beh.interrupt_on_disconnect, v: "事件 WS 断开时打断当前 turn" },
      ]},
      { title: "录音", fields: [
        { name: "recording.enable_frame_log", zh: "帧日志", check: true, on: !!rec.enable_frame_log, v: "记录每个包的序号与字节数" },
        { name: "recording.save_uplink_audio", zh: "存上行", check: true, on: !!rec.save_uplink_audio, v: "把送出去的音频落盘" },
        { name: "recording.save_downlink_audio", zh: "存下行", check: true, on: !!rec.save_downlink_audio, v: "把服务端回的音频落盘" },
        { name: "recording.output_dir", zh: "输出目录", v: rec.output_dir || "", note: "空 = 用 manager 的 RecordingsDir" },
      ]},
      { title: "拍照", fields: [
        { name: "features.photo.enabled", zh: "拍照功能", check: true, on: !!photo.enabled, v: "收到拍照指令时传图" },
        { name: "features.photo.image", zh: "拍照用图", v: photo.image || "", select: imgOpts, ph: "选一张图片资产" },
        { name: "features.photo.server_default_reply", zh: "回复格式用服务端默认", check: true, on: !!photo.server_default_reply, v: "Reserved 留空，服务端按默认回 aac" },
        { name: "features.photo.slice_interval_ms", zh: "分片间隔 ms", v: photo.slice_interval_ms ?? 0, num: true, note: "0 = 50ms" },
        { name: "features.photo.reply_timeout_sec", zh: "回复超时 s", v: photo.reply_timeout_sec ?? 0, num: true, note: "0 = 60s" },
      ]},
    ];
    return `<form id="form-product" class="prod-form sheet sheet--tight">
      <div class="grp__head"><span class="grp__title">${isNew ? "新建产品" : "编辑 " + p.id}</span></div>
      ${groups.map((g) => `<div class="grp">
        <div class="grp__head"><span class="grp__title">${esc(g.title)}</span><span class="rule"></span></div>
        <div class="fields">${g.fields.map(fieldHTML).join("")}</div>
      </div>`).join("")}
      <div class="foot">
        <button type="submit" class="btn btn--primary">${isNew ? "创建产品" : "保存产品"}</button>
        <button type="button" class="btn btn--sub" data-pact="cancel">取消</button>
        <span class="grow"></span>
        <span class="api">${isNew ? "POST /products" : "PUT /products/" + esc(p.id)}</span>
      </div>
    </form>`;
  }

  function readProductForm() {
    const root = $("products-body");
    const modes = formChecked("playing_modes", root).map(Number);
    const formats = formChecked("audio_formats", root);
    const spec = String(formVal("audio_spec", root) || "pcm/16000");
    const slash = spec.indexOf("/");
    const format = slash >= 0 ? spec.slice(0, slash) : spec;
    const sample_rate = slash >= 0 ? Number(spec.slice(slash + 1)) : 16000;
    return {
      id: String(formVal("id", root) || "").trim(),
      name: String(formVal("name", root) || "").trim(),
      playing_modes: modes,
      audio_formats: formats,
      defaults: {
        action: formVal("action", root),
        firmware_version: formVal("firmware_version", root),
        nic_type: formVal("nic_type", root),
        nic_iccid: formVal("nic_iccid", root),
        playing_mode: Number(formVal("playing_mode", root)) || 1,
        audio: {
          format, sample_rate,
          channels: 1, sample_format: "s16le",
          slice_ms: Number(formVal("audio.slice_ms", root)),
          max_payload_size: Number(formVal("audio.max_payload_size", root)),
          bitrate_kbps: Number(formVal("audio.bitrate_kbps", root)) || 0,
        },
        uuid: { min: Number(formVal("uuid.min", root)), max: Number(formVal("uuid.max", root)) },
        behavior: {
          keepalive_interval_sec: Number(formVal("behavior.keepalive_interval_sec", root)),
          first_reply_timeout_sec: Number(formVal("behavior.first_reply_timeout_sec", root)),
          speak_backlog_depth: Number(formVal("behavior.speak_backlog_depth", root)) || 0,
          silence_probe: !!formVal("behavior.silence_probe", root),
          interrupt_on_disconnect: !!formVal("behavior.interrupt_on_disconnect", root),
        },
        recording: {
          enable_frame_log: !!formVal("recording.enable_frame_log", root),
          save_uplink_audio: !!formVal("recording.save_uplink_audio", root),
          save_downlink_audio: !!formVal("recording.save_downlink_audio", root),
          output_dir: formVal("recording.output_dir", root),
        },
        features: { photo: {
          enabled: !!formVal("features.photo.enabled", root),
          image: formVal("features.photo.image", root),
          server_default_reply: !!formVal("features.photo.server_default_reply", root),
          slice_interval_ms: Number(formVal("features.photo.slice_interval_ms", root)) || 0,
          reply_timeout_sec: Number(formVal("features.photo.reply_timeout_sec", root)) || 0,
        } },
      },
    };
  }

  function cascadeProductSelects() {
    const root = $("products-body");
    if (!root) return;
    const modes = formChecked("playing_modes", root);
    const formats = formChecked("audio_formats", root);
    const modeSel = root.querySelector('[name="playing_mode"]');
    const specSel = root.querySelector('[name="audio_spec"]');
    if (modeSel) {
      const cur = modeSel.value;
      modeSel.innerHTML = selOpts(PLAYING_MODE_OPTS.filter((o) => modes.includes(o.v)), modes.includes(cur) ? cur : (modes[0] || ""), "");
    }
    if (specSel) {
      const cur = specSel.value;
      specSel.innerHTML = selOpts(formats.map((s) => ({ v: s, t: s })), formats.includes(cur) ? cur : (formats[0] || ""), "");
    }
  }

  async function saveProduct(ev) {
    if (ev) ev.preventDefault();
    const body = readProductForm();
    if (!body.name) { flash("名称不能空", "err"); return; }
    if (!body.playing_modes.length) { flash("对话模式清单不能空", "err"); return; }
    if (!body.audio_formats.length) { flash("音频格式清单不能空", "err"); return; }
    const isNew = state.productForm && state.productForm.mode === "new";
    if (isNew && !body.id) { flash("id 不能空", "err"); return; }
    try {
      if (isNew) await api("POST", "/products", body);
      else {
        const id = state.productForm.id;
        delete body.id;
        await api("PUT", `/products/${encodeURIComponent(id)}`, body);
      }
      flash(isNew ? "POST /products · 已创建 " + body.id : "PUT /products/" + state.productForm.id, "ok");
      state.productForm = null;
      await loadProducts();
    } catch (err) {
      apiErr(err);
    }
  }

  async function deleteProduct(id) {
    try {
      await api("DELETE", `/products/${encodeURIComponent(id)}`);
      flash("已删除产品 " + id, "ok");
      if (state.productForm && state.productForm.id === id) state.productForm = null;
      await loadProducts();
    } catch (err) {
      apiErr(err);
    }
  }

  function bindProducts() {
    $("products-new").addEventListener("click", () => {
      state.productForm = { mode: "new" };
      renderProducts();
    });
    $("products-body").addEventListener("click", (e) => {
      const btn = e.target.closest("[data-pact]");
      if (!btn) return;
      const act = btn.dataset.pact;
      if (act === "cancel") { state.productForm = null; renderProducts(); return; }
      const row = btn.closest("tr");
      const id = row && row.dataset.id;
      if (act === "edit" && id) { state.productForm = { mode: "edit", id }; renderProducts(); }
      else if (act === "del" && id) armThen(btn, () => deleteProduct(id));
    });
    $("products-body").addEventListener("change", (e) => {
      const n = e.target && e.target.name;
      if (n === "playing_modes" || n === "audio_formats") cascadeProductSelects();
    });
    $("products-body").addEventListener("submit", (e) => {
      if (e.target.getAttribute("id") === "form-product") saveProduct(e);
    });
  }

  // ——— 音频集（Phase 13）———

  function renderSets() {
    const rows = state.audioSets;
    setNum($("sets-count"), rows.length);
    const byId = Object.fromEntries(state.lib.rows.map((a) => [a.asset_id, a]));
    const table = rows.length ? `<table class="grid">
      <thead><tr><th>名称</th><th>条数</th><th>总时长</th><th>id</th><th class="grid__ops">操作</th></tr></thead>
      <tbody>${rows.map((s) => `<tr data-id="${esc(s.id)}" data-sel="${state.setEdit === s.id ? 1 : 0}">
        <td>${esc(s.name)}</td>
        <td class="mono">${s.asset_ids.length}</td>
        <td class="mono">${esc(fmtDurMs(s.asset_ids.reduce((n, id) => n + ((byId[id] || {}).duration_ms || 0), 0)))}</td>
        <td class="grid__id">${esc(s.id)}</td>
        <td class="grid__ops">
          <button type="button" class="btn btn--sub btn--tiny" data-sact="edit">编辑</button>
          <button type="button" class="btn btn--danger btn--tiny" data-sact="del" data-armed="0">删除</button>
        </td>
      </tr>`).join("")}</tbody></table>`
      : `<div class="blank"><p class="blank__title">还没有音频集</p><p class="blank__body">把素材库里的音频按顺序编成一组：调试台送话下拉里选它，或 simctl run --audio-set 整组送。</p></div>`;
    setHTML($("sets-body"), table + setFormHTML(byId));
  }

  function setFormHTML(byId) {
    if (!state.setEdit) return "";
    const off = state.setSaving ? " disabled" : "";
    const nameFld = (v, zh) => `<div class="fields"><label class="fld fld--wide">
      <span class="fld__head"><span class="fld__name">name</span><span class="fld__zh">${esc(zh)}</span></span>
      <input name="name" value="${esc(v)}" placeholder="冒烟" autocomplete="off"${off}></label></div>`;
    if (state.setEdit === "new") {
      return `<form id="form-set" class="prod-form sheet sheet--tight">
        <div class="grp__head"><span class="grp__title">新建音频集</span></div>
        ${nameFld("", "名称，不能重名；建好后再加条目")}
        <div class="foot">
          <button type="submit" class="btn btn--primary"${off}>创建</button>
          <button type="button" class="btn btn--sub" data-sact="close">取消</button>
          <span class="grow"></span><span class="api">POST /audio_sets</span>
        </div>
      </form>`;
    }
    const set = state.audioSets.find((x) => x.id === state.setEdit);
    if (!set) return "";
    const last = set.asset_ids.length - 1;
    const items = set.asset_ids.map((id, i) => {
      const a = byId[id];
      return `<li class="setitem" data-i="${i}">
        <span class="setitem__n">${i + 1}</span>
        <span class="setitem__name">${esc(a ? a.name || id : "（素材已不在库里）")}</span>
        <span class="setitem__meta">${esc(a ? [a.language, a.format, fmtDurMs(a.duration_ms)].filter(Boolean).join(" · ") : id)}</span>
        <button type="button" class="btn btn--ghost btn--tiny" data-sact="up" aria-label="上移"${i === 0 ? " disabled" : off}>↑</button>
        <button type="button" class="btn btn--ghost btn--tiny" data-sact="down" aria-label="下移"${i === last ? " disabled" : off}>↓</button>
        <button type="button" class="btn btn--ghost btn--tiny" data-sact="rm" aria-label="移出"${off}>✕</button>
      </li>`;
    }).join("");
    const opts = audioLib().map((a) => ({ v: a.asset_id, t: [a.name || a.asset_id, a.language, fmtDurMs(a.duration_ms)].filter(Boolean).join(" · ") }));
    return `<form id="form-set" class="prod-form sheet sheet--tight">
      <div class="grp__head"><span class="grp__title">编辑 ${esc(set.name)}</span><span class="rule"></span><span class="api">${esc(set.id)}</span></div>
      ${nameFld(set.name, "改完离开输入框即保存")}
      <div class="grp">
        <div class="grp__head"><span class="grp__title">条目 · 按顺序送</span><span class="rule"></span></div>
        ${set.asset_ids.length ? `<ol class="setlist">${items}</ol>` : `<p class="fld__note">还没有条目，从下面的素材库下拉里加。</p>`}
        <div class="setadd">
          <select id="set-add" aria-label="从素材库选音频"${off}>${selOpts(opts, "", "选素材库里的音频…")}</select>
          <button type="button" class="btn btn--sub btn--sm" data-sact="add"${off}>加入</button>
        </div>
      </div>
      <div class="foot">
        <button type="button" class="btn btn--sub" data-sact="close">收起</button>
        <button type="button" class="btn btn--sub" data-sact="compose" title="各条切掉首尾静音、首尾相接拼成一条连续音频存进素材库，一轮送出（一句话问几件事）"${set.asset_ids.length < 2 ? " disabled" : off}>拼成一条素材</button>
        <span class="grow"></span><span class="api">PUT /audio_sets/${esc(set.id)} · 每次改动立即保存 · 拼接 POST /assets/compose</span>
      </div>
    </form>`;
  }

  // putSet 整份替换。在途时编辑区禁用，所以一次只会有一个 PUT。
  async function putSet(set, patch) {
    if (state.setSaving) return;
    state.setSaving = true;
    renderSets();
    try {
      const saved = await api("PUT", `/audio_sets/${encodeURIComponent(set.id)}`,
        { name: set.name, asset_ids: set.asset_ids, ...patch });
      state.audioSets = state.audioSets.map((x) => (x.id === saved.id ? saved : x));
    } catch (err) {
      apiErr(err);
    } finally {
      state.setSaving = false;
      renderSets();
      renderSrc();
    }
  }

  async function createSet() {
    const input = $("sets-body").querySelector('[name="name"]');
    const name = input ? input.value.trim() : "";
    if (!name) { flash("名称不能空", "err"); return; }
    try {
      const set = await api("POST", "/audio_sets", { name });
      state.audioSets.push(set);
      state.setEdit = set.id; // 建完直接进编辑，接着加条目
      flash("已创建音频集 " + name, "ok");
    } catch (err) {
      apiErr(err);
    }
    renderSets();
    renderSrc();
  }

  // composeSet 把集里的条目按顺序拼成一条连续音频素材。同组合服务端复用，不会重复建。
  async function composeSet(set) {
    try {
      const a = await api("POST", "/assets/compose", { asset_ids: set.asset_ids });
      flash(`${a.reused ? "已有" : "已拼成"}「${a.name}」（${fmtDurMs(a.duration_ms)}），送话下拉里选它`, "ok");
      await loadLibrary();
    } catch (err) {
      apiErr(err);
    }
  }

  async function deleteSet(id) {
    try {
      await api("DELETE", `/audio_sets/${encodeURIComponent(id)}`);
      state.audioSets = state.audioSets.filter((x) => x.id !== id);
      if (state.setEdit === id) state.setEdit = null;
      flash("已删除音频集（音频还在素材库里）", "ok");
    } catch (err) {
      apiErr(err);
    }
    renderSets();
    renderSrc();
  }

  function bindSets() {
    $("btn-view-sets").addEventListener("click", () => setView("sets"));
    $("sets-new").addEventListener("click", () => { state.setEdit = "new"; renderSets(); });
    const body = $("sets-body");
    body.addEventListener("click", (e) => {
      const btn = e.target.closest("[data-sact]");
      if (!btn || btn.disabled) return;
      const act = btn.dataset.sact;
      if (act === "close") { state.setEdit = null; renderSets(); return; }
      const row = btn.closest("tr");
      if (row) {
        if (act === "edit") { state.setEdit = row.dataset.id; renderSets(); }
        else if (act === "del") armThen(btn, () => deleteSet(row.dataset.id));
        return;
      }
      const set = state.audioSets.find((x) => x.id === state.setEdit);
      if (!set) return;
      if (act === "compose") { composeSet(set); return; }
      const ids = set.asset_ids.slice();
      const li = btn.closest("[data-i]");
      const i = li ? Number(li.dataset.i) : -1;
      if (act === "up" && i > 0) [ids[i - 1], ids[i]] = [ids[i], ids[i - 1]];
      else if (act === "down" && i >= 0 && i < ids.length - 1) [ids[i + 1], ids[i]] = [ids[i], ids[i + 1]];
      else if (act === "rm" && i >= 0) ids.splice(i, 1);
      else if (act === "add") {
        const pick = $("set-add").value;
        if (!pick) { flash("先在下拉里选一条音频", "err"); return; }
        ids.push(pick);
      } else return;
      putSet(set, { asset_ids: ids });
    });
    // 改名：离开输入框（或回车）即保存；清空不算，留着原名。
    body.addEventListener("change", (e) => {
      if (e.target.name !== "name" || state.setEdit === "new") return;
      const set = state.audioSets.find((x) => x.id === state.setEdit);
      const name = e.target.value.trim();
      if (set && name && name !== set.name) putSet(set, { name });
    });
    body.addEventListener("submit", (e) => {
      e.preventDefault();
      if (state.setEdit === "new") createSet();
    });
  }

  function manageRows() {
    const q = state.manageQ.trim().toLowerCase();
    if (!q) return state.devices;
    return state.devices.filter((d) => [d.device_id, d.environment, d.enterprise, d.device_type, d.product, (d.audio || {}).format]
      .some((x) => String(x || "").toLowerCase().includes(q)));
  }

  function renderManage() {
    const rows = manageRows();
    setText($("manage-count"), String(rows.length));
    $("manage-empty").hidden = rows.length > 0;
    $("manage-empty-title").textContent = state.devices.length ? "没有匹配的设备" : "还没有设备";
    const sel = [...state.checked].filter((id) => state.devices.some((d) => d.device_id === id));
    $("manage-batch").hidden = sel.length === 0;
    setText($("manage-sel-count"), String(sel.length));
    $("manage-all").checked = rows.length > 0 && rows.every((d) => state.checked.has(d.device_id));

    setHTML($("manage-rows"), rows.map((d) => {
      const a = d.audio || {};
      const picked = state.checked.has(d.device_id);
      const over = d.overridden;
      return `<tr data-id="${esc(d.device_id)}" data-sel="${picked ? 1 : 0}">
        <td class="grid__pick"><input type="checkbox" data-pick="${esc(d.device_id)}"${picked ? " checked" : ""} aria-label="选择 ${esc(d.device_id)}"></td>
        <td class="grid__id">${esc(d.device_id)}</td>
        <td class="grid__path">${esc(d.environment || "—")} · ${esc(d.enterprise || "—")} · ${esc(d.device_type || "—")}</td>
        <td class="mono">${esc(d.product || "—")}${over ? ` <span class="${tagCls("warn")}">临时覆盖</span>` : ""}</td>
        <td class="mono">${esc(a.format || "—")}</td>
        <td class="mono">${esc(a.sample_rate || "—")}</td>
        <td class="mono grid__dim">${esc(d.playing_mode || "—")}</td>
        <td><span class="${tagCls(d.instance_state === "running" ? "ok" : "")}">${esc(d.instance_state || "—")}</span></td>
        <td>${over ? `<span class="${tagCls("warn")}">临时覆盖</span>` : `<span class="grid__dim">—</span>`}</td>
        <td class="grid__ops">
          <button type="button" class="btn btn--ghost btn--tiny" data-mact="open" title="到调试台选中这台">调试</button>
          <button type="button" class="btn btn--danger btn--tiny" data-mact="del" data-armed="0">删除</button>
        </td>
      </tr>`;
    }).join(""));
  }

  async function resetConfig() {
    if (!state.selectedId) return;
    try {
      state.config = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/config/reset`, null);
      flash("已重置为产品默认", "ok");
      renderDrawer();
      await refreshList();
    } catch (err) {
      apiErr(err);
    }
  }

  function bindManage() {
    $("btn-view-bench").addEventListener("click", () => setView("bench"));
    $("btn-view-manage").addEventListener("click", () => setView("manage"));
    $("manage-new").addEventListener("click", () => openDrawer("new"));
    $("manage-empty-cta").addEventListener("click", () => openDrawer("new"));
    $("manage-q").addEventListener("input", (e) => { state.manageQ = e.target.value; renderManage(); });
    $("manage-sel-clear").addEventListener("click", () => { state.checked.clear(); renderManage(); renderRoster(); });
    $("manage-batch-start").addEventListener("click", () => batchAct("start"));
    $("manage-batch-stop").addEventListener("click", () => batchAct("stop"));
    $("manage-batch-delete").addEventListener("click", (e) => armThen(e.currentTarget, () => batchAct("delete")));
    $("manage-all").addEventListener("change", (e) => {
      for (const d of manageRows()) {
        if (e.target.checked) state.checked.add(d.device_id);
        else state.checked.delete(d.device_id);
      }
      renderManage(); renderRoster();
    });
    $("manage-rows").addEventListener("change", (e) => {
      const pick = e.target.dataset.pick;
      if (!pick) return;
      if (e.target.checked) state.checked.add(pick); else state.checked.delete(pick);
      renderManage(); renderRoster();
    });
    $("manage-rows").addEventListener("click", (e) => {
      const btn = e.target.closest("[data-mact]");
      if (!btn) return;
      const id = btn.closest("tr").dataset.id;
      const act = btn.dataset.mact;
      if (act === "open") { setView("bench"); selectDevice(id); }
      else if (act === "del") armThen(btn, async () => {
        try {
          await api("DELETE", `/devices/${encodeURIComponent(id)}`, null);
          flash("已删除 " + id, "ok");
          await refreshList();
        } catch (err) { apiErr(err); }
      });
    });
  }

  // armThen 两击确认：第一次亮起并等 3 秒，第二次才真执行。
  function armThen(btn, run) {
    if (btn.dataset.armed === "1") { btn.dataset.armed = "0"; btn.classList.remove("is-armed"); run(); return; }
    btn.dataset.armed = "1";
    btn.classList.add("is-armed");
    setTimeout(() => { btn.dataset.armed = "0"; btn.classList.remove("is-armed"); }, 3000);
  }

  // ——— 抽屉 ———

  const DRAWER_TITLE = {
    new: ["新建设备", "POST /devices"],
    config: ["配置", "GET / PUT /devices/{id}/config"],
    assets: ["素材库", "GET / POST /assets"],
    help: ["术语与状态机", ""],
    scenarios: ["场景编排", "POST /scenarios/run"],
    faults: ["注入故障", "POST /devices/{id}/faults"],
    runs: ["历史运行", "GET /devices/{id}/instances"],
    sheet: ["事件", "WS /ws/events"],
  };

  function openDrawer(which) {
    if (state.drawer === "sheet" && which !== "sheet") restoreSide();
    state.drawer = which;
    state.adding = null;
    // 抽屉换内容时签名要作废，否则「同样的 HTML」会被幂等渲染跳过。
    sigCache.delete($("drawer-body"));
    show($("scrim"), true);
    show($("drawer"), true);
    $("drawer").classList.toggle("is-wide", which === "new" || which === "config");
    const [t, a] = DRAWER_TITLE[which] || ["", ""];
    setText($("drawer-title"), which === "config" || which === "sheet" ? `${t} · ${state.selectedId || ""}` : t);
    setText($("drawer-api"), a);
    renderDrawer();
    $("drawer-body").scrollTop = 0;
  }

  function restoreSide() {
    const side = $("side");
    if (side && side.parentElement !== document.querySelector(".bench")) {
      document.querySelector(".bench").appendChild(side);
      side.style.display = "";
      sigCache.delete($("drawer-body"));
    }
  }

  function closeDrawer() {
    if (state.drawer === "sheet") restoreSide();
    state.drawer = null;
    show($("scrim"), false);
    show($("drawer"), false);
  }

  function selOpts(rows, cur, placeholder) {
    return (placeholder ? `<option value="">${esc(placeholder)}</option>` : "") +
      rows.map((r) => `<option value="${esc(r.v)}"${String(r.v) === String(cur) ? " selected" : ""}>${esc(r.t)}</option>`).join("");
  }

  const TREE_LEVELS = [
    {
      key: "env", label: "环境",
      l1: "环境名", p1: "prod / staging / uat",
      l2: "url", p2: "ws://127.0.0.1:8089/{enterprise}",
      api: "POST /registry/environments",
      empty: "还没有环境",
    },
    {
      key: "ent", label: "厂商",
      l1: "名称", p1: "Acme 智能",
      l2: "简称（wire 值）", p2: "acme",
      api: "POST /registry/environments/{env}/enterprises",
      empty: "该环境下还没有厂商",
    },
    {
      key: "typ", label: "设备类型",
      l1: "名称", p1: "迷你音箱",
      l2: "简称（wire 值）", p2: "speaker-mini",
      api: "POST …/enterprises/{short}/device_types",
      empty: "该厂商下还没有设备类型",
    },
  ];

  // 配置树：环境 / 厂商 / 设备类型 三列并排，点中一列的某一项，右边那列就列出它下面的数据。
  function registryHTML() {
    const s = state.regSel;
    const cols = [
      {
        cfg: TREE_LEVELS[0], cur: s.env, blocked: "",
        rows: state.registry.map((r) => ({ v: r.name, t: r.name, sub: r.url })),
      },
      {
        cfg: TREE_LEVELS[1], cur: s.ent, blocked: s.env ? "" : "先选一个环境",
        rows: regEnts().map((r) => ({ v: r.short_name, t: r.name, sub: r.short_name })),
      },
      {
        cfg: TREE_LEVELS[2], cur: s.typ, blocked: s.ent ? "" : "先选一个厂商",
        rows: regTypes().map((r) => ({ v: r.short_name, t: r.name, sub: r.short_name, def: r.default_product || "" })),
      },
    ];
    return `<div class="tree">` + cols.map((c) => {
      const k = c.cfg.key;
      const list = c.blocked
        ? `<p class="tcol__blank">${esc(c.blocked)}</p>`
        : c.rows.length
          ? `<ul class="tcol__list">` + c.rows.map((r) => {
            const on = String(r.v) === String(c.cur);
            return `<li>
              <div class="tnode${on ? " is-on" : ""}">
                <button type="button" class="tnode__pick" data-tree-pick="${k}" data-v="${esc(r.v)}" aria-pressed="${on}">
                  <span class="tnode__t">${esc(r.t)}</span>
                  ${r.sub ? `<span class="tnode__sub">${esc(r.sub)}${r.def ? " · 默认 " + esc(r.def) : ""}</span>` : ""}
                </button>
                ${on ? `<span class="tnode__ops">
                  <button type="button" class="btn btn--ghost" data-tree-rename="${k}">编辑</button>
                  <button type="button" class="btn btn--ghost" data-tree-del="${k}" data-armed="0" title="再点一次确认删除">删除</button>
                </span>` : ""}
              </div>
              ${on && state.regEdit === k ? `<div class="lvl__add">
                <label class="fld">
                  <span class="fld__name">${esc(k === "env" ? c.cfg.l2 : c.cfg.l1)}</span>
                  <input data-edit="1" value="${esc(k === "env" ? (r.sub || "") : r.t)}">
                </label>
                ${k === "env" ? "" : `<label class="fld">
                  <span class="fld__name">${esc(c.cfg.l2)}</span>
                  <input data-edit="2" value="${esc(r.sub || "")}">
                </label>`}
                ${k === "typ" ? `<label class="fld">
                  <span class="fld__name">default_product</span>
                  <span class="fld__zh">默认产品</span>
                  <select data-edit="product">${productOpts(r.def || "", "无（启动时必须手选）")}</select>
                </label>` : ""}
                <div class="lvl__foot">
                  <button type="button" class="btn btn--primary btn--sm" data-tree-save="${k}">保存</button>
                  <button type="button" class="btn btn--ghost" data-tree-cancel="1">取消</button>
                  <span class="grow"></span>
                  <span class="api">${esc(k === "env"
                    ? "环境名是主键，改不了；这里改的是 url"
                    : "简称是 wire 值，改了下次 start 生效")}</span>
                </div>
              </div>` : ""}
            </li>`;
          }).join("") + `</ul>`
          : `<p class="tcol__blank">${esc(c.cfg.empty)}</p>`;
      return `<section class="tcol">
        <header class="tcol__head">
          <span class="tcol__label">${esc(c.cfg.label)}</span>
          <span class="tag tag--pill">${c.blocked ? 0 : c.rows.length}</span>
          <span class="grow"></span>
          <button type="button" class="btn btn--ghost" data-tree-add="${k}"${c.blocked ? " disabled" : ""}>＋ 新增</button>
        </header>
        ${state.adding === k ? `<div class="lvl__add">
          <div class="pair">
            <label class="fld"><span class="fld__name">${esc(c.cfg.l1)}</span><input data-add="1" placeholder="${esc(c.cfg.p1)}"></label>
            <label class="fld"><span class="fld__name">${esc(c.cfg.l2)}</span><input data-add="2" placeholder="${esc(c.cfg.p2)}" value="${k === "env" ? esc(c.cfg.p2) : ""}"></label>
          </div>
          ${k === "typ" ? `<label class="fld"><span class="fld__name">default_product</span><span class="fld__zh">默认产品</span><select data-add="product">${productOpts("", "无默认产品")}</select></label>` : ""}
          <div class="lvl__foot">
            <button type="button" class="btn btn--primary btn--sm" data-tree-ok="${k}">确认新增</button>
            <button type="button" class="btn btn--ghost" data-tree-cancel="1">取消</button>
          </div>
          <span class="api">${esc(c.cfg.api)}</span>
        </div>` : ""}
        ${list}
      </section>`;
    }).join("") + `</div>`;
  }

  function fieldHTML(f) {
    const wide = (f.note && f.note.length > 30) || (f.checks && f.checks.length > 3);
    const control = f.checks
      ? `<div class="picks">${f.checks.map((c) => `<label class="pick"><input type="checkbox" name="${esc(f.name)}" value="${esc(c.v)}"${c.on ? " checked" : ""}${f.locked ? " disabled" : ""}><span>${esc(c.t)}</span></label>`).join("")}</div>`
      : f.select
        ? `<select name="${esc(f.name)}"${f.locked ? " disabled" : ""}>${selOpts(f.select, f.v, f.ph || "")}</select>`
        : f.check
          ? `<span class="fld__chk"><input type="checkbox" name="${esc(f.name)}"${f.on ? " checked" : ""}${f.locked ? " disabled" : ""}><span>${esc(f.v || "")}</span></span>`
          : `<input name="${esc(f.name)}" value="${esc(f.v ?? "")}" placeholder="${esc(f.ph || "")}"${f.locked || f.ro ? " disabled" : ""}${f.num ? ` type="number"` : ""}>`;
    const tag = f.checks ? "div" : "label";
    return `<${tag} class="fld${wide ? " fld--wide" : ""}">
      <span class="fld__head">
        <span class="fld__name">${esc(f.name)}</span>
        <span class="fld__zh">${esc(f.zh || "")}</span>
        ${f.ro ? `<span class="fld__ro">只读</span>` : ""}
        ${f.dirty ? `<span class="tag tag--warn">临时覆盖</span>` : ""}
      </span>
      ${control}
      ${f.note ? `<span class="fld__note">${esc(f.note)}</span>` : ""}
    </${tag}>`;
  }

  function newDrawerHTML() {
    const single = state.newMode === "single";
    const fields = single
      ? [{ name: "device_id", zh: "设备标识", v: "sim_0001", ph: "sim_0001" }]
      : [
        { name: "id_prefix", zh: "ID 前缀", v: "sim", ph: "sim", note: "生成 sim_1 … sim_N；任一冲突整批失败" },
        { name: "count", zh: "数量", v: "2", ph: "2", num: true },
      ];
    return `<div class="sheet">
      <div class="step">
        <div class="step__head">
          <span class="step__n">1</span><span class="step__t">怎么建</span>
          <span class="grow"></span>
          <span class="seg">
            <button type="button" class="seg__btn${single ? " is-on" : ""}" data-newmode="single">单个</button>
            <button type="button" class="seg__btn${single ? "" : " is-on"}" data-newmode="batch">前缀＋数量</button>
          </span>
        </div>
        <form id="form-create" class="fields fields--pad">${fields.map(fieldHTML).join("")}</form>
        <p class="hint" style="padding-left:29px">${single
          ? "设备册只留 device_id。属性在启动时从产品合成。"
          : "一次 POST /devices 带 id_prefix + count，生成 前缀_1 … 前缀_N。任一 ID 冲突，整批回滚。"}</p>
        <div class="foot foot--plain" style="padding-left:29px">
          <button type="button" class="btn btn--primary" data-act="create">${single ? "创建设备" : "批量创建"}</button>
          <button type="button" class="btn btn--sub" data-act="close">取消</button>
        </div>
      </div>
    </div>`;
  }

  function isOverridden(path) {
    const ov = (state.config && state.config.overrides) || {};
    return Object.prototype.hasOwnProperty.call(ov, path);
  }

  function mark(f) {
    return Object.assign(f, { dirty: isOverridden(f.name) });
  }

  function configDrawerHTML() {
    const cfg = state.config;
    if (!cfg) return `<p class="blank--drawer">这台设备没有可读的配置（可能已进入墓碑态）。</p>`;
    const noProd = !cfg.product;
    const identLock = noProd || state.tombstone || !identityEditable();
    const liveLock = noProd || state.tombstone;
    const audio = cfg.audio || {};
    const beh = cfg.behavior || {};
    const rec = cfg.recording || {};
    const uuid = cfg.uuid || {};
    const photo = ((cfg.features || {}).photo) || {};
    const st = state.tombstone ? "deleted" : ((state.live && state.live.instance_state) || "created");
    const imgOpts = imageLib().map((x) => ({ v: x.asset_id, t: x.name || x.asset_id }));
    if (photo.image && !imgOpts.some((r) => r.v === photo.image)) imgOpts.push({ v: photo.image, t: photo.image });

    const groups = [
      { title: "音频", tag: "仅 created / stopped 可改", lock: true, fields: [
        mark({ name: "audio.format", zh: "格式", v: audio.format || "pcm", locked: identLock,
          select: ["pcm", "wav", "mp3", "amr", "aac"].map((f) => ({ v: f, t: f })) }),
        mark({ name: "audio.sample_rate", zh: "采样率", v: audio.sample_rate ?? 16000, num: true, locked: identLock, note: "amr 仅支持 8000 / 16000" }),
        mark({ name: "audio.bitrate_kbps", zh: "码率", v: audio.bitrate_kbps ?? 0, num: true, locked: identLock, note: "0 = 默认" }),
        mark({ name: "audio.slice_ms", zh: "切片长度", v: audio.slice_ms ?? 100, num: true, locked: identLock }),
        mark({ name: "audio.max_payload_size", zh: "单包上限", v: audio.max_payload_size ?? 51200, num: true, locked: identLock }),
      ]},
      { title: "身份", tag: "仅 created / stopped 可改", lock: true, fields: [
        mark({ name: "firmware_version", zh: "固件版本", v: cfg.firmware_version || "", locked: identLock, note: ID_RISK }),
        mark({ name: "nic_type", zh: "网卡类型", v: cfg.nic_type || "", locked: identLock, note: ID_RISK }),
        mark({ name: "nic_iccid", zh: "SIM ICCID", v: cfg.nic_iccid || "", locked: identLock, note: ID_RISK }),
        mark({ name: "playing_mode", zh: "对话模式", v: cfg.playing_mode ?? 1, num: true, locked: identLock, note: "1 按键 / 2 连续 / 3 唤醒" }),
        mark({ name: "uuid.min", zh: "UUID 下界", v: uuid.min ?? 1, num: true, locked: identLock }),
        mark({ name: "uuid.max", zh: "UUID 上界", v: uuid.max ?? 2147483647, num: true, locked: identLock }),
      ]},
      { title: "行为", tag: "仅 created / stopped 可改", lock: true, fields: [
        mark({ name: "behavior.keepalive_interval_sec", zh: "心跳间隔", v: beh.keepalive_interval_sec ?? 60, num: true, locked: identLock }),
        mark({ name: "behavior.first_reply_timeout_sec", zh: "首包超时", v: beh.first_reply_timeout_sec ?? 20, num: true, locked: identLock }),
        mark({ name: "behavior.speak_backlog_depth", zh: "送话排队深度", v: beh.speak_backlog_depth ?? 0, num: true, locked: identLock }),
        mark({ name: "behavior.silence_probe", zh: "静默探针", check: true, on: !!beh.silence_probe, locked: identLock, v: "timeout 静默终态后发一个探针 report" }),
        mark({ name: "behavior.interrupt_on_disconnect", zh: "断线即打断", check: true, on: !!beh.interrupt_on_disconnect, locked: identLock, v: "事件 WS 断开时打断当前 turn" }),
      ]},
      { title: "录音", tag: "running 也可改", lock: false, fields: [
        mark({ name: "recording.enable_frame_log", zh: "帧日志", check: true, on: !!rec.enable_frame_log, locked: liveLock, v: "记录每个包的序号与字节数" }),
        mark({ name: "recording.save_uplink_audio", zh: "存上行", check: true, on: !!rec.save_uplink_audio, locked: liveLock, v: "把送出去的音频落盘" }),
        mark({ name: "recording.save_downlink_audio", zh: "存下行", check: true, on: !!rec.save_downlink_audio, locked: liveLock, v: "把服务端回的音频落盘" }),
        mark({ name: "recording.output_dir", zh: "输出目录", v: rec.output_dir || "", locked: liveLock }),
      ]},
      { title: "拍照", tag: "running 也可改", lock: false, fields: [
        mark({ name: "features.photo.enabled", zh: "拍照功能", check: true, on: !!photo.enabled, locked: liveLock, v: "下一次收到拍照指令时生效" }),
        mark({ name: "features.photo.image", zh: "拍照用图", v: photo.image || "", select: imgOpts, ph: "选一张图片资产", locked: liveLock }),
        mark({ name: "features.photo.server_default_reply", zh: "回复格式用服务端默认", check: true, on: !!photo.server_default_reply, locked: liveLock, v: "Reserved 留空，服务端按默认回 aac" }),
        mark({ name: "features.photo.slice_interval_ms", zh: "分片间隔 ms", v: photo.slice_interval_ms ?? 0, num: true, locked: liveLock, note: "0 = 50ms" }),
        mark({ name: "features.photo.reply_timeout_sec", zh: "回复超时 s", v: photo.reply_timeout_sec ?? 0, num: true, locked: liveLock, note: "0 = 60s" }),
      ]},
    ];
    const conn = (state.live && state.live.connection_state) || "—";
    const barKind = noProd ? "mute" : (identLock ? "warn" : "ok");
    const barText = noProd
      ? "这台设备还没选过产品。先在启动条选产品并启动一次，才能改配置。"
      : (identLock
        ? `当前产品 ${esc(cfg.product)} · instance_state ${esc(st)}，只有录音与拍照能改。`
        : `当前产品 ${esc(cfg.product)} · instance_state ${esc(st)}。保存只提交改过的字段。`);
    return `<form id="form-config" class="sheet sheet--tight">
      <div class="lockbar lockbar--${barKind}">
        <span class="tag">${noProd ? "未选产品" : productLabel(cfg.product)}</span>
        <span>${barText}</span>
      </div>
      ${groups.map((g) => `<div class="grp">
        <div class="grp__head">
          <span class="grp__title">${esc(g.title)}</span>
          <span class="${tagCls(g.lock ? (identLock ? "warn" : "") : "ok")}">${esc(g.tag)}</span>
          <span class="rule"></span>
        </div>
        <div class="fields">${g.fields.map(fieldHTML).join("")}</div>
      </div>`).join("")}
      <div class="foot">
        <button type="submit" class="btn btn--primary"${noProd || state.tombstone ? " disabled" : ""}>保存配置（只对本次运行生效）</button>
        <button type="button" class="btn btn--sub" data-act="reset"${noProd || !cfg.overridden ? " disabled" : ""}
          title="POST /devices/{id}/config/reset · 丢弃临时覆盖，回到产品默认">重置为产品默认</button>
        <button type="button" class="btn btn--sub" data-act="report"${conn === "ready" && !noProd && !state.tombstone ? "" : " disabled"}
          title="POST /devices/{id}/report {playingMode} · 仅 connection_state=ready">热更新对话模式</button>
        <span class="grow"></span>
        <span class="api">${cfg.overridden ? "临时覆盖 · " : ""}connection_state = ${esc(conn)}</span>
      </div>
    </form>`;
  }

  const LANGS = [
    { v: "zh", t: "zh 中文" },
    { v: "en", t: "en 英语" },
    { v: "ja", t: "ja 日语" },
    { v: "ko", t: "ko 韩语" },
    { v: "id", t: "id 印尼语" },
    { v: "ar", t: "ar 阿拉伯语" },
  ];

  function assetsDrawerHTML() {
    const rows = visibleLib();
    const fmtOpts = state.lib.kind === "image"
      ? ["jpg", "png", "bmp"].map((f) => ({ v: f, t: f }))
      : ["pcm", "wav", "mp3", "amr", "aac"].map((f) => ({ v: f, t: f }));
    return `<div class="sheet sheet--tight">
      <div class="bar">
        <select data-lib="kind" style="width:120px">${selOpts([{ v: "audio", t: "音频" }, { v: "image", t: "图片" }], state.lib.kind, "全部类型")}</select>
        <select data-lib="format" style="width:140px">${selOpts(fmtOpts, state.lib.format, "全部格式")}</select>
        <select data-lib="lang" style="width:140px">${selOpts(LANGS, state.lib.language, "全部语言")}</select>
        <input data-lib="tag" type="search" placeholder="按标签过滤" value="${esc(state.lib.tag)}" style="width:110px" title="内容标签：对话 / 联网 / 唱歌…">
        <span class="grow"></span>
        <span style="display:flex;align-items:center;gap:8px;flex:none">
          <span class="dim" style="font-size:11.5px">导入语言</span>
          <select data-lib="newlang" style="width:118px" title="写进新素材的 language 字段，不是筛选">${selOpts(LANGS, state.lib.newLang, "不标注")}</select>
          <label class="btn btn--primary btn--sm">＋ 导入<input type="file" id="lib-file" accept=".wav,.mp3,.amr,.aac,.jpg,.jpeg,.png,.bmp,audio/*,image/*" hidden></label>
        </span>
      </div>
      <p class="hint">POST /assets · 音频 wav/mp3/amr/aac · 图片 jpg/png/bmp · GET /assets/{id}/content</p>
      <div class="list">
        ${rows.length ? rows.map((a) => {
          const armed = state.lib.armedId === a.asset_id;
          const editing = state.lib.editingId === a.asset_id;
          const img = a.kind === "image";
          if (editing) {
            return `<div class="row" data-id="${esc(a.asset_id)}">
              <div class="fields">
                <label class="fld"><span class="fld__name">名称</span><input data-edit="name" value="${esc(a.name)}"></label>
                <label class="fld"><span class="fld__name">语言</span><select data-edit="language">${selOpts(LANGS, a.language || "", "不标注")}</select></label>
                <label class="fld"><span class="fld__name">标签</span><input data-edit="tags" value="${esc((a.tags || []).join(", "))}" placeholder="对话, 联网, 唱歌"></label>
              </div>
              <div class="bar">
                <button type="button" class="btn btn--primary btn--sm" data-asset="save">保存</button>
                <button type="button" class="btn btn--ghost" data-asset="cancel">取消</button>
                <span class="grow"></span><span class="api">PATCH /assets/${esc(a.asset_id)}</span>
              </div>
            </div>`;
          }
          return `<div class="row" data-id="${esc(a.asset_id)}" data-tags="${esc((a.tags || []).join(" "))}">
            <div class="row__top">
              ${img ? `<img class="thumb" src="/assets/${esc(a.asset_id)}/content" alt="">` : ""}
              <span class="row__name" title="${esc(a.asset_id)}">${esc(a.name)}</span>
              <span class="${tagCls("acc")}">${esc(a.format)}</span>
              <span class="tag tag--mute">${esc(a.kind || "audio")}</span>
              ${(a.tags || []).map((t) => `<span class="tag tag--ok">${esc(t)}</span>`).join("")}
              <span class="grow"></span>
              ${img ? "" : `<button type="button" class="btn btn--ok btn--tiny" data-asset="play" title="试听（服务端解码为 wav）">▶</button>`}
              <button type="button" class="btn btn--ghost" data-asset="edit">改名</button>
              <button type="button" class="btn btn--danger btn--ghost" data-asset="del">${armed ? "再点一次" : "删除"}</button>
            </div>
            <div class="row__meta">
              <span>${esc(img ? fmtBytes(a.bytes) : (a.sample_rate ? a.sample_rate + " Hz" : "—"))}</span><span class="sep">|</span>
              <span>${esc(img ? (a.format || "image") : (a.bitrate_kbps ? a.bitrate_kbps + " kbps" : "—"))}</span><span class="sep">|</span>
              <span>${esc(img ? "" : fmtDurMs(a.duration_ms))}</span><span class="sep">|</span>
              <span>${esc(a.language || "—")}</span>
            </div>
          </div>`;
        }).join("") : `<p class="blank--drawer">库是空的，先导入一条</p>`}
      </div>
    </div>`;
  }

  const GLOSSARY = [
    ["instance_state", "设备实例的生命周期：created → starting → running → stopped，失败进 failed，删除后进墓碑。决定启动/停止/删除三个按钮能不能点。"],
    ["connection_state", "这一条 WebSocket 连接走到哪一步：disconnected → connected → registering → registered → reporting → ready。只有 ready 才能热更新 playingMode。"],
    ["conn_generation", "这台设备重连了几次。每次 start 都会 +1，用来区分旧实例的事件。"],
    ["instance_id", "一次启动的唯一标识。设备删掉后用同一个 instance_id 还能只读回看它的事件和 turn，TTL 24 小时。"],
    ["简称（wire 值）", "厂商和设备类型都有「名称」和「简称」两个字段。上线报文里真正发出去的是简称，名称只给人看。"],
    ["{enterprise} 占位符", "环境 url 里可以写 ws://127.0.0.1:8089/{enterprise}，start 时会用厂商简称替换掉它。"],
    ["product", "设备属性的来源。启动时选产品，实例上可以临时覆盖；换产品会清空覆盖。"],
    ["playing_mode", "对话模式，取值 1 按键 / 2 连续 / 3 唤醒。Ready 状态下可以不重启直接热更新。"],
    ["nic_iccid", "模拟设备上报的 SIM 卡 ICCID。换产品或改 ICCID 会让真实服务端重新校验这台设备。"],
    ["photo_command", "收到拍照指令，reason 是 QuestionKey。之后是 photo_uploaded 或 photo_skipped，再等图片分析的语音回复。"],
    ["photo_uploaded", "传完一张图。reason 的 source=speak 是带图送话（先传图再说话，图与音频同一个 UUID，回复是普通 TTS），source=command 是指令拍照；uuid 用来取本轮留档的那张图。"],
    ["stage2", "uplink_end_reason 的一种：上行音频推完了整个第二阶段才结束，不是被打断或超时。"],
    ["speak_backlog_depth", "送话排队深度。0 表示不排队，槽被占用时直接 409；大于 0 时新的送话进队列等前一轮结束。"],
    ["turn_terminal", "一轮对话的终结事件，带 reply_kind（回了什么）、turn_end_reason（怎么结束的）、uplink_end_reason（上行怎么停的）。"],
    ["backpressure", "服务端来不及收，模拟器主动降速。看到它说明推流速度超过了对端处理能力。"],
  ];

  const MACHINES = [
    ["instance_state", ["created", "starting", "running", "stopped", "failed", "墓碑"],
      "启动只在 created / stopped 时可点；失败后要先停止再重来。"],
    ["connection_state", ["disconnected", "connected", "registering", "registered", "reporting", "ready"],
      "ready 之前送话都会被拒；keepalive 必须明显短于服务端约 60s 的踢线阈值。"],
    ["一个 turn 的生命周期", ["speak 受理", "上行推包", "asr_result", "photo_command", "photo_uploaded", "tts_chunk ×N", "turn_terminal"],
      "拍照路径是指令 → 传图 → 图片分析的语音回复；没开拍照或没配图则 photo_skipped。带图送话则是 speak 受理 → photo_uploaded → 上行推包 → tts_chunk ×N → turn_terminal。"],
  ];

  function helpDrawerHTML() {
    return `<div class="sheet">
      <div class="grp">
        <span class="grp__title">术语</span>
        ${GLOSSARY.map(([k, v]) => `<div class="gloss"><span class="gloss__k">${esc(k)}</span><span class="gloss__v">${esc(v)}</span></div>`).join("")}
      </div>
      <div class="grp">
        <span class="grp__title">状态机</span>
        ${MACHINES.map(([name, steps, note]) => `<div class="mach">
          <span class="mach__name">${esc(name)}</span>
          <div class="mach__steps">${steps.map((s, i) =>
            `<span class="${i === steps.length - 1 ? "tag tag--acc" : "tag"}">${esc(s)}</span>`).join("")}</div>
          <div class="mach__note">${esc(note)}</div>
        </div>`).join("")}
      </div>
    </div>`;
  }

  // 场景用后端真支持的三种 action 组：batch_start / speak / assert。
  function scenarioSpecs() {
    const ids = state.checked.size ? [...state.checked] : (state.selectedId ? [state.selectedId] : []);
    const asset = state.srcKey.startsWith("asset:") ? state.srcKey.slice(6) : "";
    return [
      {
        name: `并发冷启动（${ids.length} 台）`,
        steps: `batch_start ${ids.length} 台 · wait_ready`,
        ok: ids.length > 0 && !!state.attachProduct,
        why: ids.length ? "先在启动条选产品" : "先在名册里勾几台，或选中一台设备",
        spec: { name: "batch-start", steps: [{ action: "batch_start", device_ids: ids, wait_ready: true, ...attachRefs(), product: state.attachProduct }] },
      },
      {
        name: "单轮语音回归",
        steps: `batch_start(1) → speak{asset_id} → wait`,
        ok: !!(state.selectedId && asset && state.attachProduct),
        why: !state.selectedId || !asset ? "先选中一台设备，并在送话条里选一条素材库音频" : "先在启动条选产品",
        spec: {
          name: "one-turn",
          steps: [
            { action: "batch_start", device_ids: [state.selectedId], wait_ready: true, ...attachRefs(), product: state.attachProduct },
            { action: "speak", device_id: state.selectedId, asset_id: asset, wait: true },
          ],
        },
      },
      {
        name: "只等就绪",
        steps: `assert{ready} · 用 after_event_seq 断言`,
        ok: !!(state.selectedId && state.instanceId),
        why: "先选中一台已启动的设备",
        spec: {
          name: "wait-ready",
          steps: [{
            action: "wait", device_id: state.selectedId, instance_id: state.instanceId,
            event_type: "ready", after_event_seq: 0,
          }],
        },
      },
    ];
  }

  function scenariosDrawerHTML() {
    return `<div class="sheet sheet--tight">
      <p class="hint">把一串动作写成脚本跑：启动一组设备、等它们都 Ready、并发送话、等终态。运行中可以在右栏「全局」页签看跨设备事件。后端支持的动作只有 batch_start / speak / assert。</p>
      ${scenarioSpecs().map((s, i) => `<div class="row">
        <div class="row__top">
          <span style="font-size:13px;color:var(--fg)">${esc(s.name)}</span>
          <span class="grow"></span>
          <button type="button" class="btn btn--primary btn--sm" data-scenario="${i}"${s.ok ? "" : " disabled"}>运行</button>
        </div>
        <div class="row__meta"><span>${esc(s.steps)}</span></div>
        ${s.ok ? "" : `<div class="row__note">${esc(s.why)}</div>`}
      </div>`).join("")}
      <div class="row"><div class="row__top"><span style="font-size:12px">批量等待条件</span></div>
        <div class="row__meta"><span>POST /wait · 等一组设备同时满足条件后再继续</span></div></div>
    </div>`;
  }

  // 每次运行一行。instance_id 是每进程新生成的，manager 一重启同一台设备就换一个，
  // 所以「历史」天然按运行分段；本次运行标 live，盘上的旧运行标可回看。
  function runsDrawerHTML() {
    const rows = state.instances;
    return `<div class="sheet sheet--tight">
      <p class="hint">GET /devices/{id}/instances · manager 每次启动都给设备一个新 instance_id，录音与事件按 instance 分区。旧运行只读回看，不能再送话。</p>
      ${rows.length ? rows.map((r) => {
        const cur = r.instance_id === state.instanceId;
        const live = r.source === "live";
        const span = [fmtTime(r.started_at), r.turns ? `${r.turns} 轮` : "还没有轮次"].filter(Boolean).join(" · ");
        return `<div class="row row--flat">
          <span class="${live ? "tag tag--acc" : "tag tag--mute"}">${live ? "本次运行" : r.source === "tomb" ? "墓碑" : "历史"}</span>
          ${r.product ? `<span class="tag tag--acc">${esc(r.product)}</span>` : ""}
          <span class="mono" style="font-size:12px" title="${esc(r.instance_id)}">${esc(shortId(r.instance_id))}</span>
          <span class="dim" style="font-size:12px">${esc(span)}</span>
          <span class="grow"></span>
          ${cur ? `<span class="dim" style="font-size:12px">正在看</span>` : `<button type="button" class="btn btn--sm" data-run-open="${esc(r.instance_id)}" data-run-src="${esc(r.source)}">回看</button>`}
          ${live ? "" : `<button type="button" class="btn btn--danger btn--sm" data-run-del="${esc(r.instance_id)}">删除</button>`}
        </div>`;
      }).join("") : `<p class="blank--drawer">这台设备还没有留下任何运行记录。启动并送一段音频后再来看。</p>`}
    </div>`;
  }

  async function loadInstances() {
    if (!state.selectedId) return;
    try {
      const data = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/instances`);
      state.instances = data.instances || [];
    } catch {
      state.instances = [];
    }
  }

  async function openRuns() {
    await loadInstances();
    openDrawer("runs");
  }

  async function delRun(ins) {
    try {
      await api("DELETE", `/devices/${encodeURIComponent(state.selectedId)}/instances/${encodeURIComponent(ins)}`);
      flash("已删除这次运行的录音与事件", "ok");
      await loadInstances();
      renderDrawer();
    } catch (err) {
      apiErr(err);
    }
  }

  const FAULTS = [
    ["skip_register", "跳过上线注册报文，验证服务端对未注册连接的处理"],
    ["skip_report", "跳过 report 上报，验证服务端的 ready 判定与超时"],
    ["bad_seq", "发一个错误的 sequence_number，验证序号校验分支"],
    ["oversize", "发一个超过 max_payload_size 的包，验证服务端限长"],
    ["bad_header", "发一个坏协议头，验证 protocol_error 处理"],
    ["dup_uuid", "复用上一轮的 uplink uuid，验证服务端去重"],
    ["bad_stage", "发一个错误的 stage 值，验证阶段机校验"],
  ];

  function faultsDrawerHTML() {
    const st = (state.live && state.live.instance_state) || "";
    const can = st === "created" || st === "stopped";
    const cur = (state.live && state.live.fault) || "";
    return `<div class="sheet sheet--tight">
      <p class="hint">POST /devices/{id}/faults · 往这台设备的连接上注入一个故障，用来验证服务端的异常分支。</p>
      ${cur ? `<p class="warnbar">已挂：<b class="mono">${esc(cur)}</b> ——这台设备的行为现在偏离正常，点下面「清除故障」恢复。</p>` : ""}
      ${can ? "" : `<p class="warnbar">当前 instance_state 是 ${esc(st || "—")}，仅 created / stopped 可设 fault，先停止设备。</p>`}
      ${FAULTS.map(([k, zh]) => `<div class="row row--flat">
        <div class="grow" style="display:flex;flex-direction:column;gap:5px">
          <span class="mono" style="font-size:11.5px;color:var(--fg)">${esc(k)}</span>
          <span class="fld__note">${esc(zh)}</span>
        </div>
        <button type="button" class="btn btn--sub btn--sm" data-fault="${esc(k)}"${can ? "" : " disabled"}>注入</button>
      </div>`).join("")}
      <div class="row row--flat">
        <div class="grow"><span class="mono" style="font-size:11.5px;color:var(--fg)">（清除）</span></div>
        <button type="button" class="btn btn--ghost" data-fault=""${can ? "" : " disabled"}>清除故障</button>
      </div>
    </div>`;
  }

  function renderDrawer() {
    const body = $("drawer-body");
    const w = state.drawer;
    if (!w) return;
    if (w === "new") setHTML(body, newDrawerHTML());
    else if (w === "config") setHTML(body, configDrawerHTML());
    else if (w === "assets") setHTML(body, assetsDrawerHTML());
    else if (w === "help") setHTML(body, helpDrawerHTML());
    else if (w === "scenarios") setHTML(body, scenariosDrawerHTML());
    else if (w === "faults") setHTML(body, faultsDrawerHTML());
    else if (w === "runs") setHTML(body, runsDrawerHTML());
    else if (w === "sheet") {
      // 窄屏：右栏收进抽屉，直接把事件带的 DOM 搬过来，不做第二套渲染。
      if (body.firstElementChild !== $("side")) {
        body.innerHTML = "";
        body.appendChild($("side"));
        $("side").style.display = "flex";
      }
    }
  }

  // ——— 抽屉里的动作 ———

  function formRoot(root) {
    return root || $("drawer-body");
  }

  function formVal(name, root) {
    const el = formRoot(root).querySelector(`[name="${CSS.escape(name)}"]`);
    if (!el) return "";
    return el.type === "checkbox" ? el.checked : el.value;
  }

  function formChecked(name, root) {
    return [...formRoot(root).querySelectorAll(`[name="${CSS.escape(name)}"]:checked`)].map((el) => el.value);
  }

  function origAt(obj, path) {
    return path.split(".").reduce((o, k) => (o == null ? o : o[k]), obj);
  }

  function nestSet(obj, path, val) {
    const parts = path.split(".");
    let cur = obj;
    for (let i = 0; i < parts.length - 1; i++) {
      if (!cur[parts[i]] || typeof cur[parts[i]] !== "object") cur[parts[i]] = {};
      cur = cur[parts[i]];
    }
    cur[parts[parts.length - 1]] = val;
  }

  async function createFromDrawer() {
    try {
      if (state.newMode === "single") {
        const id = String(formVal("device_id") || "").trim();
        if (!id) {
          flash("device_id 不能空", "err");
          return;
        }
        await api("POST", "/devices", { device_id: id });
        flash("POST /devices · 已创建 " + id, "ok");
        closeDrawer();
        await refreshList();
        await selectDevice(id);
      } else {
        const id_prefix = String(formVal("id_prefix") || "").trim();
        const count = Number(formVal("count")) || 0;
        if (!id_prefix) {
          flash("前缀不能空", "err");
          return;
        }
        if (count <= 0) {
          flash("数量必须 > 0", "err");
          return;
        }
        const data = await api("POST", "/devices", { id_prefix, count });
        flash("已创建 " + (data.device_ids || []).join(", "), "ok");
        closeDrawer();
        await refreshList();
        if (data.device_ids && data.device_ids[0]) await selectDevice(data.device_ids[0]);
      }
    } catch (err) {
      apiErr(err);
    }
  }

  const CONFIG_FIELDS = [
    { name: "firmware_version" },
    { name: "nic_type" },
    { name: "nic_iccid" },
    { name: "playing_mode", num: true },
    { name: "audio.format" },
    { name: "audio.sample_rate", num: true },
    { name: "audio.bitrate_kbps", num: true },
    { name: "audio.slice_ms", num: true },
    { name: "audio.max_payload_size", num: true },
    { name: "uuid.min", num: true },
    { name: "uuid.max", num: true },
    { name: "behavior.keepalive_interval_sec", num: true },
    { name: "behavior.first_reply_timeout_sec", num: true },
    { name: "behavior.speak_backlog_depth", num: true },
    { name: "behavior.silence_probe", check: true },
    { name: "behavior.interrupt_on_disconnect", check: true },
    { name: "recording.enable_frame_log", check: true, live: true },
    { name: "recording.save_uplink_audio", check: true, live: true },
    { name: "recording.save_downlink_audio", check: true, live: true },
    { name: "recording.output_dir", live: true },
    { name: "features.photo.enabled", check: true, live: true },
    { name: "features.photo.image", live: true },
    { name: "features.photo.server_default_reply", check: true, live: true },
    { name: "features.photo.slice_interval_ms", num: true, live: true },
    { name: "features.photo.reply_timeout_sec", num: true, live: true },
  ];

  function sameVal(a, b, f) {
    if (f.check) return !!a === !!b;
    if (f.num) return Number(a) === Number(b);
    return String(a ?? "") === String(b ?? "");
  }

  function readConfigDrawer() {
    const runningLock = !identityEditable();
    const out = {};
    for (const f of CONFIG_FIELDS) {
      if (runningLock && !f.live) continue;
      const raw = formVal(f.name);
      const val = f.check ? !!raw : f.num ? Number(raw) : raw;
      if (sameVal(val, origAt(state.config, f.name), f)) continue;
      nestSet(out, f.name, val);
    }
    return out;
  }

  async function saveConfig(ev) {
    if (ev) ev.preventDefault();
    if (!state.selectedId || state.tombstone) return;
    const patch = readConfigDrawer();
    if (!Object.keys(patch).length) {
      flash("没有改动", "info");
      return;
    }
    try {
      state.config = await api("PUT", `/devices/${encodeURIComponent(state.selectedId)}/config`, patch);
      flash("PUT /devices/" + state.selectedId + "/config · 已保存", "ok");
      renderDrawer();
      await refreshList();
    } catch (err) {
      apiErr(err);
    }
  }

  async function reportMode() {
    if (!state.selectedId) return;
    const mode = Number(formVal("playing_mode")) || 1;
    try {
      const res = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/report`, { playingMode: mode });
      flash("report seq " + res.sequence_number, "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  const TREE_PATH = {
    env: () => "/registry/environments",
    ent: () => `/registry/environments/${encodeURIComponent(state.regSel.env)}/enterprises`,
    typ: () => `/registry/environments/${encodeURIComponent(state.regSel.env)}/enterprises/${encodeURIComponent(state.regSel.ent)}/device_types`,
  };

  function treeNodePath(k) {
    const s = state.regSel;
    if (k === "env") return `/registry/environments/${encodeURIComponent(s.env)}`;
    if (k === "ent") return `${TREE_PATH.ent()}/${encodeURIComponent(s.ent)}`;
    return `${TREE_PATH.typ()}/${encodeURIComponent(s.typ)}`;
  }

  async function treeAdd(k) {
    const body = $("registry-body");
    const v1 = (body.querySelector('[data-add="1"]') || {}).value || "";
    const v2 = (body.querySelector('[data-add="2"]') || {}).value || "";
    const a = v1.trim();
    const b = v2.trim();
    if (!a) {
      flash(k === "env" ? "环境名不能空" : "名称不能空", "err");
      return;
    }
    if (k !== "env" && !b) {
      flash("简称不能空 —— 上线报文里发出去的是简称", "err");
      return;
    }
    try {
      if (k === "env") {
        await api("POST", TREE_PATH.env(), { name: a, url: b || "ws://127.0.0.1:8089/{enterprise}" });
        state.regSel = { env: a, ent: "", typ: "" };
      } else if (k === "ent") {
        await api("POST", TREE_PATH.ent(), { name: a, short_name: b });
        state.regSel.ent = b;
        state.regSel.typ = "";
      } else {
        const payload = { name: a, short_name: b };
        const dp = ((body.querySelector('[data-add="product"]') || {}).value || "").trim();
        if (dp) payload.default_product = dp;
        await api("POST", TREE_PATH.typ(), payload);
        state.regSel.typ = b;
        state.attachProduct = dp || state.attachProduct;
      }
      state.adding = null;
      await loadRegistry();
      flash("已新增 " + a, "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  // 环境改 url（环境名是主键，改不了）；厂商/设备类型改名称与简称。
  // 简称原本也是禁改的——Phase 11 之前它焊在每条设备定义里。现在设备册不引用
  // 配置树了，改简称只动树本身，正在跑的实例下次 start 才用新值。
  // 行内表单而不是 prompt()：预览窗格和一些内嵌 webview 直接把 prompt/confirm 抛错。
  async function treeSave(k) {
    const body = $("registry-body");
    const v = ((body.querySelector('[data-edit="1"]') || {}).value || "").trim();
    if (!v) {
      flash(k === "env" ? "url 不能空" : "名称不能空", "err");
      return;
    }
    let payload = { url: v };
    let okMsg = "已改 url " + v;
    if (k !== "env") {
      const short = ((body.querySelector('[data-edit="2"]') || {}).value || "").trim();
      if (!short) {
        flash("简称不能空——上线报文里发出去的是它", "err");
        return;
      }
      payload = { name: v, short_name: short };
      if (k === "typ") {
        payload.default_product = ((body.querySelector('[data-edit="product"]') || {}).value || "");
      }
      const was = { ent: state.regSel.ent, typ: state.regSel.typ }[k];
      const wasName = k === "ent"
        ? ((regEnt() || {}).name || "")
        : ((regTypes().find((x) => x.short_name === state.regSel.typ) || {}).name || "");
      okMsg = (v === wasName && short === was)
        ? "已保存 " + v
        : (short === was ? "已改名 " + v : `已改成 ${v}（${was} → ${short}）`);
    }
    try {
      const res = await api("PUT", treeNodePath(k), payload);
      // 简称是选中项的键，改了要把选中态迁过去，否则右边整列会空掉。
      if (k === "ent" && res.short_name) { state.regSel.ent = res.short_name; state.regSel.typ = ""; }
      if (k === "typ" && res.short_name) state.regSel.typ = res.short_name;
      if (k === "typ") state.attachProduct = payload.default_product || "";
      state.regEdit = "";
      await loadRegistry();
      flash(okMsg, "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  // 两击确认（armThen），同样是为了不依赖 confirm()。
  async function treeDelete(k) {
    const cur = { env: state.regSel.env, ent: state.regSel.ent, typ: state.regSel.typ }[k];
    try {
      await api("DELETE", treeNodePath(k));
      if (k === "env") state.regSel = { env: "", ent: "", typ: "" };
      else if (k === "ent") state.regSel.ent = state.regSel.typ = "";
      else state.regSel.typ = "";
      await loadRegistry();
      flash("已删除 " + cur, "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  async function assetAction(act, id) {
    const body = $("drawer-body");
    const asset = state.lib.rows.find((a) => a.asset_id === id);
    if (act === "play") {
      playHref(`/assets/${encodeURIComponent(id)}/content?decode=1`, {
        label: "试听 · " + (asset ? asset.name : id),
        dlHref: `/assets/${encodeURIComponent(id)}/content`,
        download: (asset ? asset.name : id),
      });
      return;
    }
    if (act === "edit") {
      state.lib.editingId = id;
      state.lib.armedId = null;
      renderDrawer();
      return;
    }
    if (act === "cancel") {
      state.lib.editingId = null;
      renderDrawer();
      return;
    }
    if (act === "save") {
      const row = body.querySelector(`[data-id="${CSS.escape(id)}"]`);
      try {
        await api("PATCH", `/assets/${encodeURIComponent(id)}`, {
          name: row.querySelector('[data-edit="name"]').value.trim(),
          language: row.querySelector('[data-edit="language"]').value.trim(),
          tags: row.querySelector('[data-edit="tags"]').value,
        });
        state.lib.editingId = null;
        flash("已保存", "ok");
        await loadLibrary();
      } catch (err) {
        apiErr(err);
      }
      return;
    }
    if (act === "del") {
      // 两击确认，与设备删除同款交互。
      if (state.lib.armedId !== id) {
        state.lib.armedId = id;
        renderDrawer();
        setTimeout(() => {
          if (state.lib.armedId === id) {
            state.lib.armedId = null;
            if (state.drawer === "assets") renderDrawer();
          }
        }, 2500);
        return;
      }
      state.lib.armedId = null;
      try {
        await api("DELETE", `/assets/${encodeURIComponent(id)}`);
        flash("DELETE /assets/" + id, "ok");
        await loadLibrary();
      } catch (err) {
        apiErr(err);
        renderDrawer();
      }
    }
  }

  async function runScenario(i) {
    const s = scenarioSpecs()[i];
    if (!s || !s.ok) return;
    try {
      const res = await api("POST", "/scenarios/run", s.spec);
      flash(`POST /scenarios/run · ${s.name} 已下发 run_id=${res.run_id}`, "ok");
      closeDrawer();
      showSide("global");
      pollScenario(res.run_id);
    } catch (err) {
      apiErr(err);
    }
  }

  function pollScenario(runId) {
    let n = 0;
    const tick = async () => {
      n++;
      try {
        const run = await api("GET", `/scenarios/runs/${encodeURIComponent(runId)}`);
        if (run.status !== "running") {
          const bad = (run.steps || []).filter((x) => x.error);
          flash(`场景 ${runId} · ${run.status}` + (bad.length ? ` · ${bad.length} 步出错：${bad[0].error}` : ""),
            bad.length ? "err" : "ok");
          refreshList().catch(() => {});
          return;
        }
      } catch {
        return;
      }
      if (n < 120) setTimeout(tick, 1000);
    };
    setTimeout(tick, 1000);
  }

  async function injectFault(f) {
    if (!state.selectedId) return;
    try {
      await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/faults`, { fault: f });
      flash(`POST /devices/${state.selectedId}/faults · ${f || "已清除"}`, "info");
      closeDrawer();
    } catch (err) {
      apiErr(err);
    }
  }

  // ——— 右栏页签 ———

  function showSide(which) {
    state.sideTab = which;
    show($("panel-tape"), which === "tape");
    show($("panel-turns"), which === "turns");
    show($("panel-global"), which === "global");
    for (const [id, k] of [["tab-tape", "tape"], ["tab-turns", "turns"], ["tab-global", "global"]]) {
      $(id).classList.toggle("is-on", which === k);
      $(id).setAttribute("aria-selected", String(which === k));
    }
    if (which === "tape" && state.tapeFollow) {
      const ol = $("tape");
      ol.scrollTop = ol.scrollHeight;
    }
    if (which === "global") {
      connectGlobalWS();
      renderGlobalTape();
    }
  }

  // ——— 全局事件总线 ———
  // 与设备事件带独立：一条 WS 看所有设备。切到页签才连；连接保持，
  // 断线后下次切回用 after_global_seq 续传，不重复拉全量。

  function connectGlobalWS() {
    if (state.globalWs && state.globalWs.readyState <= 1) return;
    const q = state.globalNewest > 0 ? `?after_global_seq=${state.globalNewest}` : "";
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/ws/events/global${q}`);
    state.globalWs = ws;
    ws.onmessage = (e) => {
      let ev;
      try { ev = JSON.parse(e.data); } catch { return; }
      if (!ev || typeof ev.global_seq !== "number" || ev.global_seq <= state.globalNewest) return;
      state.globalNewest = ev.global_seq;
      state.globalEvents.push(ev);
      if (state.globalEvents.length > GLOBAL_MAX) {
        state.globalEvents.splice(0, state.globalEvents.length - GLOBAL_MAX);
      }
      paint("global");
    };
    ws.onclose = () => {
      if (state.globalWs === ws) state.globalWs = null;
    };
    ws.onerror = () => { /* onclose 收尾；410 过期由后端拒绝升级 */ };
  }

  function renderGlobalTape() {
    const ol = $("global-tape");
    setNum($("global-count"), state.globalEvents.length);
    if (ol.hidden || $("panel-global").hidden) return;
    if (!state.globalEvents.length) {
      setHTML(ol, `<li class="ev__blank">还没有全局事件。任意设备的动作都会汇到这里。</li>`);
      return;
    }
    const html = state.globalEvents.map((ev) => {
      const extra = [ev.turn_id, ev.reply_kind, ev.turn_end_reason, ev.reason].filter(Boolean).join(" · ");
      return `<li class="ev">
        <div class="ev__top">
          <span class="ev__t">#${esc(ev.global_seq)}</span>
          <span class="ev__type t-${esc(ev.event_type)}">${esc(ev.event_type)}</span>
        </div>
        <div class="ev__dev">${esc(ev.device_id || "")}${extra ? " · " + esc(extra) : ""}</div>
      </li>`;
    }).join("");
    if (setHTML(ol, html)) ol.scrollTop = ol.scrollHeight;
  }

  // ——— 调音台皮肤：灯位桥 + 连续时间轴 ———
  // 道内一律不放文字；详情挂 title，文字信息全在下方 side 面板。

  const TL_WIN_MS = 10 * 60 * 1000; // 时间轴最多回看 10 分钟
  const TL_DOTS_SKIP = new Set(["tts_chunk", "uplink_frames", "downlink_frames"]);

  function deckModel() {
    const now = Date.now();
    let tMin = Infinity;
    const firstEv = new Map(), lastEv = new Map();
    for (const ev of state.events) {
      const t = Date.parse(ev.ts);
      if (!isFinite(t)) continue;
      if (ev.turn_id) {
        if (!firstEv.has(ev.turn_id)) firstEv.set(ev.turn_id, t);
        lastEv.set(ev.turn_id, t);
      }
    }
    const ups = [], downs = [], bands = [];
    for (const t of convTurns()) {
      const f = state.framesByTurn[t.id] || { up: [], down: [] };
      const t0 = firstEv.get(t.id);
      const live = state.occupiedTurnId === t.id;
      if (t0) bands.push({ t0, t1: t.done ? (lastEv.get(t.id) || t0) : now, live, id: t.id });
      if (f.up.length) {
        ups.push({ t0: Date.parse(f.up[0].ts), t1: Date.parse(f.up[f.up.length - 1].ts), id: t.id, live: t.phase === "up", tip: `${t.id} · 上行 ${f.up.length} 包 · ${fmtBytes(sumBytes(f.up))}` });
      } else if (live && t0) {
        ups.push({ t0, t1: now, id: t.id, live: true, tip: t.id + " · 上行（等帧日志）" });
      }
      if (f.down.length) {
        downs.push({ t0: Date.parse(f.down[0].ts), t1: Date.parse(f.down[f.down.length - 1].ts), id: t.id, live: live && !t.done, tip: `${t.id} · 下行 ${f.down.length} 包 · ${fmtBytes(sumBytes(f.down))}` });
      }
    }
    const dots = [];
    for (const ev of state.events) {
      const t = Date.parse(ev.ts);
      if (!isFinite(t) || TL_DOTS_SKIP.has(ev.event_type)) continue;
      dots.push({ t, err: isErrEvent(ev), tip: `${fmtClock(ev.ts)} ${ev.event_type}${ev.turn_id ? " · " + ev.turn_id : ""}${ev.reason ? " · " + ev.reason : ""}` });
    }
    for (const s of [...ups, ...downs, ...bands, ...dots.map((x) => ({ t0: x.t }))]) {
      if (s.t0 < tMin) tMin = s.t0;
    }
    if (!isFinite(tMin)) tMin = now - 60000;
    if (now - tMin > TL_WIN_MS) tMin = now - TL_WIN_MS;
    const tMax = now + Math.max(2000, (now - tMin) * 0.03);
    // 下行静默预算区：上一包下行 → +downlink_idle_timeout_sec
    let sil = null;
    if (state.occupiedTurnId) {
      let lastDown = 0;
      for (let i = state.events.length - 1; i >= 0; i--) {
        const ev = state.events[i];
        if (ev.turn_id !== state.occupiedTurnId) continue;
        if (ev.event_type === "tts_chunk" || ev.event_type === "tts_done") { lastDown = Date.parse(ev.ts); break; }
      }
      const budget = Number(((state.config || {}).behavior || {}).downlink_idle_timeout_sec) || 0;
      if (lastDown && budget) sil = { t0: lastDown, t1: lastDown + budget * 1000 };
    }
    return { tMin, tMax, ups, downs, bands, dots, sil, now };
  }

  function tlTicks(tMin, tMax) {
    const span = tMax - tMin;
    const steps = [2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600].map((s) => s * 1000);
    let step = steps[steps.length - 1];
    for (const s of steps) { if (span / s <= 9) { step = s; break; } }
    const ticks = [];
    for (let t = Math.ceil(tMin / step) * step; t <= tMax; t += step) ticks.push(t);
    return ticks;
  }

  function renderDeck() {
    const deck = $("deck");
    if (!deck || document.documentElement.dataset.skin !== "console") return;
    const live = state.live || {};
    const cur = convTurns().find((t) => t.id === state.occupiedTurnId);
    const conn = live.connection_state || "—";
    const lamps = [
      ["CONN", conn === "ready" || conn === "registered" || conn === "reporting", conn],
      ["READY", live.instance_state === "running" && conn === "ready", live.instance_state || "—"],
      ["UP", !!(cur && !cur.done && cur.phase === "up"), cur ? `${cur.up} 包` : ""],
      ["DOWN", !!(cur && !cur.done && cur.down > 0), cur ? `${cur.down} 包` : ""],
      ["FAULT", !!live.fault, live.fault || ""],
    ];
    setHTML($("deck-bridge"),
      lamps.map(([n, on, v]) =>
        `<span class="lamp${on ? " is-on" : ""}${n === "FAULT" && on ? " is-err" : ""}" title="${esc(n + " " + v)}"><i></i>${n}</span>`).join("") +
      `<span class="deck__meta mono">${esc(state.selectedId || "—")} · gen ${esc(state.connGeneration ?? live.conn_generation ?? "—")} · turns ${convTurns().length}${state.occupiedTurnId ? " · " + esc(shortTurn(state.occupiedTurnId)) + esc(idleNoteFor(state.occupiedTurnId)) : ""}</span>`);

    const empty = $("deck-empty");
    const d = deckModel();
    const span = d.tMax - d.tMin;
    const pc = (t) => Math.max(0, Math.min(100, ((t - d.tMin) / span) * 100));
    const has = d.ups.length || d.downs.length || d.dots.length || d.bands.length;
    show(empty, !has);
    setText(empty, state.selectedId
      ? "还没有活动。送出一段音频后，上行/下行会按真实帧时间铺在这里。"
      : "从左边通道选一台设备。");
    if (!has) { setHTML($("deck-inner"), ""); return; }

    const seg = (s, cls) =>
      `<i class="seg ${cls}${s.live ? " is-live" : ""}" style="left:${pc(s.t0)}%;width:${Math.max(0.5, pc(s.t1) - pc(s.t0))}%" title="${esc(s.tip)}"></i>`;
    const ticks = tlTicks(d.tMin, d.tMax);
    const rulerHtml = ticks.map((t) =>
      `<i class="tick" style="left:${pc(t)}%"><b></b>${new Date(t).toTimeString().slice(0, 8)}</i>`).join("");
    const grid = ticks.map((t) => `<i class="gl" style="left:${pc(t)}%"></i>`).join("");
    setHTML($("deck-inner"), `
      <div class="tlr tlr--top">${rulerHtml}</div>
      <div class="tla">
        ${grid}
        ${d.bands.map((b) => `<i class="band${b.live ? " is-live" : ""}" style="left:${pc(b.t0)}%;width:${Math.max(0.5, pc(b.t1) - pc(b.t0))}%" title="${esc(b.id)}"></i>`).join("")}
        <div class="lane lane--up">${d.ups.map((s) => seg(s, "seg--up")).join("")}</div>
        <div class="lane lane--down">
          ${d.downs.map((s) => seg(s, "seg--down")).join("")}
          ${d.sil ? `<i class="sil" style="left:${pc(d.sil.t0)}%;width:${Math.max(0.4, pc(Math.min(d.sil.t1, d.now)) - pc(d.sil.t0))}%" title="下行静默收尾窗口"></i>` : ""}
        </div>
        <div class="lane lane--dots">${d.dots.map((x) => `<i class="dot${x.err ? " is-err" : ""}" style="left:${pc(x.t)}%" title="${esc(x.tip)}"></i>`).join("")}</div>
        <i class="nowl" style="left:${pc(d.now)}%"></i>
      </div>
      <div class="tlr tlr--bot">${rulerHtml}</div>`);
  }

  // ——— 麦克风录制 → WAV → 音源 ———

  function wavFromBlob(blob) {
    return new Promise((resolve, reject) => {
      const rd = new FileReader();
      rd.onerror = () => reject(new Error("录音读取失败"));
      rd.onload = () => {
        const ac = new (window.AudioContext || window.webkitAudioContext)();
        ac.decodeAudioData(rd.result).then((buf) => {
          const sr = buf.sampleRate;
          const nCh = Math.min(buf.numberOfChannels, 2);
          const len = buf.length * nCh;
          const out = new DataView(new ArrayBuffer(44 + len * 2));
          const wstr = (o, s) => { for (let i = 0; i < s.length; i++) out.setUint8(o + i, s.charCodeAt(i)); };
          wstr(0, "RIFF"); out.setUint32(4, 36 + len * 2, true); wstr(8, "WAVE");
          wstr(12, "fmt "); out.setUint32(16, 16, true); out.setUint16(20, 1, true);
          out.setUint16(22, nCh, true); out.setUint32(24, sr, true);
          out.setUint32(28, sr * nCh * 2, true); out.setUint16(32, nCh * 2, true); out.setUint16(34, 16, true);
          wstr(36, "data"); out.setUint32(40, len * 2, true);
          let off = 44;
          const chans = [];
          for (let c = 0; c < nCh; c++) chans.push(buf.getChannelData(c));
          for (let i = 0; i < buf.length; i++) {
            for (let c = 0; c < nCh; c++) {
              const v = Math.max(-1, Math.min(1, chans[c][i]));
              out.setInt16(off, v < 0 ? v * 0x8000 : v * 0x7fff, true);
              off += 2;
            }
          }
          ac.close();
          resolve(new File([out.buffer], state.rec.name || "mic.wav", { type: "audio/wav" }));
        }, () => { ac.close(); reject(new Error("录音解码失败")); });
      };
      rd.readAsArrayBuffer(blob);
    });
  }

  async function toggleRec() {
    const btn = $("btn-rec");
    if (state.rec.mr) {
      state.rec.mr.stop(); // onstop 里收尾
      return;
    }
    if (!navigator.mediaDevices || !window.MediaRecorder) {
      flash("这个浏览器不支持录音", "err");
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      const mr = new MediaRecorder(stream);
      state.rec = { mr, stream, chunks: [], file: state.rec.file, name: state.rec.name };
      mr.ondataavailable = (e) => { if (e.data && e.data.size) state.rec.chunks.push(e.data); };
      mr.onstop = async () => {
        state.rec.stream.getTracks().forEach((t) => t.stop());
        state.rec.mr = null;
        state.rec.stream = null;
        btn.classList.remove("is-on");
        btn.setAttribute("aria-pressed", "false");
        setText(btn, "● 录制");
        try {
          const blob = new Blob(state.rec.chunks, { type: mr.mimeType || "audio/webm" });
          state.rec.name = "mic-" + new Date().toTimeString().slice(0, 8).replace(/:/g, "") + ".wav";
          state.rec.file = await wavFromBlob(blob);
          state.srcKey = "rec";
          flash("录音完成 " + state.rec.name + " · 已选为音源", "ok");
        } catch (err) {
          flash(err.message, "err");
        }
        renderSrc();
        renderStage();
      };
      mr.start();
      btn.classList.add("is-on");
      btn.setAttribute("aria-pressed", "true");
      setText(btn, "■ 停止录制");
      flash("录音中…再点一次结束", "info");
    } catch (err) {
      flash("麦克风不可用：" + err.message, "err");
    }
  }

  // ——— 主题 ———

  const THEMES = ["system", "light", "dark"];
  const THEME_GLYPH = { system: "◐", light: "☀", dark: "☾" };
  const THEME_LABEL = { system: "跟随系统", light: "浅色", dark: "深色" };

  function readTheme() {
    try {
      const t = localStorage.getItem("bench.theme");
      return THEMES.includes(t) ? t : "system";
    } catch {
      return "system";
    }
  }

  function applyTheme(t) {
    if (t === "system") delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = t;
    setText($("theme-glyph"), THEME_GLYPH[t]);
    $("btn-theme").title = "主题：" + THEME_LABEL[t];
    try { localStorage.setItem("bench.theme", t); } catch { /* 隐私模式下忽略 */ }
  }

  function cycleTheme() {
    const cur = readTheme();
    applyTheme(THEMES[(THEMES.indexOf(cur) + 1) % THEMES.length]);
  }

  // ——— 布局皮肤：经典 / 调音台 ———

  function readSkin() {
    try {
      const s = localStorage.getItem("bench.skin");
      return s === "console" ? "console" : "classic";
    } catch {
      return "classic";
    }
  }

  function applySkin(s) {
    if (s === "console") document.documentElement.dataset.skin = "console";
    else delete document.documentElement.dataset.skin;
    const con = s === "console";
    $("btn-skin-classic").classList.toggle("is-on", !con);
    $("btn-skin-console").classList.toggle("is-on", con);
    $("btn-skin-classic").setAttribute("aria-pressed", String(!con));
    $("btn-skin-console").setAttribute("aria-pressed", String(con));
    $("deck").hidden = !con;
    try { localStorage.setItem("bench.skin", s); } catch { /* 隐私模式下忽略 */ }
    renderDeck();
  }

  // ——— 绑定 ———

  function setScope(scope) {
    state.tapeScope = scope;
    for (const c of $("tape-chips").querySelectorAll(".chip")) {
      c.classList.toggle("is-on", c.dataset.scope === scope);
    }
    state.tapeRows = [];
    renderTape();
  }

  function setMode(m) {
    state.convMode = m;
    $("btn-mode-bubble").classList.toggle("is-on", m === "bubble");
    $("btn-mode-cards").classList.toggle("is-on", m === "cards");
    $("btn-mode-bubble").setAttribute("aria-pressed", String(m === "bubble"));
    $("btn-mode-cards").setAttribute("aria-pressed", String(m === "cards"));
    state.convSigs = [];
    renderConv();
  }

  function debounce(fn, ms) {
    let t = 0;
    return (...a) => {
      clearTimeout(t);
      t = setTimeout(() => fn(...a), ms);
    };
  }

  function bindShell() {
    $("btn-refresh").addEventListener("click", () => refreshList().catch(apiErr));
    $("btn-theme").addEventListener("click", cycleTheme);
    $("btn-skin-classic").addEventListener("click", () => applySkin("classic"));
    $("btn-skin-console").addEventListener("click", () => applySkin("console"));
    $("btn-mode-bubble").addEventListener("click", () => setMode("bubble"));
    $("btn-mode-cards").addEventListener("click", () => setMode("cards"));
    $("btn-help").addEventListener("click", () => openDrawer("help"));
    $("btn-scenarios").addEventListener("click", () => openDrawer("scenarios"));
    $("btn-new").addEventListener("click", () => openDrawer("new"));
    $("btn-assets").addEventListener("click", () => openDrawer("assets"));
    // 打开前重拉一次：overridden 与各字段可能被别处改过（agent、另一个页签、
    // 或本页的重置），拿选中设备时的缓存会让「重置为产品默认」按钮状态不对。
    $("btn-config").addEventListener("click", async () => {
      openDrawer("config");
      if (!state.selectedId || state.tombstone) return;
      try {
        state.config = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/config`);
        if (state.drawer === "config") renderDrawer();
      } catch { /* 抽屉已渲染缓存值，拉失败就维持原样 */ }
    });
    $("btn-faults").addEventListener("click", () => openDrawer("faults"));
    $("btn-runs").addEventListener("click", () => { openRuns().catch(apiErr); });
    $("btn-sheet").addEventListener("click", () => openDrawer("sheet"));
    $("flash-x").addEventListener("click", () => flash(""));
    $("drawer-x").addEventListener("click", closeDrawer);
    $("scrim").addEventListener("click", closeDrawer);
    $("player-x").addEventListener("click", resetPlayer);
  }

  function bindRail() {
    $("roster-q").addEventListener("input", debounce(() => {
      state.rosterQ = $("roster-q").value.trim().toLowerCase();
      renderRoster();
    }, 120));
    for (const [id, key] of [["filter-env", "filterEnv"], ["filter-enterprise", "filterEnterprise"], ["filter-type", "filterType"]]) {
      $(id).addEventListener("change", () => {
        state[key] = $(id).value;
        renderRoster();
      });
    }
    $("roster-list").addEventListener("click", (e) => {
      const box = e.target.closest("[data-check]");
      if (box) {
        e.stopPropagation();
        const id = box.dataset.check;
        if (state.checked.has(id)) state.checked.delete(id);
        else state.checked.add(id);
        renderRoster();
        return;
      }
      const row = e.target.closest(".device");
      if (row) selectDevice(row.dataset.id);
    });
    // 名册行是 li + 键盘支持：Tab 到位后 Enter/Space 选中；
    // 焦点在勾选框上时放行，让原生 button 行为去切勾选。
    $("roster-list").addEventListener("keydown", (e) => {
      if (e.key !== "Enter" && e.key !== " ") return;
      const row = e.target.closest(".device");
      if (!row || e.target.closest("[data-check]")) return;
      e.preventDefault();
      selectDevice(row.dataset.id);
    });
    $("roster-empty-cta").addEventListener("click", () => {
      const k = $("roster-empty").dataset.kind;
      if (k === "none") openDrawer("new");
      else if (k === "search") {
        state.rosterQ = "";
        $("roster-q").value = "";
        renderRoster();
      } else {
        state.filterEnv = state.filterEnterprise = state.filterType = "";
        renderRoster();
      }
    });
    $("btn-batch-clear").addEventListener("click", () => {
      state.checked.clear();
      renderRoster();
    });
    $("btn-batch-start").addEventListener("click", () => batchAct("start"));
    $("btn-batch-stop").addEventListener("click", () => batchAct("stop"));
    $("btn-batch-delete").addEventListener("click", (e) => armThen(e.currentTarget, () => batchAct("delete")));
  }

  function bindStage() {
    $("attach-bar").addEventListener("change", (e) => {
      if (e.target.getAttribute("data-attach") === "product") {
        state.attachProduct = e.target.value;
        renderStage();
        return;
      }
      if (changeTree(e.target)) renderStage();
    });
    $("btn-start").addEventListener("click", startAndWait);
    $("btn-stop").addEventListener("click", stopDevice);
    $("btn-delete").addEventListener("click", deleteDevice);
    $("btn-interrupt").addEventListener("click", interrupt);
    $("btn-rec").addEventListener("click", toggleRec);
    $("btn-file").addEventListener("click", () => $("wav-file").click());
    $("form-speak").addEventListener("submit", speak);
    $("chk-sync").addEventListener("change", () => { state.syncWait = $("chk-sync").checked; });
    $("src-select").addEventListener("change", () => {
      state.srcKey = $("src-select").value;
      if (state.srcKey === "local") $("wav-file").click();
      renderSrc();
      renderStage();
    });
    $("img-select").addEventListener("change", () => {
      state.imgKey = $("img-select").value;
      renderSrc();
    });
    $("wav-file").addEventListener("change", () => {
      if ($("wav-file").files[0]) state.srcKey = "local";
      renderSrc();
      renderStage();
    });
    // 对话区里的播放 / 跳转事件。
    $("conv").addEventListener("click", (e) => {
      const play = e.target.closest("[data-play]");
      if (play) {
        const turn = play.dataset.turn;
        const dir = play.dataset.play === "up" ? "uplink" : "downlink";
        playHref(audioHref(turn, dir), {
          label: (dir === "uplink" ? "上行 · " : "下行 · ") + shortTurn(turn),
          download: `${turn}-${dir}.wav`,
        });
        return;
      }
      const jump = e.target.closest("[data-jump]");
      if (jump) {
        showSide("tape");
        state.tapeQ = jump.dataset.jump.toLowerCase();
        $("tape-q").value = jump.dataset.jump;
        state.tapeRows = [];
        renderTape();
      }
    });

    // 把 WAV 直接拖到作业区就行，不用走文件选择框。
    const stage = $("stage");
    stage.addEventListener("dragover", (e) => {
      if (!e.dataTransfer || state.tombstone) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
      show($("drop-zone"), true);
    });
    stage.addEventListener("dragleave", (e) => {
      if (e.target === stage) show($("drop-zone"), false);
    });
    stage.addEventListener("drop", (e) => {
      show($("drop-zone"), false);
      if (state.tombstone) return;
      const f = e.dataTransfer && e.dataTransfer.files[0];
      if (!f) return;
      e.preventDefault();
      if (!/\.wav$/i.test(f.name)) {
        flash("只收 .wav", "err");
        return;
      }
      const dt = new DataTransfer();
      dt.items.add(f);
      $("wav-file").files = dt.files;
      state.srcKey = "local";
      renderSrc();
      renderStage();
      flash("已选入 " + f.name + " · 按 ⌘⏎ 送出", "ok");
    });
  }

  function bindSide() {
    $("tape").addEventListener("click", (e) => {
      const b = e.target.closest("[data-burst]");
      if (!b) return;
      const k = b.dataset.burst;
      if (state.tapeOpen.has(k)) state.tapeOpen.delete(k);
      else state.tapeOpen.add(k);
      state.tapeRows = [];
      renderTape();
    });
    // 往回翻历史时自动松开跟随，别把人拽回底部。
    $("tape").addEventListener("scroll", () => {
      const on = nearBottom($("tape"));
      if (on === state.tapeFollow) return;
      state.tapeFollow = on;
      const btn = $("btn-follow");
      btn.setAttribute("aria-pressed", String(on));
      btn.classList.toggle("btn--ok", on);
      btn.classList.toggle("btn--icon", !on);
    }, { passive: true });
    $("btn-follow").addEventListener("click", () => setFollow(!state.tapeFollow));
    $("btn-copy").addEventListener("click", copyTape);
    $("tape-chips").addEventListener("click", (e) => {
      const chip = e.target.closest(".chip");
      if (chip) setScope(chip.dataset.scope);
    });
    $("tape-q").addEventListener("input", debounce(() => {
      state.tapeQ = $("tape-q").value.trim().toLowerCase();
      state.tapeRows = [];
      renderTape();
    }, 120));
    $("tab-tape").addEventListener("click", () => showSide("tape"));
    $("tab-turns").addEventListener("click", () => showSide("turns"));
    $("tab-global").addEventListener("click", () => showSide("global"));
    $("turns").addEventListener("click", (e) => {
      const play = e.target.closest("[data-play]");
      if (!play) return;
      const turn = play.dataset.turn;
      const dir = play.dataset.play === "up" ? "uplink" : "downlink";
      playHref(audioHref(turn, dir), {
        label: (dir === "uplink" ? "上行 · " : "下行 · ") + shortTurn(turn),
        download: `${turn}-${dir}.wav`,
      });
    });
  }

  // 抽屉里的所有交互走一层事件委托：内容整块重渲，绑不住具体节点。
  function bindDrawer() {
    const body = $("drawer-body");
    body.addEventListener("click", (e) => {
      const t = e.target;
      const hit = (attr) => {
        const el = t.closest(`[${attr}]`);
        return el ? el.getAttribute(attr) : null;
      };
      const mode = hit("data-newmode");
      if (mode) { state.newMode = mode; renderDrawer(); return; }
      const asset = hit("data-asset");
      if (asset) {
        const row = t.closest("[data-id]");
        if (row) assetAction(asset, row.dataset.id);
        return;
      }
      const sc = hit("data-scenario");
      if (sc !== null) { runScenario(Number(sc)); return; }
      const runOpen = t.closest("[data-run-open]");
      if (runOpen) {
        const ins = runOpen.getAttribute("data-run-open");
        closeDrawer();
        if (runOpen.getAttribute("data-run-src") === "live") selectDevice(state.selectedId).catch(apiErr);
        else openTombstone(state.selectedId, ins, runOpen.getAttribute("data-run-src")).catch(apiErr);
        return;
      }
      const runDel = hit("data-run-del");
      if (runDel) { delRun(runDel); return; }
      const fault = t.closest("[data-fault]");
      if (fault) { injectFault(fault.getAttribute("data-fault")); return; }
      const act = hit("data-act");
      if (act === "create") createFromDrawer();
      else if (act === "close") closeDrawer();
      else if (act === "report") reportMode();
      else if (act === "reset") resetConfig();
    });
    body.addEventListener("change", (e) => {
      const t = e.target;
      const lib = t.getAttribute && t.getAttribute("data-lib");
      if (lib === "kind") {
        state.lib.kind = t.value;
        state.lib.format = "";
        renderDrawer();
        return;
      }
      if (lib === "format") { state.lib.format = t.value; renderDrawer(); return; }
      if (lib === "lang") { state.lib.language = t.value; renderDrawer(); return; }
      if (lib === "newlang") { state.lib.newLang = t.value; return; }
      if (t.id === "lib-file" && t.files[0]) {
        const f = t.files[0];
        importAsset(f, f.name.replace(/\.[^.]+$/, ""), state.lib.newLang).catch(apiErr);
      }
    });
    // 标签过滤直接改行可见性——重绘抽屉会把正在输入的焦点打掉。
    body.addEventListener("input", (e) => {
      const t = e.target;
      if (!t.getAttribute || t.getAttribute("data-lib") !== "tag") return;
      state.lib.tag = t.value.trim();
      const q = state.lib.tag.toLowerCase();
      for (const row of body.querySelectorAll(".list .row")) {
        row.hidden = !!q && !(row.dataset.tags || "").toLowerCase().includes(q);
      }
    });
    body.addEventListener("submit", (e) => {
      e.preventDefault();
      if (e.target.getAttribute("id") === "form-config") saveConfig(e);
      else if (e.target.getAttribute("id") === "form-create") createFromDrawer();
    });
  }

  function bindKeys() {
    window.addEventListener("hashchange", () => {
      const { id, ins } = parseHash();
      if (id) selectDevice(id, ins);
    });
    document.addEventListener("keydown", (e) => {
      const t = e.target;
      const typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
      if (e.key === "Escape") {
        if (state.drawer) closeDrawer();
        else flash("");
        return;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        if (!$("btn-speak").disabled) {
          e.preventDefault();
          speak();
        }
        return;
      }
      if (e.key === "/" && !typing && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        $("roster-q").focus();
        $("roster-q").select();
      }
    });
    // 标签页在后台时别再拉名册，回到前台立刻补一次。
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) refreshList().catch(() => {});
    });
    // 药丸的显隐由 CSS 媒体查询管（见 .fab）；这里只负责窗口变宽时把
    // 借去抽屉的右栏还回三栏布局，否则它会被困在隐藏的抽屉里。
    // 注意别用 matchMedia 的 change：有些内嵌 webview 下它根本不触发。
    window.addEventListener("resize", debounce(() => {
      if (window.innerWidth > 1200 && state.drawer === "sheet") closeDrawer();
    }, 150));
  }

  async function init() {
    applyTheme(readTheme());
    bindShell();
    bindRail();
    bindStage();
    bindSide();
    bindDrawer();
    bindManage();
    bindRegistry();
    bindProducts();
    bindSets();
    bindKeys();
    setFollow(true);
    setMode("bubble");
    showSide("tape");
    await loadRegistry();
    await loadProducts();
    await loadSamples();
    await loadLibrary();
    await loadAudioSets(); // 调试台的送话下拉也要用，不能等进了「音频集」视图才拉
    try {
      await refreshList();
    } catch (err) {
      flash("列表失败：" + err.message, "err");
    }
    const { id, ins } = parseHash();
    if (id) await selectDevice(id, ins);
    applySkin(readSkin());
    renderStage();
    renderConv();
    renderTape();
    setInterval(() => {
      if (document.hidden) return;
      refreshList().catch(() => {});
    }, 2000);
    // NOW 线与静默区靠这秒心跳前进；经典皮肤下 renderDeck 直接返回，开销可忽略。
    setInterval(() => {
      if (document.hidden) return;
      paint("deck");
    }, 1000);
  }

  init();
})();
