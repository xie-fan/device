(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);

  const state = {
    devices: [],
    selectedId: null,
    instanceId: null,
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
    templates: [],
    registry: [],
    regSel: { env: "", ent: "", typ: "" },
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
    // 送话源：'' | sample:<url> | asset:<id> | local
    srcKey: "",
    srcName: "",
    samples: [],
    syncWait: false,
    // 抽屉：null | new | config | assets | help | scenarios | templates | faults | sheet
    drawer: null,
    newMode: "single",
    adding: null,
    globalWs: null,
    globalEvents: [],
    globalNewest: 0,
    lib: { rows: [], format: "", language: "", editingId: null, armedId: null },
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
    dirty.roster = dirty.stage = dirty.conv = dirty.tape = dirty.turns = dirty.global = false;
    if (d.roster) renderRoster();
    if (d.stage) renderStage();
    if (d.conv) renderConv();
    if (d.tape) renderTape();
    if (d.turns) renderTurns();
    if (d.global) renderGlobalTape();
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
      state.flashTimer = setTimeout(() => { box.hidden = true; }, 6000);
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
    if (state.tombstone) return "连接已结束，重新启动后可再送出";
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
    return v === "running" ? "ok" : v === "failed" ? "err" : v === "starting" ? "warn" : v === "deleted" ? "mute" : "";
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
        const hay = [d.device_id, d.environment, d.enterprise, d.device_type, d.instance_id].join(" ").toLowerCase();
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
    if (state.drawer === "new" || state.drawer === "config") renderDrawer();
  }

  function attachRefs() {
    return {
      environment: state.regSel.env,
      enterprise: state.regSel.ent,
      device_type: state.regSel.typ,
    };
  }

  async function loadTemplates() {
    try {
      const data = await api("GET", "/templates");
      state.templates = data.templates || [];
    } catch {
      state.templates = [];
    }
  }

  async function refreshList() {
    const data = await api("GET", "/devices");
    state.devices = data.devices || [];
    // 名册里没有的选中项要撤掉，批量条才不会指向幽灵设备。
    for (const id of [...state.checked]) {
      if (!state.devices.some((d) => d.device_id === id)) state.checked.delete(id);
    }
    renderRoster();
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
      return `<li class="device${on ? " is-on" : ""}" data-id="${esc(d.device_id)}" title="${esc(tip)}">
        <div class="device__top">
          <button type="button" class="device__box${picked ? " is-on" : ""}" data-check="${esc(d.device_id)}" title="多选以批量启停删" aria-pressed="${picked}">${picked ? "✓" : ""}</button>
          <span class="led led--${esc(st)}" aria-hidden="true"></span>
          <span class="device__id">${esc(d.device_id)}</span>
          <span class="grow"></span>
          <span class="device__act">${esc(fmtHMS(d.last_activity))}</span>
        </div>
        <div class="device__body">
          ${path ? `<div class="device__path">${esc(path)}</div>` : ""}
          <div class="device__tags">
            <span class="${tagCls(insTone(st))}">${esc(st)}</span>
            <span class="${tagCls(connTone(d.connection_state))}">${esc(d.connection_state || "—")}</span>
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
    const st = !has ? "" : state.tombstone ? "deleted" : (live.instance_state || "created");
    const conn = live.connection_state || "—";

    $("stage-led").className = "led" + (st ? " led--" + st : "");
    setText($("stage-name"), state.selectedId || "—");
    const kind = $("stage-kind");
    show(kind, has);
    if (has) {
      setText(kind, state.tombstone ? "历史" : "对话");
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
    `);

    // last_error 原来只藏在 title 里，工位上根本看不见。
    show($("talk-error"), !!live.last_error);
    setText($("talk-error-text"), live.last_error || "");

    const slot = !!state.occupiedTurnId;
    show($("slot-banner"), slot);
    if (slot) {
      const q = backlog > 0 ? ` · 队列 ${backlog} 项` : "";
      setText($("slot-text"), `槽占用 ${state.occupiedTurnId} · 等 turn_terminal${q}`);
    }

    show($("tomb-banner"), state.tombstone);
    if (state.tombstone) {
      setText($("tomb-meta"), `instance_id ${state.instanceId || "—"} · 事件与 turn 只读 · TTL 24 小时`);
    }

    setDisabled($("btn-start"), !has || state.tombstone || state.busy || !identityEditable());
    setDisabled($("btn-stop"), !has || state.tombstone || state.busy);
    setDisabled($("btn-delete"), !has || state.tombstone || state.busy);
    setDisabled($("btn-config"), !has);
    setDisabled($("btn-faults"), !has || state.tombstone);

    const blocked = has ? blockedReason() : "先在左边选一台设备";
    show($("send-blocked"), !!blocked);
    setText($("send-blocked-text"), blocked);
    const can = !blocked && !state.busy && !!state.srcKey;
    setDisabled($("btn-speak"), !can);
    $("btn-speak").title = state.occupiedTurnId && backlogEnabled()
      ? "槽占用 · 送出将进队列" : "Ctrl/⌘ + Enter 送出";
    setDisabled($("btn-interrupt"), !interruptableUI() || state.busy);
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
        t = { turn_id: tid, ts: ev.ts, evCount: 0, hasAsr: false, seq: ev.event_seq };
        byId.set(tid, t);
      }
      t.evCount++;
      if (ev.event_type === "asr_result") t.hasAsr = true;
    }
    for (const rec of state.turns) {
      if (rec.turn_id && !byId.has(rec.turn_id)) {
        byId.set(rec.turn_id, { turn_id: rec.turn_id, ts: "", evCount: 0, hasAsr: false, seq: Number.MAX_SAFE_INTEGER });
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

  function bubbleHTML(t) {
    const upMeta = t.up
      ? `${t.up} 包 · ${fmtBytes(t.upBytes)}`
      : (t.phase === "up" ? "受理中" : "没有帧日志");
    const ttsMeta = t.down
      ? `tts_chunk ${t.down} 包 · ${fmtBytes(t.downBytes)}${t.done ? " · tts_done" : ""}`
      : (t.done ? "本轮没有下行音频" : "等待下行");
    const reply = t.hasAsr || t.down || t.done ? `
      <div class="bub bub--reply">
        <div class="bub__in">
          <div class="bub__head">
            <span class="bub__who">设备回复</span>
            <span class="bub__sub">${t.hasAsr ? "asr_result" : "尚无 asr_result"}</span>
          </div>
          <p class="bub__asr bub__asr--none">${t.hasAsr
            ? "服务端已返回 asr_result（协议不带识别文本，原文见右栏事件）"
            : "服务端还没有回 asr_result"}</p>
          <div class="bub__rule"></div>
          ${bars(t.down, 30, t.phase === "tts", " bars--tts")}
          <div class="bub__meta"><span>${esc(ttsMeta)}</span></div>
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
        setText($("conv-empty-body"), "在下面选一段音频按 ⌘⏎ 送出，我送了什么、设备回了什么、这轮怎么结束的都会按顺序落在这里。");
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
    if (k.startsWith("asset:")) {
      const a = state.lib.rows.find((x) => x.asset_id === k.slice(6));
      if (!a) return { name: "（素材已不在库里）", meta: "" };
      return {
        name: a.name,
        meta: [a.format, a.sample_rate ? a.sample_rate + " Hz" : "", fmtDurMs(a.duration_ms), "格式不符自动 ffmpeg 转码"]
          .filter(Boolean).join(" · "),
      };
    }
    if (k === "local") {
      const f = $("wav-file").files[0];
      if (f) return { name: f.name, meta: `${fmtBytes(f.size)} · 送出时入库为 asset` };
      return { name: "未选择文件", meta: "点这里从本机挑一个 .wav，或直接拖到中栏" };
    }
    return { name: "—", meta: "" };
  }

  function renderSrc() {
    const sel = $("src-select");
    const html = `<option value="">选音频源…</option>` +
      state.samples.map((s) => `<option value="sample:${esc(s.url)}">夹具 · ${esc(s.name)}</option>`).join("") +
      state.lib.rows.map((a) => `<option value="asset:${esc(a.asset_id)}">音频库 · ${esc(a.name)}</option>`).join("") +
      `<option value="local">本机文件 · 选择 .wav</option>`;
    if (sel.dataset.sig !== html) {
      sel.innerHTML = html;
      sel.dataset.sig = html;
    }
    if (sel.value !== state.srcKey) sel.value = state.srcKey;
    const info = srcInfo();
    setText($("src-name"), info.name);
    setText($("src-meta"), info.meta);
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
    setNum($("tape-count"), state.events.length);
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
      flash("本轮结束" + (extra ? " · " + extra : "") + " · 可再送出", "ok");
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

  async function openTombstone(id, ins) {
    const instanceChanged = state.selectedId !== id || state.instanceId !== ins;
    state.selectedId = id;
    state.tombstone = true;
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
    flash("live 已摘除。TTL 内用同一 instance_id 看历史，不要换到新实例。", "info");
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
        state.turns.forEach((t) => { if (t.turn_id) queueFrames(t.turn_id); });
      } catch {
        state.turns = [];
      }
      connectWS({ fromOldest: resetTape || state.events.length === 0 });
    }
    renderTurns();
  }

  // 设备体只含设备级属性；enterprise/device_type/server 由树引用派生。
  function defaultDevice(id, extra) {
    return {
      device_id: id,
      action: "chatbot",
      firmware_version: extra.firmware_version || "1.0.0",
      nic_type: extra.nic_type || "wifi",
      nic_iccid: extra.nic_iccid || "8986xxxxxxxxxx",
      playing_mode: extra.playing_mode || 1,
      audio: {
        format: "pcm",
        sample_rate: extra.sample_rate || 16000,
        channels: 1,
        sample_format: "s16le",
        slice_ms: 100,
        max_payload_size: 51200,
      },
      behavior: {
        auto_register: true,
        auto_report: true,
        // 实测联调服务端空闲约 60s 即踢线；keepalive 必须明显短于该窗口，
        // 取 60 会与踢线同刻开火、必输竞态（每轮对话结束约 1 分钟后掉线）。
        keepalive_interval_sec: 30,
        keepalive_method: "report",
        report_sequence_start: 1,
        report_echo_timeout_sec: 5,
        register_ack_timeout_sec: 5,
        first_reply_timeout_sec: 90,
        downlink_idle_timeout_sec: 40,
        non_audio_followup_sec: 5,
        post_final_asr_silence_sec: 5,
        wait_timeout_slack_sec: 5,
        expect_downlink_need_ack: false,
        downlink_ack: { mode: "binary", sleep_ms: 0, code: 0 },
      },
      uuid: { min: 1, max: 2147483647 },
      recording: {
        enable_frame_log: true,
        save_uplink_audio: true,
        save_downlink_audio: true,
        output_dir: "./recordings",
      },
    };
  }

  // ——— 生命周期 ———

  async function startAndWait() {
    if (!state.selectedId) return;
    state.busy = true;
    renderStage();
    flash("正在启动…", "info");
    try {
      const started = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/start`);
      state.instanceId = started.instance_id;
      state.connGeneration = started.conn_generation;
      if (state.live) {
        state.live.instance_id = started.instance_id;
        state.live.conn_generation = started.conn_generation;
        state.live.instance_state = "starting";
      }
      connectWS({ fromOldest: state.events.length === 0 });
      renderStage();
      const ready = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/wait_ready`, {
        instance_id: started.instance_id,
        conn_generation: started.conn_generation,
      });
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
      const res = await api("POST", `/devices/batch/${kind}`, { device_ids: ids });
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
    const spoken = await api("POST", path, { asset_id: assetId });
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

  async function speak(ev) {
    if (ev) ev.preventDefault();
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
      if (k.startsWith("asset:")) {
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

  // ——— 音频库 ———
  // 试听一律走 ?decode=1，服务端解码成 wav 再给 <audio>：浏览器放不了 amr
  // 与裸 pcm，而 manager 有现成的 ffmpeg 解码能力（Phase 6）。

  // 连改筛选时多个请求在飞，旧响应后到会覆盖新结果：只认最新一次。
  let libReqSeq = 0;

  async function loadLibrary() {
    const seq = ++libReqSeq;
    const q = new URLSearchParams();
    if (state.lib.format) q.set("format", state.lib.format);
    if (state.lib.language) q.set("language", state.lib.language);
    const qs = q.toString();
    let rows = [];
    try {
      const data = await api("GET", "/assets" + (qs ? "?" + qs : ""));
      rows = data.assets || [];
    } catch {
      rows = [];
    }
    if (seq !== libReqSeq) return;
    state.lib.rows = rows;
    setNum($("lib-count"), rows.length);
    renderSrc();
    if (state.drawer === "assets") renderDrawer();
  }

  async function importAsset(file, name, lang) {
    const fd = new FormData();
    fd.append("file", file, file.name);
    if (name) fd.append("name", name);
    if (lang) fd.append("language", lang);
    const a = await api("POST", "/assets", fd);
    flash(`已导入 ${a.name}（${a.format} · ${fmtDurMs(a.duration_ms)}）`, "ok");
    await loadLibrary();
  }

  // ——— 抽屉 ———

  const DRAWER_TITLE = {
    new: ["新建设备", "POST /devices"],
    config: ["配置", "GET / PUT /devices/{id}/config"],
    assets: ["音频库", "GET / POST /assets"],
    help: ["术语与状态机", ""],
    scenarios: ["场景编排", "POST /scenarios/run"],
    templates: ["模板管理", "GET / POST / DELETE /templates"],
    faults: ["注入故障", "POST /devices/{id}/faults"],
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
    },
    {
      key: "ent", label: "厂商",
      l1: "名称", p1: "Acme 智能",
      l2: "简称（wire 值）", p2: "acme",
      api: "POST /registry/environments/{env}/enterprises",
    },
    {
      key: "typ", label: "设备类型",
      l1: "名称", p1: "迷你音箱",
      l2: "简称（wire 值）", p2: "speaker-mini",
      api: "POST …/enterprises/{short}/device_types",
    },
  ];

  function treeHTML() {
    const sel = state.regSel;
    const envNode = regEnv();
    const entNode = regEnt();
    const typNode = regTypes().find((x) => x.short_name === sel.typ);
    const rows = [
      {
        cfg: TREE_LEVELS[0], value: sel.env, disabled: false,
        opts: state.registry.map((r) => ({ v: r.name, t: r.name })), ph: "选一个环境",
        hint: envNode ? `url  ${envNode.url}  ·  {enterprise} 会在 start 时替换成厂商简称` : "",
      },
      {
        cfg: TREE_LEVELS[1], value: sel.ent, disabled: !sel.env,
        opts: regEnts().map((r) => ({ v: r.short_name, t: `${r.name}（${r.short_name}）` })), ph: sel.env ? "选一个厂商" : "先选环境",
        hint: entNode ? `名称  ${entNode.name}  ·  简称  ${entNode.short_name}  ·  上线报文里真正用的是简称` : "",
      },
      {
        cfg: TREE_LEVELS[2], value: sel.typ, disabled: !sel.ent,
        opts: regTypes().map((r) => ({ v: r.short_name, t: `${r.name}（${r.short_name}）` })), ph: sel.ent ? "选一个类型" : "先选厂商",
        hint: typNode ? `名称  ${typNode.name}  ·  简称  ${typNode.short_name}` : "",
      },
    ];
    const missing = [!sel.env && "环境", !sel.ent && "厂商", !sel.typ && "设备类型"].filter(Boolean);
    return rows.map((r) => {
      const k = r.cfg.key;
      const adding = state.adding === k;
      return `<div class="lvl">
        <div class="lvl__row">
          <span class="lvl__label">${esc(r.cfg.label)}</span>
          <select data-tree="${k}"${r.disabled ? " disabled" : ""}>${selOpts(r.opts, r.value, r.ph)}</select>
          <button type="button" class="btn btn--ghost" data-tree-add="${k}">＋ 新增</button>
          <button type="button" class="btn btn--ghost" data-tree-rename="${k}"${r.value ? "" : " disabled"}>${k === "env" ? "改 url" : "改名"}</button>
          <button type="button" class="btn btn--ghost" data-tree-del="${k}"${r.value ? "" : " disabled"}>删除</button>
        </div>
        ${r.hint ? `<div class="lvl__hint">${esc(r.hint)}</div>` : ""}
        ${adding ? `<div class="lvl__add">
          <div class="pair">
            <label class="fld"><span class="fld__name">${esc(r.cfg.l1)}</span><input data-add="1" placeholder="${esc(r.cfg.p1)}"></label>
            <label class="fld"><span class="fld__name">${esc(r.cfg.l2)}</span><input data-add="2" placeholder="${esc(r.cfg.p2)}" value="${k === "env" ? esc(r.cfg.p2) : ""}"></label>
          </div>
          <div class="lvl__foot">
            <button type="button" class="btn btn--primary btn--sm" data-tree-ok="${k}">确认新增</button>
            <button type="button" class="btn btn--ghost" data-tree-cancel="1">取消</button>
            <span class="grow"></span>
            <span class="api">${esc(r.cfg.api)}</span>
          </div>
        </div>` : ""}
      </div>`;
    }).join("") + (missing.length
      ? `<p class="warnbar" style="margin-left:29px">三级要选全才能建设备：${esc(missing.join(" / "))} 还没选。</p>`
      : "");
  }

  const NEW_FIELDS = [
    { name: "device_id", zh: "设备标识", v: "sim_0001", ph: "sim_0001" },
    { name: "firmware_version", zh: "固件版本", v: "1.0.0", ph: "1.0.0" },
    { name: "nic_type", zh: "网卡类型", v: "wifi", ph: "wifi / lte" },
    { name: "nic_iccid", zh: "SIM ICCID", v: "8986xxxxxxxxxx", ph: "20 位数字", note: "模拟上报的 SIM 卡号，纯透传" },
    { name: "playing_mode", zh: "播放模式", v: "1", ph: "1-3" },
    { name: "sample_rate", zh: "采样率", v: "16000", ph: "16000", note: "amr 仅支持 8000 / 16000" },
  ];

  function fieldHTML(f) {
    const wide = f.note && f.note.length > 30;
    return `<label class="fld${wide ? " fld--wide" : ""}">
      <span class="fld__head">
        <span class="fld__name">${esc(f.name)}</span>
        <span class="fld__zh">${esc(f.zh || "")}</span>
        ${f.ro ? `<span class="fld__ro">只读</span>` : ""}
      </span>
      ${f.select
        ? `<select name="${esc(f.name)}"${f.locked ? " disabled" : ""}>${selOpts(f.select, f.v, f.ph || "")}</select>`
        : f.check
          ? `<span class="fld__chk"><input type="checkbox" name="${esc(f.name)}"${f.on ? " checked" : ""}${f.locked ? " disabled" : ""}><span>${esc(f.v || "")}</span></span>`
          : `<input name="${esc(f.name)}" value="${esc(f.v ?? "")}" placeholder="${esc(f.ph || "")}"${f.locked || f.ro ? " disabled" : ""}${f.num ? ` type="number"` : ""}>`}
      ${f.note ? `<span class="fld__note">${esc(f.note)}</span>` : ""}
    </label>`;
  }

  function newDrawerHTML() {
    const single = state.newMode === "single";
    const fields = single
      ? NEW_FIELDS
      : [
        { name: "template_id", zh: "模板", v: "", ph: "选择模板", select: state.templates.map((t) => ({ v: t, t })) },
        { name: "count", zh: "数量", v: "2", ph: "2", num: true },
        { name: "id_prefix", zh: "ID 前缀", v: "sim", ph: "sim", note: "ID 冲突会导致整批失败" },
      ];
    return `<div class="sheet">
      <div class="step">
        <div class="step__head">
          <span class="step__n">1</span><span class="step__t">挂到配置树上</span>
          <span class="step__hint">环境 → 厂商 → 设备类型</span>
        </div>
        ${treeHTML()}
      </div>
      <div class="rule"></div>
      <div class="step">
        <div class="step__head">
          <span class="step__n">2</span><span class="step__t">怎么建</span>
          <span class="grow"></span>
          <span class="seg">
            <button type="button" class="seg__btn${single ? " is-on" : ""}" data-newmode="single">单个</button>
            <button type="button" class="seg__btn${single ? "" : " is-on"}" data-newmode="template">按模板批量</button>
          </span>
        </div>
        <form id="form-create" class="fields fields--pad">${fields.map(fieldHTML).join("")}</form>
        <p class="hint" style="padding-left:29px">${single
          ? "单个创建与按模板批量是两种互斥的方式，上面切换一次只显示一种。"
          : "按模板批量：一次 POST /devices 带 template_id，前缀 + 序号生成 device_id。任一 ID 冲突，整批回滚。"}</p>
        <div class="foot foot--plain" style="padding-left:29px">
          <button type="button" class="btn btn--primary" data-act="create">${single ? "创建设备" : "批量创建"}</button>
          <button type="button" class="btn btn--sub" data-act="close">取消</button>
          <span class="grow"></span>
          <span class="api">其余 30+ 字段用默认值补齐</span>
        </div>
      </div>
    </div>`;
  }

  function configDrawerHTML() {
    const cfg = state.config;
    if (!cfg) return `<p class="blank--drawer">这台设备没有可读的配置（可能已进入墓碑态）。</p>`;
    const lock = !identityEditable();
    const audio = cfg.audio || {};
    const beh = cfg.behavior || {};
    const rec = cfg.recording || {};
    const uuid = cfg.uuid || {};
    const server = cfg.server || {};
    const st = state.tombstone ? "deleted" : ((state.live && state.live.instance_state) || "created");

    // 当前值可能已不在树上（节点被引用时删不掉，但可能是历史值）：补进选项免得显示空白。
    const withCur = (rows, val) => (val && !rows.some((r) => r.v === val) ? rows.concat([{ v: val, t: val }]) : rows);
    const envOpts = withCur(state.registry.map((r) => ({ v: r.name, t: `${r.name} · ${r.url}` })), cfg.environment);
    const curEnv = state.registry.find((x) => x.name === cfg.environment);
    const entRows = (curEnv && curEnv.enterprises) || [];
    const entOpts = withCur(entRows.map((r) => ({ v: r.short_name, t: `${r.name}（${r.short_name}）` })), cfg.enterprise);
    const curEnt = entRows.find((x) => x.short_name === cfg.enterprise);
    const typOpts = withCur(((curEnt && curEnt.device_types) || []).map((r) => ({ v: r.short_name, t: `${r.name}（${r.short_name}）` })), cfg.device_type);

    const groups = [
      { title: "挂靠", tag: "仅 created / stopped 可改", lock: true, fields: [
        { name: "device_id", zh: "设备标识", v: cfg.device_id, ro: true },
        { name: "server.url", zh: "服务端地址", v: server.url || "", ro: true, note: "由环境派生，start 时重新解析" },
        { name: "environment", zh: "环境", v: cfg.environment, select: envOpts, locked: lock },
        { name: "enterprise", zh: "厂商", v: cfg.enterprise, select: entOpts, locked: lock },
        { name: "device_type", zh: "设备类型", v: cfg.device_type, select: typOpts, locked: lock, note: "改环境要重建下游选项" },
      ]},
      { title: "音频", tag: "仅 created / stopped 可改", lock: true, fields: [
        { name: "audio.format", zh: "格式", v: audio.format || "pcm", locked: lock,
          select: ["pcm", "wav", "mp3", "amr", "aac"].map((f) => ({ v: f, t: f })),
          note: "压缩格式经 ffmpeg 转码并 -re 限速推流；下发格式跟随上行，但 wav 设备的下行会回落成 pcm" },
        { name: "audio.sample_rate", zh: "采样率", v: audio.sample_rate ?? 16000, num: true, locked: lock, note: "amr 仅支持 8000 / 16000；running 下改会 409" },
        { name: "audio.bitrate_kbps", zh: "码率", v: audio.bitrate_kbps ?? 0, num: true, locked: lock, note: "0 = 默认，仅压缩格式有效" },
        { name: "audio.slice_ms", zh: "切片长度", v: audio.slice_ms ?? 100, num: true, locked: lock },
        { name: "audio.max_payload_size", zh: "单包上限", v: audio.max_payload_size ?? 51200, num: true, locked: lock },
        { name: "channels", zh: "声道", v: audio.channels ?? 1, ro: true },
        { name: "sample_format", zh: "采样格式", v: audio.sample_format || "s16le", ro: true },
      ]},
      { title: "身份", tag: "仅 created / stopped 可改", lock: true, fields: [
        { name: "firmware_version", zh: "固件版本", v: cfg.firmware_version || "", locked: lock },
        { name: "nic_type", zh: "网卡类型", v: cfg.nic_type || "", locked: lock },
        { name: "nic_iccid", zh: "SIM ICCID", v: cfg.nic_iccid || "", locked: lock },
        { name: "playing_mode", zh: "播放模式", v: cfg.playing_mode ?? 1, num: true, locked: lock, note: "1-3；ready 下可热更新" },
        { name: "uuid.min", zh: "UUID 下界", v: uuid.min ?? 1, num: true, locked: lock },
        { name: "uuid.max", zh: "UUID 上界", v: uuid.max ?? 2147483647, num: true, locked: lock },
      ]},
      { title: "行为", tag: "仅 created / stopped 可改", lock: true, fields: [
        { name: "behavior.keepalive_interval_sec", zh: "心跳间隔", v: beh.keepalive_interval_sec ?? 30, num: true, locked: lock, note: "实测服务端约 60s 踢线，这里必须明显更短" },
        { name: "behavior.first_reply_timeout_sec", zh: "首包超时", v: beh.first_reply_timeout_sec ?? 90, num: true, locked: lock },
        { name: "behavior.speak_backlog_depth", zh: "送话排队深度", v: beh.speak_backlog_depth ?? 0, num: true, locked: lock, note: "0-64；0 = 关闭，槽占用直接 409" },
        { name: "behavior.silence_probe", zh: "静默探针", check: true, on: !!beh.silence_probe, locked: lock, v: "timeout 静默终态后发一个探针 report" },
        { name: "behavior.interrupt_on_disconnect", zh: "断线即打断", check: true, on: !!beh.interrupt_on_disconnect, locked: lock, v: "事件 WS 断开时打断当前 turn" },
      ]},
      { title: "录音", tag: "running 也可改", lock: false, fields: [
        { name: "recording.enable_frame_log", zh: "帧日志", check: true, on: !!rec.enable_frame_log, v: "记录每个包的序号与字节数" },
        { name: "recording.save_uplink_audio", zh: "存上行", check: true, on: !!rec.save_uplink_audio, v: "把送出去的音频落盘" },
        { name: "recording.save_downlink_audio", zh: "存下行", check: true, on: !!rec.save_downlink_audio, v: "把服务端回的音频落盘" },
        { name: "recording.output_dir", zh: "输出目录", v: rec.output_dir || "" },
      ]},
    ];
    const conn = (state.live && state.live.connection_state) || "—";
    return `<form id="form-config" class="sheet sheet--tight">
      <div class="lockbar lockbar--${lock ? "warn" : "ok"}">
        <span class="tag">锁态</span>
        <span>${lock
          ? `当前 instance_state 是 ${esc(st)}，只有录音那一组能改。改采样率会返回 409，要先停止设备。`
          : `当前 instance_state 是 ${esc(st)}，挂靠 / 音频 / 身份 / 行为 四组都可以改。`}</span>
      </div>
      ${groups.map((g) => `<div class="grp">
        <div class="grp__head">
          <span class="grp__title">${esc(g.title)}</span>
          <span class="${tagCls(g.lock ? (lock ? "warn" : "") : "ok")}">${esc(g.tag)}</span>
          <span class="rule"></span>
        </div>
        <div class="fields">${g.fields.map(fieldHTML).join("")}</div>
      </div>`).join("")}
      <div class="foot">
        <button type="submit" class="btn btn--primary"${state.tombstone ? " disabled" : ""}>保存配置</button>
        <button type="button" class="btn btn--sub" data-act="report"${conn === "ready" ? "" : " disabled"}
          title="POST /devices/{id}/report {playingMode} · 仅 connection_state=ready">热更新 playingMode</button>
        <span class="grow"></span>
        <span class="api">connection_state = ${esc(conn)} · 热更新${conn === "ready" ? "可用" : "不可用"}</span>
      </div>
    </form>`;
  }

  function assetsDrawerHTML() {
    const rows = state.lib.rows;
    return `<div class="sheet sheet--tight">
      <div class="bar">
        <select data-lib="format" style="width:140px">${selOpts(
          ["pcm", "wav", "mp3", "amr", "aac"].map((f) => ({ v: f, t: f })), state.lib.format, "全部格式")}</select>
        <input data-lib="lang" value="${esc(state.lib.language)}" placeholder="语言，如 zh" class="mono" style="width:130px;padding:7px 10px;border-radius:var(--r);border:1px solid var(--line);background:var(--well);font-size:11.5px">
        <span class="grow"></span>
        <label class="btn btn--primary btn--sm">＋ 导入<input type="file" id="lib-file" accept=".wav,.mp3,.amr,.aac,audio/*" hidden></label>
      </div>
      <p class="hint">POST /assets（multipart）· wav / mp3 / amr / aac · 上限 10 MB / 60 秒</p>
      <div class="list">
        ${rows.length ? rows.map((a) => {
          const armed = state.lib.armedId === a.asset_id;
          const editing = state.lib.editingId === a.asset_id;
          if (editing) {
            return `<div class="row" data-id="${esc(a.asset_id)}">
              <div class="fields">
                <label class="fld"><span class="fld__name">名称</span><input data-edit="name" value="${esc(a.name)}"></label>
                <label class="fld"><span class="fld__name">语言</span><input data-edit="language" value="${esc(a.language || "")}"></label>
              </div>
              <div class="bar">
                <button type="button" class="btn btn--primary btn--sm" data-asset="save">保存</button>
                <button type="button" class="btn btn--ghost" data-asset="cancel">取消</button>
                <span class="grow"></span><span class="api">PATCH /assets/${esc(a.asset_id)}</span>
              </div>
            </div>`;
          }
          return `<div class="row" data-id="${esc(a.asset_id)}">
            <div class="row__top">
              <span class="row__name" title="${esc(a.asset_id)}">${esc(a.name)}</span>
              <span class="${tagCls("acc")}">${esc(a.format)}</span>
              <span class="grow"></span>
              <button type="button" class="btn btn--ok btn--tiny" data-asset="play" title="试听（服务端解码为 wav）">▶</button>
              <button type="button" class="btn btn--ghost" data-asset="edit">改名</button>
              <button type="button" class="btn btn--danger btn--ghost" data-asset="del">${armed ? "再点一次" : "删除"}</button>
            </div>
            <div class="row__meta">
              <span>${esc(a.sample_rate ? a.sample_rate + " Hz" : "—")}</span><span class="sep">|</span>
              <span>${esc(a.bitrate_kbps ? a.bitrate_kbps + " kbps" : "—")}</span><span class="sep">|</span>
              <span>${esc(fmtDurMs(a.duration_ms))}</span><span class="sep">|</span>
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
    ["playing_mode", "设备播放模式，取值 1-3。Ready 状态下可以不重启直接热更新。"],
    ["nic_iccid", "模拟设备上报的 SIM 卡 ICCID，纯透传字段，服务端一般只做日志。"],
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
    ["一个 turn 的生命周期", ["speak 受理", "speak_queued", "speak_dequeued", "上行推包", "asr_result", "tts_chunk ×N", "tts_done", "turn_terminal"],
      "时间线上从左到右就是这个顺序；tts_chunk 一秒能来几十条，事件区按包组折叠。"],
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
        ok: ids.length > 0,
        why: "先在名册里勾几台，或选中一台设备",
        spec: { name: "batch-start", steps: [{ action: "batch_start", device_ids: ids, wait_ready: true }] },
      },
      {
        name: "单轮语音回归",
        steps: `batch_start(1) → speak{asset_id} → wait`,
        ok: !!(state.selectedId && asset),
        why: "先选中一台设备，并在送话条里选一条音频库素材",
        spec: {
          name: "one-turn",
          steps: [
            { action: "batch_start", device_ids: [state.selectedId], wait_ready: true },
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

  function templatesDrawerHTML() {
    return `<div class="sheet sheet--tight">
      <div class="bar">
        <span class="api">GET /templates · POST /templates · DELETE /templates/{id}</span>
        <span class="grow"></span>
        <button type="button" class="btn btn--primary btn--sm" data-act="tpl-add"${state.config ? "" : " disabled"}>＋ 从当前设备存模板</button>
      </div>
      ${state.templates.length ? state.templates.map((t) => `<div class="row row--flat" data-id="${esc(t)}">
        <span class="mono" style="font-size:12px">${esc(t)}</span>
        <span class="grow"></span>
        <button type="button" class="btn btn--danger btn--sm" data-tpl-del="${esc(t)}">删除</button>
      </div>`).join("") : `<p class="blank--drawer">还没有模板。选中一台配好的设备，用上面的按钮存一个。</p>`}
    </div>`;
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
    return `<div class="sheet sheet--tight">
      <p class="hint">POST /devices/{id}/faults · 往这台设备的连接上注入一个故障，用来验证服务端的异常分支。</p>
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
    else if (w === "templates") setHTML(body, templatesDrawerHTML());
    else if (w === "faults") setHTML(body, faultsDrawerHTML());
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

  function formVal(name) {
    const el = $("drawer-body").querySelector(`[name="${CSS.escape(name)}"]`);
    if (!el) return "";
    return el.type === "checkbox" ? el.checked : el.value;
  }

  async function createFromDrawer() {
    const refs = attachRefs();
    if (!refs.environment || !refs.enterprise || !refs.device_type) {
      flash("三级挂靠还没选全", "err");
      return;
    }
    try {
      if (state.newMode === "single") {
        const id = String(formVal("device_id") || "").trim();
        if (!id) {
          flash("device_id 不能空", "err");
          return;
        }
        await api("POST", "/devices", {
          ...refs,
          device: defaultDevice(id, {
            firmware_version: String(formVal("firmware_version") || "").trim(),
            nic_type: String(formVal("nic_type") || "").trim(),
            nic_iccid: String(formVal("nic_iccid") || "").trim(),
            playing_mode: Number(formVal("playing_mode")),
            sample_rate: Number(formVal("sample_rate")),
          }),
        });
        flash("POST /devices · 已创建 " + id, "ok");
        closeDrawer();
        await refreshList();
        await selectDevice(id);
      } else {
        const template_id = String(formVal("template_id") || "");
        if (!template_id) {
          flash("请选择模板", "err");
          return;
        }
        const data = await api("POST", "/devices", {
          ...refs,
          template_id,
          count: Number(formVal("count")) || 1,
          id_prefix: String(formVal("id_prefix") || "sim"),
        });
        flash("已创建 " + (data.device_ids || []).join(", "), "ok");
        closeDrawer();
        await refreshList();
        if (data.device_ids && data.device_ids[0]) await selectDevice(data.device_ids[0]);
      }
    } catch (err) {
      apiErr(err);
    }
  }

  function readConfigDrawer() {
    const rec = {
      enable_frame_log: !!formVal("recording.enable_frame_log"),
      save_uplink_audio: !!formVal("recording.save_uplink_audio"),
      save_downlink_audio: !!formVal("recording.save_downlink_audio"),
      output_dir: formVal("recording.output_dir"),
    };
    if (!identityEditable()) return { recording: rec };
    return {
      environment: formVal("environment"),
      enterprise: formVal("enterprise"),
      device_type: formVal("device_type"),
      firmware_version: formVal("firmware_version"),
      nic_type: formVal("nic_type"),
      nic_iccid: formVal("nic_iccid"),
      playing_mode: Number(formVal("playing_mode")),
      audio: {
        format: formVal("audio.format") || "pcm",
        sample_rate: Number(formVal("audio.sample_rate")),
        channels: 1,
        sample_format: "s16le",
        slice_ms: Number(formVal("audio.slice_ms")),
        max_payload_size: Number(formVal("audio.max_payload_size")),
        bitrate_kbps: Number(formVal("audio.bitrate_kbps")) || 0,
      },
      uuid: { min: Number(formVal("uuid.min")), max: Number(formVal("uuid.max")) },
      behavior: {
        keepalive_interval_sec: Number(formVal("behavior.keepalive_interval_sec")),
        first_reply_timeout_sec: Number(formVal("behavior.first_reply_timeout_sec")),
        speak_backlog_depth: Number(formVal("behavior.speak_backlog_depth")) || 0,
        silence_probe: !!formVal("behavior.silence_probe"),
        interrupt_on_disconnect: !!formVal("behavior.interrupt_on_disconnect"),
      },
      recording: rec,
    };
  }

  // 换环境/厂商时就地重建下游 <select>，不整块重渲——保住用户手打的值。
  function cascadeConfig(changed) {
    const body = $("drawer-body");
    const envSel = body.querySelector('[name="environment"]');
    const entSel = body.querySelector('[name="enterprise"]');
    const typSel = body.querySelector('[name="device_type"]');
    if (!envSel || !entSel || !typSel) return;
    const env = state.registry.find((x) => x.name === envSel.value);
    if (changed === "env") {
      const ents = (env && env.enterprises) || [];
      entSel.innerHTML = ents.length
        ? ents.map((r) => `<option value="${esc(r.short_name)}">${esc(r.name)}（${esc(r.short_name)}）</option>`).join("")
        : `<option value="">（该环境下无厂商）</option>`;
    }
    const ent = ((env && env.enterprises) || []).find((x) => x.short_name === entSel.value);
    const typs = (ent && ent.device_types) || [];
    typSel.innerHTML = typs.length
      ? typs.map((r) => `<option value="${esc(r.short_name)}">${esc(r.name)}（${esc(r.short_name)}）</option>`).join("")
      : `<option value="">（该厂商下无类型）</option>`;
  }

  async function saveConfig(ev) {
    if (ev) ev.preventDefault();
    if (!state.selectedId || state.tombstone) return;
    try {
      state.config = await api("PUT", `/devices/${encodeURIComponent(state.selectedId)}/config`, readConfigDrawer());
      flash("PUT /devices/" + state.selectedId + "/config · 已保存", "ok");
      renderDrawer();
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
    const body = $("drawer-body");
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
        await api("POST", TREE_PATH.typ(), { name: a, short_name: b });
        state.regSel.typ = b;
      }
      state.adding = null;
      await loadRegistry();
      flash("已新增 " + a, "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  // 后端只开了两种改法：环境改 url，厂商/设备类型改 name（简称是主键，改不了）。
  async function treeEdit(k) {
    const s = state.regSel;
    if (k === "env") {
      const node = regEnv();
      const url = prompt("环境 " + s.env + " 的新 url（可含 {enterprise}）", node ? node.url : "");
      if (url == null || !url.trim()) return;
      try {
        await api("PUT", treeNodePath(k), { url: url.trim() });
        await loadRegistry();
        flash("PUT /registry/environments/" + s.env + " · 已改 url", "ok");
      } catch (err) {
        apiErr(err);
      }
      return;
    }
    const node = k === "ent" ? regEnt() : regTypes().find((x) => x.short_name === s.typ);
    const name = prompt("新的名称（简称是 wire 值，改不了）", node ? node.name : "");
    if (name == null || !name.trim()) return;
    try {
      await api("PUT", treeNodePath(k), { name: name.trim() });
      await loadRegistry();
      flash("已改名 " + name.trim(), "ok");
    } catch (err) {
      apiErr(err);
    }
  }

  async function treeDelete(k) {
    const cur = { env: state.regSel.env, ent: state.regSel.ent, typ: state.regSel.typ }[k];
    if (!confirm(`删除 ${cur}？被设备引用的节点删不掉。`)) return;
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

  async function addTemplate() {
    if (!state.config) return;
    const id = prompt("模板 id（只能字母数字下划线短横）", "tpl_" + (state.selectedId || "dev"));
    if (id == null || !id.trim()) return;
    // 模板体禁止 device_id / enterprise / device_type / server：挂靠由创建时的树引用决定。
    const dev = { ...state.config };
    delete dev.device_id;
    delete dev.enterprise;
    delete dev.device_type;
    delete dev.server;
    delete dev.environment;
    try {
      await api("POST", "/templates", { template_id: id.trim(), device: dev });
      flash("POST /templates · 已存 " + id.trim(), "ok");
      await loadTemplates();
      renderDrawer();
    } catch (err) {
      apiErr(err);
    }
  }

  async function delTemplate(id) {
    try {
      await api("DELETE", `/templates/${encodeURIComponent(id)}`);
      flash("DELETE /templates/" + id, "info");
      await loadTemplates();
      renderDrawer();
    } catch (err) {
      apiErr(err);
    }
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
    $("btn-mode-bubble").addEventListener("click", () => setMode("bubble"));
    $("btn-mode-cards").addEventListener("click", () => setMode("cards"));
    $("btn-help").addEventListener("click", () => openDrawer("help"));
    $("btn-scenarios").addEventListener("click", () => openDrawer("scenarios"));
    $("btn-new").addEventListener("click", () => openDrawer("new"));
    $("btn-assets").addEventListener("click", () => openDrawer("assets"));
    $("btn-templates").addEventListener("click", () => openDrawer("templates"));
    $("btn-config").addEventListener("click", () => openDrawer("config"));
    $("btn-faults").addEventListener("click", () => openDrawer("faults"));
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
    $("btn-batch-delete").addEventListener("click", () => batchAct("delete"));
  }

  function bindStage() {
    $("btn-start").addEventListener("click", startAndWait);
    $("btn-stop").addEventListener("click", stopDevice);
    $("btn-delete").addEventListener("click", deleteDevice);
    $("btn-interrupt").addEventListener("click", interrupt);
    $("form-speak").addEventListener("submit", speak);
    $("chk-sync").addEventListener("change", () => { state.syncWait = $("chk-sync").checked; });
    $("src-select").addEventListener("change", () => {
      state.srcKey = $("src-select").value;
      if (state.srcKey === "local") $("wav-file").click();
      renderSrc();
      renderStage();
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
      const add = hit("data-tree-add");
      if (add) { state.adding = state.adding === add ? null : add; renderDrawer(); return; }
      if (hit("data-tree-cancel")) { state.adding = null; renderDrawer(); return; }
      const ok = hit("data-tree-ok");
      if (ok) { treeAdd(ok); return; }
      const rn = hit("data-tree-rename");
      if (rn) { treeEdit(rn); return; }
      const del = hit("data-tree-del");
      if (del) { treeDelete(del); return; }
      const asset = hit("data-asset");
      if (asset) {
        const row = t.closest("[data-id]");
        if (row) assetAction(asset, row.dataset.id);
        return;
      }
      const sc = hit("data-scenario");
      if (sc !== null) { runScenario(Number(sc)); return; }
      const tdel = hit("data-tpl-del");
      if (tdel) { delTemplate(tdel); return; }
      const fault = t.closest("[data-fault]");
      if (fault) { injectFault(fault.getAttribute("data-fault")); return; }
      const act = hit("data-act");
      if (act === "create") createFromDrawer();
      else if (act === "close") closeDrawer();
      else if (act === "report") reportMode();
      else if (act === "tpl-add") addTemplate();
    });
    body.addEventListener("change", (e) => {
      const t = e.target;
      const tree = t.getAttribute && t.getAttribute("data-tree");
      if (tree) {
        if (tree === "env") state.regSel = { env: t.value, ent: "", typ: "" };
        else if (tree === "ent") { state.regSel.ent = t.value; state.regSel.typ = ""; }
        else state.regSel.typ = t.value;
        state.adding = null;
        renderDrawer();
        return;
      }
      const lib = t.getAttribute && t.getAttribute("data-lib");
      if (lib === "format") { state.lib.format = t.value; loadLibrary().catch(() => {}); return; }
      if (t.id === "lib-file" && t.files[0]) {
        const f = t.files[0];
        importAsset(f, f.name.replace(/\.[^.]+$/, ""), "").catch(apiErr);
        return;
      }
      if (t.name === "environment") cascadeConfig("env");
      else if (t.name === "enterprise") cascadeConfig("ent");
    });
    body.addEventListener("input", debounce((e) => {
      const t = e.target;
      if (t.getAttribute && t.getAttribute("data-lib") === "lang") {
        state.lib.language = t.value.trim();
        loadLibrary().catch(() => {});
      }
    }, 250));
    body.addEventListener("submit", (e) => {
      e.preventDefault();
      if (e.target.id === "form-config") saveConfig(e);
      else if (e.target.id === "form-create") createFromDrawer();
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
    bindKeys();
    setFollow(true);
    setMode("bubble");
    showSide("tape");
    await loadRegistry();
    await loadTemplates();
    await loadSamples();
    await loadLibrary();
    try {
      await refreshList();
    } catch (err) {
      flash("列表失败：" + err.message, "err");
    }
    const { id, ins } = parseHash();
    if (id) await selectDevice(id, ins);
    renderStage();
    renderConv();
    renderTape();
    setInterval(() => {
      if (document.hidden) return;
      refreshList().catch(() => {});
    }, 2000);
  }

  init();
})();
