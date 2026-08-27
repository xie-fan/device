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
    ws: null,
    wsKey: "",
    assetId: null,
    wavName: "",
    templates: [],
    registry: [],
    regSel: { env: "", ent: "", typ: "" },
    busy: false,
    deleteArmed: false,
    filterEnv: "",
    filterEnterprise: "",
    filterType: "",
    rosterQ: "",
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
    // 全局事件总线（Phase 4）：切到「全局」页签才连；断线记住 global_seq 续传。
    globalWs: null,
    globalEvents: [],
    globalNewest: 0,
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

  function setCount(el, n) {
    if (!el) return;
    el.hidden = !n;
    setText(el, n ? String(n) : "");
  }

  // ingestEvent 是高频路径，合帧后再画；用户动作仍走同步渲染，
  // 免得按钮 disabled 晚一帧被点第二下。
  const dirty = { roster: false, talk: false, tape: false, turns: false, global: false };
  let frame = 0;
  let frameTimer = 0;

  function flushPaint() {
    if (!frame) return;
    cancelAnimationFrame(frame);
    clearTimeout(frameTimer);
    frame = 0;
    frameTimer = 0;
    const d = { ...dirty };
    dirty.roster = dirty.talk = dirty.tape = dirty.turns = dirty.global = false;
    if (d.roster) renderRoster();
    if (d.talk) renderTalk();
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
    box.hidden = !msg;
    box.dataset.kind = msg ? (kind || "info") : "";
    // 出错的话留着让人看清；成功提示自己退场。
    if (msg && kind !== "err") {
      state.flashTimer = setTimeout(() => { box.hidden = true; }, 6000);
    }
  }

  function setWsNote(msg, tone) {
    const el = $("ws-note");
    setText(el, msg || "未连接事件流");
    el.dataset.tone = tone || "idle";
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

  function identityEditable() {
    if (state.tombstone || !state.live) return false;
    const st = state.live.instance_state;
    return st === "created" || st === "stopped";
  }

  function speakableUI() {
    if (state.tombstone || !state.live) return false;
    if (state.live.instance_state === "starting") return false;
    // backlog 开启（depth>0）时槽占用仍可送出：进队列排队。
    if (state.occupiedTurnId && !backlogEnabled()) return false;
    if (state.live.instance_state !== "running") return false;
    return true;
  }

  function backlogEnabled() {
    const beh = (state.config && state.config.behavior) || {};
    return Number(beh.speak_backlog_depth) > 0;
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

  function uniqueSorted(arr) {
    return [...new Set(arr.filter((v) => v != null && String(v) !== ""))].sort((a, b) => String(a).localeCompare(String(b), "zh"));
  }

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
    });
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
    renderAttach();
    renderConfigForm();
  }

  function optionsHTML(rows, value, label) {
    return rows.map((r) => `<option value="${esc(value(r))}">${esc(label(r))}</option>`).join("");
  }

  function renderAttach() {
    const selEnv = $("sel-env");
    if (!selEnv) return;
    const selEnt = $("sel-ent");
    const selTyp = $("sel-typ");
    selEnv.innerHTML = state.registry.length
      ? optionsHTML(state.registry, (r) => r.name, (r) => `${r.name} · ${r.url}`)
      : `<option value="">（先添加环境）</option>`;
    selEnv.value = state.regSel.env;
    const ents = regEnts();
    selEnt.innerHTML = ents.length
      ? optionsHTML(ents, (r) => r.short_name, (r) => `${r.name}（${r.short_name}）`)
      : `<option value="">（先添加厂商）</option>`;
    selEnt.value = state.regSel.ent;
    const typs = regTypes();
    selTyp.innerHTML = typs.length
      ? optionsHTML(typs, (r) => r.short_name, (r) => `${r.name}（${r.short_name}）`)
      : `<option value="">（先添加类型）</option>`;
    selTyp.value = state.regSel.typ;
    const hint = $("attach-hint");
    const missing = !state.regSel.env || !state.regSel.ent || !state.regSel.typ;
    hint.hidden = !missing;
    if (missing) setText(hint, "挂靠不完整：环境 / 厂商 / 设备类型三级都选好才能创建设备。");
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
    const sel = $("template-select");
    const cur = sel.value;
    sel.innerHTML = `<option value="">（无模板）</option>` +
      state.templates.map((id) => `<option value="${esc(id)}">${esc(id)}</option>`).join("");
    if (cur && state.templates.includes(cur)) sel.value = cur;
  }

  async function refreshList() {
    const data = await api("GET", "/devices");
    state.devices = data.devices || [];
    renderRoster();
    if (state.selectedId && !state.tombstone) {
      const row = state.devices.find((d) => d.device_id === state.selectedId);
      if (row) {
        state.live = row;
        if (row.instance_id) state.instanceId = row.instance_id;
        if (row.conn_generation != null) state.connGeneration = row.conn_generation;
        renderTalk();
      } else if (state.instanceId) {
        state.tombstone = true;
        state.live = null;
        closeWS(false);
        writeHash();
        renderTalk();
      }
    }
  }

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
    const envHTML = `<option value="">全部环境</option>` +
      envs.map((v) => `<option value="${esc(v)}">${esc(v)}</option>`).join("");
    const entHTML = `<option value="">全部厂商</option>` +
      enterprises.map((v) => `<option value="${esc(v)}">${esc(v)}</option>`).join("");
    const typeHTML = `<option value="">全部类型</option>` +
      types.map((v) => `<option value="${esc(v)}">${esc(v)}</option>`).join("");
    if (envSel.dataset.sig !== envHTML) {
      envSel.innerHTML = envHTML;
      envSel.dataset.sig = envHTML;
    }
    if (entSel.dataset.sig !== entHTML) {
      entSel.innerHTML = entHTML;
      entSel.dataset.sig = entHTML;
    }
    if (typeSel.dataset.sig !== typeHTML) {
      typeSel.innerHTML = typeHTML;
      typeSel.dataset.sig = typeHTML;
    }
    if (document.activeElement !== envSel) envSel.value = state.filterEnv;
    if (document.activeElement !== entSel) entSel.value = state.filterEnterprise;
    if (document.activeElement !== typeSel) typeSel.value = state.filterType;
  }

  function renderRoster() {
    const ul = $("roster-list");
    const empty = $("roster-empty");
    renderFilters();
    const shown = filteredDevices();
    setCount($("roster-count"), state.devices.length);
    if (state.devices.length === 0) {
      empty.hidden = false;
      setText(empty, "还没有设备。底部新建后启动，再送 WAV。");
    } else if (shown.length === 0) {
      empty.hidden = false;
      setText(empty, state.rosterQ ? "没有匹配搜索的设备。" : "没有匹配筛选的设备。");
    } else {
      empty.hidden = true;
    }
    setHTML(ul, shown.map((d) => {
      const on = d.device_id === state.selectedId && !state.tombstone;
      const err = d.last_error ? `<span class="stamp stamp--err" title="${esc(d.last_error)}">error</span>` : "";
      const st = String(d.instance_state || "created");
      const tip = [shortId(d.instance_id), fmtTime(d.last_activity)].filter(Boolean).join(" · ");
      const meta = [d.environment, d.enterprise, d.device_type].filter(Boolean).join(" · ");
      const seen = fmtHMS(d.last_activity);
      return `<li class="device${on ? " is-on" : ""}" data-id="${esc(d.device_id)}" title="${esc(tip)}">
        <span class="led led--${esc(st)}" aria-hidden="true"></span>
        <div>
          <div class="device__id">${esc(d.device_id)}</div>
          ${meta ? `<div class="device__meta">${esc(meta)}</div>` : ""}
          <div class="stamps">
            <span class="stamp stamp--${esc(st)}">${esc(st)}</span>
            <span class="stamp">${esc(d.connection_state)}</span>
            ${err}
            ${seen ? `<span class="stamp stamp--time">${esc(seen)}</span>` : ""}
          </div>
        </div>
      </li>`;
    }).join(""));
  }

  function renderTalk() {
    const empty = $("talk-empty");
    const body = $("talk-body");
    if (!state.selectedId) {
      empty.hidden = false;
      body.hidden = true;
      setText($("talk-ids"), "");
      return;
    }
    empty.hidden = true;
    body.hidden = false;
    setText($("talk-title"), state.tombstone ? "历史" : "对话");
    setText($("talk-ids"), state.selectedId);
    const live = state.live || {};
    const st = state.tombstone ? "deleted" : (live.instance_state || "created");
    const conn = live.connection_state || "—";
    const err = live.last_error ? `<span class="stamp stamp--err">error</span>` : "";
    const backlog = Number(live.speak_backlog_len) || 0;
    const backlogBadge = backlog > 0
      ? `<span class="stamp" title="speak backlog 排队数">排队 ${backlog}</span>` : "";
    setHTML($("talk-facts"), `
      <span class="led led--${esc(st)}" aria-hidden="true"></span>
      <span class="stamp stamp--${esc(st)}">${esc(st)}</span>
      <span class="stamp">${esc(conn)}</span>
      <span class="mono" title="${esc(state.instanceId || "")}">${esc(shortId(state.instanceId) || "—")}</span>
      <span class="status__gen">gen ${esc(state.connGeneration ?? live.conn_generation ?? "—")}</span>
      ${backlogBadge}
      ${err}
    `);

    // last_error 原来只藏在 title 里，工位上根本看不见。
    const fault = $("talk-error");
    if (live.last_error) {
      fault.hidden = false;
      setText(fault, live.last_error);
    } else {
      fault.hidden = true;
    }

    const running = !state.tombstone && st === "running";
    const starting = !state.tombstone && st === "starting";
    $("form-speak").hidden = !!state.tombstone;
    // 下发结束或连接断开后仍保留夹具/送出，只禁用按钮。
    $("speak-work").hidden = !!state.tombstone;
    let hint = "";
    if (!state.tombstone && !running) {
      if (starting) hint = "Starting 时不能说话。";
      else if (st === "stopped" || conn === "disconnected") hint = "连接已结束。夹具和下行都还在，重新启动后可再送出。";
      else hint = "启动并 Ready 后可送出。";
    }
    $("speak-wait").hidden = !hint;
    setText($("speak-wait"), hint);

    const slot = $("slot-banner");
    if (state.occupiedTurnId) {
      slot.hidden = false;
      const q = backlog > 0 ? ` · 队列 ${backlog} 项` : "";
      setText(slot, `槽占用 ${state.occupiedTurnId} · 等 turn_terminal${q}`);
    } else {
      slot.hidden = true;
    }

    $("btn-start").disabled = state.tombstone || state.busy || !identityEditable();
    $("btn-stop").disabled = state.tombstone || state.busy;
    $("btn-delete").disabled = state.tombstone || state.busy;
    $("btn-speak").disabled = !speakableUI() || starting || state.busy;
    $("btn-speak").title = starting ? "Starting 时禁用说话"
      : (state.occupiedTurnId
        ? (backlogEnabled() ? "槽占用 · 送出将进队列" : "槽占用，等 turn_terminal")
        : "Ctrl/⌘ + Enter 送出");
    $("btn-interrupt").disabled = !interruptableUI() || state.busy;
    $("btn-report").disabled = state.tombstone || live.connection_state !== "ready" || state.busy;
    $("wav-file").disabled = state.tombstone;
    renderConfigForm();
    renderTurns();
  }

  function renderConfigForm() {
    const form = $("form-config");
    const cfg = state.config;
    if (!cfg) {
      setHTML(form, "");
      return;
    }
    const lock = !identityEditable();
    const audio = cfg.audio || {};
    const server = cfg.server || {};
    const beh = cfg.behavior || {};
    const rec = cfg.recording || {};
    const uuid = cfg.uuid || {};
    // 挂靠三选来自配置树；当前值可能已不在树上（节点被删不掉——有引用；
    // 这里仍兜底把当前值补进选项，避免显示空白）。
    const withCurrent = (rows, val, mk) => {
      if (val && !rows.some((r) => mk(r) === val)) return rows.concat([{ __cur: val }]);
      return rows;
    };
    const envRows = withCurrent(state.registry, cfg.environment || "", (r) => r.__cur || r.name);
    const envOpts = envRows.map((r) => {
      const v = r.__cur || r.name;
      return `<option value="${esc(v)}"${v === (cfg.environment || "") ? " selected" : ""}>${esc(r.__cur ? v : `${r.name} · ${r.url}`)}</option>`;
    }).join("");
    const curEnv = state.registry.find((x) => x.name === (cfg.environment || ""));
    const entRows = withCurrent((curEnv && curEnv.enterprises) || [], cfg.enterprise || "", (r) => r.__cur || r.short_name);
    const entOpts = entRows.map((r) => {
      const v = r.__cur || r.short_name;
      return `<option value="${esc(v)}"${v === (cfg.enterprise || "") ? " selected" : ""}>${esc(r.__cur ? v : `${r.name}（${r.short_name}）`)}</option>`;
    }).join("");
    const curEnt = ((curEnv && curEnv.enterprises) || []).find((x) => x.short_name === (cfg.enterprise || ""));
    const typRows = withCurrent((curEnt && curEnt.device_types) || [], cfg.device_type || "", (r) => r.__cur || r.short_name);
    const typOpts = typRows.map((r) => {
      const v = r.__cur || r.short_name;
      return `<option value="${esc(v)}"${v === (cfg.device_type || "") ? " selected" : ""}>${esc(r.__cur ? v : `${r.name}（${r.short_name}）`)}</option>`;
    }).join("");
    // 签名只由 state.config、registry 与锁态决定，所以用户手打的值不会被事件流冲掉。
    const fresh = setHTML(form, `
      <fieldset ${lock ? "disabled" : ""}>
        <legend>挂靠 / 音频 / 身份（仅 Created、Stopped）</legend>
        <label>device_id（不可改）
          <input value="${esc(cfg.device_id)}" disabled>
        </label>
        <label>环境<select name="environment" data-cascade="env">${envOpts}</select></label>
        <div class="split">
          <label>厂商<select name="enterprise" data-cascade="ent">${entOpts}</select></label>
          <label>设备类型<select name="device_type">${typOpts}</select></label>
        </div>
        <div class="split">
          <label>firmware_version<input name="firmware_version" value="${esc(cfg.firmware_version || "")}"></label>
          <label>nic_type<input name="nic_type" value="${esc(cfg.nic_type || "")}"></label>
        </div>
        <label>nic_iccid<input name="nic_iccid" value="${esc(cfg.nic_iccid || "")}"></label>
        <label>server.url（由环境派生，start 时重解析）
          <input value="${esc(server.url || "")}" disabled>
        </label>
        <div class="split">
          <label>playing_mode<input name="playing_mode" type="number" min="1" max="3" value="${esc(cfg.playing_mode ?? 1)}"></label>
          <label>sample_rate<input name="sample_rate" type="number" value="${esc(audio.sample_rate ?? 16000)}"></label>
        </div>
        <div class="split">
          <label>slice_ms<input name="slice_ms" type="number" value="${esc(audio.slice_ms ?? 100)}"></label>
          <label>max_payload_size<input name="max_payload_size" type="number" value="${esc(audio.max_payload_size ?? 51200)}"></label>
        </div>
        <label>线上格式（wav=整段一次加头再切流）
          <select name="audio_format">
            <option value="pcm"${(audio.format || "pcm") === "pcm" ? " selected" : ""}>pcm</option>
            <option value="wav"${audio.format === "wav" ? " selected" : ""}>wav</option>
          </select>
        </label>
        <p class="hint">channels=${esc(audio.channels)} · sample_format=${esc(audio.sample_format)}</p>
        <div class="split">
          <label>uuid.min<input name="uuid_min" type="number" value="${esc(uuid.min ?? 1)}"></label>
          <label>uuid.max<input name="uuid_max" type="number" value="${esc(uuid.max ?? 2147483647)}"></label>
        </div>
        <div class="split">
          <label>keepalive_interval_sec<input name="keepalive_interval_sec" type="number" value="${esc(beh.keepalive_interval_sec ?? 60)}"></label>
          <label>first_reply_timeout_sec<input name="first_reply_timeout_sec" type="number" value="${esc(beh.first_reply_timeout_sec ?? 20)}"></label>
        </div>
        <label>speak_backlog_depth（0=关闭，槽占用 409；>0 排队）
          <input name="speak_backlog_depth" type="number" min="0" max="64" value="${esc(beh.speak_backlog_depth ?? 0)}">
        </label>
        <label class="chk"><input type="checkbox" name="silence_probe" ${beh.silence_probe ? "checked" : ""}> silence_probe（timeout 静默终态后发探针 report）</label>
        <label class="chk"><input type="checkbox" name="interrupt_on_disconnect" ${beh.interrupt_on_disconnect ? "checked" : ""}> interrupt_on_disconnect（事件 WS 断开即打断当前 turn）</label>
      </fieldset>
      <fieldset>
        <legend>录音（Running 也可改）</legend>
        <label class="chk"><input type="checkbox" name="enable_frame_log" ${rec.enable_frame_log ? "checked" : ""}> enable_frame_log</label>
        <label class="chk"><input type="checkbox" name="save_uplink_audio" ${rec.save_uplink_audio ? "checked" : ""}> save_uplink_audio</label>
        <label class="chk"><input type="checkbox" name="save_downlink_audio" ${rec.save_downlink_audio ? "checked" : ""}> save_downlink_audio</label>
        <label>output_dir<input name="output_dir" value="${esc(rec.output_dir || "")}"></label>
      </fieldset>
      <button type="submit" class="btn btn--primary" ${state.tombstone ? "disabled" : ""}>保存配置</button>
    `);
    const rm = $("report-mode");
    if (fresh && rm && cfg.playing_mode != null && document.activeElement !== rm) {
      rm.value = cfg.playing_mode;
    }
  }

  function readConfigForm() {
    const f = $("form-config");
    const fd = new FormData(f);
    const rec = {
      enable_frame_log: f.querySelector('[name="enable_frame_log"]').checked,
      save_uplink_audio: f.querySelector('[name="save_uplink_audio"]').checked,
      save_downlink_audio: f.querySelector('[name="save_downlink_audio"]').checked,
      output_dir: fd.get("output_dir"),
    };
    if (!identityEditable()) return { recording: rec };
    return {
      environment: fd.get("environment"),
      enterprise: fd.get("enterprise"),
      device_type: fd.get("device_type"),
      firmware_version: fd.get("firmware_version"),
      nic_type: fd.get("nic_type"),
      nic_iccid: fd.get("nic_iccid"),
      playing_mode: Number(fd.get("playing_mode")),
      audio: {
        format: fd.get("audio_format") || "pcm",
        sample_rate: Number(fd.get("sample_rate")),
        channels: 1,
        sample_format: "s16le",
        slice_ms: Number(fd.get("slice_ms")),
        max_payload_size: Number(fd.get("max_payload_size")),
      },
      uuid: { min: Number(fd.get("uuid_min")), max: Number(fd.get("uuid_max")) },
      behavior: {
        keepalive_interval_sec: Number(fd.get("keepalive_interval_sec")),
        first_reply_timeout_sec: Number(fd.get("first_reply_timeout_sec")),
        speak_backlog_depth: Number(fd.get("speak_backlog_depth")) || 0,
        silence_probe: f.querySelector('[name="silence_probe"]').checked,
        interrupt_on_disconnect: f.querySelector('[name="interrupt_on_disconnect"]').checked,
      },
      recording: rec,
    };
  }

  // 配置表单里换环境/厂商时就地重建下游选项（不整块重渲，保住手打的值）。
  function updateConfigCascade(changed) {
    const form = $("form-config");
    const envSel = form.querySelector('[name="environment"]');
    const entSel = form.querySelector('[name="enterprise"]');
    const typSel = form.querySelector('[name="device_type"]');
    if (!envSel || !entSel || !typSel) return;
    const env = state.registry.find((x) => x.name === envSel.value);
    if (changed === "env") {
      const ents = (env && env.enterprises) || [];
      entSel.innerHTML = ents.length
        ? optionsHTML(ents, (r) => r.short_name, (r) => `${r.name}（${r.short_name}）`)
        : `<option value="">（该环境下无厂商）</option>`;
    }
    const ent = ((env && env.enterprises) || []).find((x) => x.short_name === entSel.value);
    const typs = (ent && ent.device_types) || [];
    typSel.innerHTML = typs.length
      ? optionsHTML(typs, (r) => r.short_name, (r) => `${r.name}（${r.short_name}）`)
      : `<option value="">（该厂商下无类型）</option>`;
  }

  function downlinkHref(turnId) {
    return `/devices/${encodeURIComponent(state.selectedId || "")}/turns/${encodeURIComponent(turnId)}/audio/downlink?instance_id=${encodeURIComponent(state.instanceId || "")}`;
  }

  function renderTurns() {
    const ul = $("turns");
    setCount($("turns-count"), state.turns.length);
    if (!state.turns.length) {
      setHTML(ul, `<li class="empty">还没有 Turn。</li>`);
      return;
    }
    setHTML(ul, state.turns.map((t) => {
      const canPlay = t.reply_kind && String(t.reply_kind).includes("tts");
      const href = downlinkHref(t.turn_id);
      const on = state.occupiedTurnId === t.turn_id;
      return `<li${on ? ` class="is-live"` : ""}>
        <strong class="mono">${esc(t.turn_id)}</strong>
        <span>${esc(t.reply_kind || "（未终态）")} · ${esc(t.turn_end_reason || "—")} / ${esc(t.uplink_end_reason || "—")}</span>
        ${canPlay ? `<span class="turns__acts">
          <button type="button" class="btn btn--sm" data-play="${esc(t.turn_id)}" data-href="${esc(href)}">播放下行</button>
          <a class="btn btn--sm" href="${esc(href)}" download="${esc(t.turn_id)}-downlink.wav">下载</a>
        </span>` : ""}
      </li>`;
    }).join(""));
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

  function packetRowsHTML(packets) {
    if (!packets.length) {
      return `<li class="tl__pkt tl__pkt--empty">还没有写入帧日志</li>`;
    }
    return packets.map((p, i) => `<li class="tl__pkt">
      <time datetime="${esc(p.ts || "")}">${esc(fmtClock(p.ts))}</time>
      <span class="tl__n">#${i + 1}</span>
      <span class="tl__sz">${esc(fmtBytes(p.payload_len))}</span>
    </li>`).join("");
  }

  function burstHTML(row) {
    const key = row.key;
    const open = state.tapeOpen.has(key) ? " open" : "";
    const n = row.packets.length;
    const bytes = row.bytes;
    const live = row.live ? " · 进行中" : "";
    const sum = n ? `${n} 包 · ${fmtBytes(bytes)}${live}` : (row.live ? "进行中" : "0 包");
    const extra = row.turn_id ? ` · ${row.turn_id}` : "";
    return `<li class="tl tl--pkt tl--${esc(row.kind)}">
      <details data-key="${esc(key)}"${open}>
        <summary>
          <time datetime="${esc(row.ts || "")}">${esc(fmtClock(row.ts))}</time>
          <div>
            <div class="tl__title">${esc(row.label)}</div>
            <div class="hint">${esc(sum)}${esc(extra)}</div>
          </div>
        </summary>
        <ol class="tl__pkts">${packetRowsHTML(row.packets)}</ol>
      </details>
    </li>`;
  }

  function eventHTML(ev) {
    const extra = [ev.turn_id, ev.reply_kind, ev.turn_end_reason, ev.reason].filter(Boolean).join(" · ");
    return `<li class="tl tl--ev event--${esc(ev.event_type)}">
      <time datetime="${esc(ev.ts || "")}">${esc(fmtClock(ev.ts))}</time>
      <div>
        <div class="tl__title">${esc(ev.event_type)}</div>
        ${extra ? `<div class="hint">${esc(extra)}</div>` : ""}
      </div>
    </li>`;
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
        sig: `${key}|${pkts.length}|${bytes}|${live ? 1 : 0}`,
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
          sig: `${key}|${packets.length}|${bytes}`,
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
    btn.classList.toggle("is-on", state.tapeFollow);
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
    setCount($("tape-count"), state.events.length);

    if (!rows.length) {
      ol.classList.remove("tape--live");
      state.tapeRows = [];
      const none = state.events.length ? "没有匹配筛选的事件。" : "还没有事件。";
      const html = `<li class="tl tl--empty"><span class="empty">${esc(none)}</span></li>`;
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
    flash(ok ? `已复制 ${n} 条事件（JSONL）` : "复制失败，请手动选中", ok ? "ok" : "err");
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
    renderTape();
    const tick = async () => {
      if (state.occupiedTurnId !== turnId) {
        stopFramePoll();
        return;
      }
      const changed = await loadFrames(turnId);
      if (changed) renderTape();
    };
    tick();
    state.framePoll = setInterval(tick, 400);
  }

  function resetTapeAux() {
    stopFramePoll();
    state.tapeOpen = new Set();
    state.framesByTurn = {};
    state.needFrames = new Set();
    state.tapeRows = [];
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
      setWsNote("device_deleted，连接已关闭", "done");
    }
    paint("tape", "talk");
  }

  async function refreshTurnsQuiet() {
    if (!state.selectedId || !state.instanceId) return;
    try {
      const data = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/turns?instance_id=${encodeURIComponent(state.instanceId)}`);
      state.turns = data.turns || [];
      renderTurns();
    } catch {
      /* tombstone 过期时保持现有列表 */
    }
  }

  function maybePlayDownlink(ev) {
    const kind = ev.reply_kind || "";
    if (!kind.includes("tts") || !ev.turn_id || !state.instanceId || !state.selectedId) return;
    playHref(downlinkHref(ev.turn_id), ev.turn_id);
  }

  function playHref(href, turnId) {
    const audio = $("downlink-audio");
    const box = $("player-box");
    box.hidden = false;
    setText($("player-label"), "下行 · " + turnId);
    const dl = $("player-dl");
    dl.hidden = false;
    dl.href = href;
    dl.setAttribute("download", turnId + "-downlink.wav");
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
    setWsNote(fromOldest ? "事件从 oldest 回放" : "只收新事件", "live");
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
    state.deleteArmed = false;
    setText($("btn-delete"), "删除");
    $("btn-delete").dataset.armed = "0";
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
      renderTalk();
    } catch (err) {
      if (err.status === 404) {
        await openTombstone(id, insHint || state.instanceId);
        return;
      }
      flash(err.message, "err");
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
    renderTalk();
    flash("live 已摘除。TTL 内用同一 instance_id 看历史，不要换到新实例。", "info");
  }

  async function loadConfigAndTurns(resetTape) {
    if (resetTape) {
      state.events = [];
      state.seenSeq = new Set();
      state.newestSeq = 0;
      resetTapeAux();
      closeWS(false);
      setFollow(true);
      renderTape();
    }
    if (state.selectedId && !state.tombstone) {
      try {
        state.config = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}/config`);
      } catch (err) {
        state.config = null;
        flash(err.message, "err");
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

  function requireAttach() {
    const refs = attachRefs();
    if (!refs.environment || !refs.enterprise || !refs.device_type) {
      flash("先在上方选好 环境 / 厂商 / 设备类型", "err");
      return null;
    }
    return refs;
  }

  async function createDevice(ev) {
    ev.preventDefault();
    const refs = requireAttach();
    if (!refs) return;
    const fd = new FormData(ev.currentTarget);
    const id = String(fd.get("device_id") || "").trim();
    try {
      await api("POST", "/devices", {
        ...refs,
        device: defaultDevice(id, {
          firmware_version: String(fd.get("firmware_version") || "").trim(),
          nic_type: String(fd.get("nic_type") || "").trim(),
          nic_iccid: String(fd.get("nic_iccid") || "").trim(),
          playing_mode: Number(fd.get("playing_mode")),
          sample_rate: Number(fd.get("sample_rate")),
        }),
      });
      flash("已创建 " + id, "ok");
      $("create-box").open = false;
      await refreshList();
      await selectDevice(id);
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  async function createFromTemplate(ev) {
    ev.preventDefault();
    const refs = requireAttach();
    if (!refs) return;
    const fd = new FormData(ev.currentTarget);
    const template_id = String(fd.get("template_id") || "");
    if (!template_id) {
      flash("请选择模板", "err");
      return;
    }
    try {
      const data = await api("POST", "/devices", {
        ...refs,
        template_id,
        count: Number(fd.get("count")),
        id_prefix: String(fd.get("id_prefix") || "sim"),
      });
      flash("已创建 " + (data.device_ids || []).join(", "), "ok");
      await refreshList();
      if (data.device_ids && data.device_ids[0]) await selectDevice(data.device_ids[0]);
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  async function startAndWait() {
    if (!state.selectedId) return;
    state.busy = true;
    renderTalk();
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
      renderTalk();
      const ready = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/wait_ready`, {
        instance_id: started.instance_id,
        conn_generation: started.conn_generation,
      });
      flash("Ready · " + (ready.connection_state || "ready"), "ok");
      await refreshList();
      try {
        state.live = await api("GET", `/devices/${encodeURIComponent(state.selectedId)}`);
        state.instanceId = state.live.instance_id;
        state.connGeneration = state.live.conn_generation;
      } catch (err) {
        flash(err.message, "err");
      }
      await loadConfigAndTurns(false);
    } catch (err) {
      flash(err.status + " " + err.message, "err");
      await refreshList();
      renderTalk();
    } finally {
      state.busy = false;
      renderTalk();
    }
  }

  async function stopDevice() {
    if (!state.selectedId) return;
    state.busy = true;
    renderTalk();
    try {
      await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/stop`);
      state.occupiedTurnId = null;
      stopFramePoll();
      flash("已停止（不是故障）", "ok");
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
      flash(err.status + " " + err.message, "err");
    } finally {
      state.busy = false;
      renderTalk();
    }
  }

  async function deleteDevice() {
    if (!state.selectedId) return;
    const btn = $("btn-delete");
    if (btn.dataset.armed !== "1") {
      btn.dataset.armed = "1";
      setText(btn, "确认删除");
      state.deleteArmed = true;
      setTimeout(() => {
        if (btn.dataset.armed === "1") {
          btn.dataset.armed = "0";
          setText(btn, "删除");
        }
      }, 4000);
      return;
    }
    btn.dataset.armed = "0";
    setText(btn, "删除");
    const ins = state.instanceId;
    try {
      await api("DELETE", `/devices/${encodeURIComponent(state.selectedId)}`);
      flash("已删除。等 device_deleted 后再关 WS。", "ok");
      state.tombstone = true;
      state.instanceId = ins;
      writeHash();
      await refreshList();
      renderTalk();
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  async function loadSamples() {
    const sel = $("sample-select");
    if (!sel) return;
    try {
      const data = await api("GET", "/samples");
      const rows = data.samples || [];
      sel.innerHTML = `<option value="">（选一条）</option>` +
        rows.map((s) => `<option value="${esc(s.url)}">${esc(s.name)}</option>`).join("");
      const prefer = rows.find((s) => s.name.indexOf("你叫什么") >= 0) || rows[0];
      if (prefer) sel.value = prefer.url;
    } catch {
      sel.innerHTML = `<option value="">（无夹具）</option>`;
    }
  }

  function attachFile(file) {
    const dt = new DataTransfer();
    dt.items.add(file);
    $("wav-file").files = dt.files;
    state.assetId = null;
    state.wavName = file.name;
  }

  async function useSample() {
    const sel = $("sample-select");
    const url = sel && sel.value;
    if (!url) {
      flash("先选一条夹具 WAV", "err");
      return;
    }
    const name = sel.options[sel.selectedIndex] ? sel.options[sel.selectedIndex].text : "sample.wav";
    try {
      const res = await fetch(url);
      if (!res.ok) {
        throw new Error("读不到 " + name);
      }
      const blob = await res.blob();
      attachFile(new File([blob], name, { type: "audio/wav" }));
      setText($("wav-meta"), name + " · 已选入");
      flash("已选用 " + name, "ok");
    } catch (err) {
      flash(err.message, "err");
    }
  }

  async function speak(ev) {
    ev.preventDefault();
    if (!speakableUI()) {
      flash("现在不能说话（Starting、未 Ready、或槽占用）", "err");
      return;
    }
    const file = $("wav-file").files[0];
    if (!file) {
      flash("请选择 WAV，或点「选用夹具」", "err");
      return;
    }
    state.busy = true;
    renderTalk();
    try {
      const fd = new FormData();
      fd.append("file", file, file.name);
      const asset = await api("POST", "/assets", fd);
      state.assetId = asset.asset_id;
      setText($("wav-meta"), `${file.name} · ${asset.sample_rate} Hz · ${asset.duration_ms} ms · ${asset.asset_id}`);
      const spoken = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/speak`, {
        asset_id: asset.asset_id,
      });
      if (spoken.queued) {
        // Phase 4 backlog：槽占用时进队列，终态后自动出队。
        flash(`已排队 ${spoken.turn_id} · 位置 ${spoken.queue_position} · 终态后自动出队`, "ok");
      } else {
        state.occupiedTurnId = spoken.turn_id;
        setFollow(true);
        startFramePoll(spoken.turn_id);
        flash("已受理 " + spoken.turn_id + " · 等 turn_terminal", "ok");
      }
      renderTalk();
    } catch (err) {
      const extra = err.data && err.data.instance_state
        ? ` (${err.data.instance_state}/${err.data.connection_state})`
        : "";
      flash(err.status + " " + err.message + extra, "err");
    } finally {
      state.busy = false;
      renderTalk();
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
        flash("没有活动 Turn", "ok");
      }
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  async function saveConfig(ev) {
    ev.preventDefault();
    if (!state.selectedId || state.tombstone) return;
    try {
      const body = readConfigForm();
      state.config = await api("PUT", `/devices/${encodeURIComponent(state.selectedId)}/config`, body);
      flash("配置已保存", "ok");
      renderConfigForm();
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  async function reportMode(ev) {
    ev.preventDefault();
    if (!state.selectedId) return;
    const mode = Number($("report-mode").value);
    try {
      const res = await api("POST", `/devices/${encodeURIComponent(state.selectedId)}/report`, {
        playingMode: mode,
      });
      flash("report seq " + res.sequence_number, "ok");
    } catch (err) {
      flash(err.status + " " + err.message, "err");
    }
  }

  function showSide(which) {
    const tape = which === "tape";
    const turns = which === "turns";
    const global = which === "global";
    $("tape").hidden = !tape;
    $("turns").hidden = !turns;
    $("global-tape").hidden = !global;
    $("tape-chips").hidden = !tape;
    $("tape-tools").hidden = !tape;
    $("tab-tape").classList.toggle("is-on", tape);
    $("tab-turns").classList.toggle("is-on", turns);
    $("tab-global").classList.toggle("is-on", global);
    $("tab-tape").setAttribute("aria-selected", String(tape));
    $("tab-turns").setAttribute("aria-selected", String(turns));
    $("tab-global").setAttribute("aria-selected", String(global));
    if (tape && state.tapeFollow) {
      const ol = $("tape");
      ol.scrollTop = ol.scrollHeight;
    }
    if (global) {
      connectGlobalWS();
      renderGlobalTape();
    }
  }

  // ——— 全局事件总线（Phase 4）———
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

  function globalEventHTML(ev) {
    const extra = [ev.turn_id, ev.reply_kind, ev.turn_end_reason, ev.reason].filter(Boolean).join(" · ");
    return `<li class="tl tl--ev event--${esc(ev.event_type)}">
      <time datetime="${esc(ev.ts || "")}">${esc(fmtClock(ev.ts))}</time>
      <div>
        <div class="tl__title">${esc(ev.event_type)}<span class="tl__dev mono">${esc(ev.device_id || "")}</span></div>
        <div class="hint">#${esc(ev.global_seq)}${extra ? " · " + esc(extra) : ""}</div>
      </div>
    </li>`;
  }

  function renderGlobalTape() {
    const ol = $("global-tape");
    setCount($("global-count"), state.globalEvents.length);
    if (ol.hidden) return;
    if (!state.globalEvents.length) {
      setHTML(ol, `<li class="tl tl--empty"><span class="empty">还没有全局事件。任意设备的动作都会汇到这里。</span></li>`);
      return;
    }
    if (setHTML(ol, state.globalEvents.map(globalEventHTML).join(""))) {
      ol.scrollTop = ol.scrollHeight;
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

  // ——— 绑定 ———

  function setScope(scope) {
    state.tapeScope = scope;
    for (const c of $("tape-chips").querySelectorAll(".chip")) {
      c.classList.toggle("is-on", c.dataset.scope === scope);
    }
    state.tapeRows = [];
    renderTape();
  }

  function debounce(fn, ms) {
    let t = 0;
    return (...a) => {
      clearTimeout(t);
      t = setTimeout(() => fn(...a), ms);
    };
  }

  function bind() {
    $("form-create").addEventListener("submit", createDevice);
    $("form-template").addEventListener("submit", createFromTemplate);
    $("btn-refresh").addEventListener("click", () => refreshList().catch((e) => flash(e.message, "err")));
    $("btn-start").addEventListener("click", startAndWait);
    $("btn-stop").addEventListener("click", stopDevice);
    $("btn-delete").addEventListener("click", deleteDevice);
    $("form-speak").addEventListener("submit", speak);
    $("btn-sample").addEventListener("click", useSample);
    $("btn-interrupt").addEventListener("click", interrupt);
    $("form-config").addEventListener("submit", saveConfig);
    $("form-report").addEventListener("submit", reportMode);
    $("btn-theme").addEventListener("click", cycleTheme);
    $("flash-x").addEventListener("click", () => flash(""));

    $("roster-q").addEventListener("input", debounce(() => {
      state.rosterQ = $("roster-q").value.trim().toLowerCase();
      renderRoster();
    }, 120));
    $("filter-env").addEventListener("change", () => {
      state.filterEnv = $("filter-env").value;
      renderRoster();
    });
    $("filter-enterprise").addEventListener("change", () => {
      state.filterEnterprise = $("filter-enterprise").value;
      renderRoster();
    });
    bindAttach();
    $("filter-type").addEventListener("change", () => {
      state.filterType = $("filter-type").value;
      renderRoster();
    });
    $("roster-list").addEventListener("click", (e) => {
      const row = e.target.closest(".device");
      if (!row) return;
      selectDevice(row.dataset.id);
    });

    $("tape").addEventListener("toggle", (e) => {
      const d = e.target;
      if (!(d instanceof HTMLDetailsElement) || !d.dataset.key || !d.isConnected) return;
      if (d.open) state.tapeOpen.add(d.dataset.key);
      else state.tapeOpen.delete(d.dataset.key);
    }, true);
    // 往回翻历史时自动松开跟随，别把人拽回底部。
    $("tape").addEventListener("scroll", () => {
      const on = nearBottom($("tape"));
      if (on === state.tapeFollow) return;
      state.tapeFollow = on;
      $("btn-follow").setAttribute("aria-pressed", String(on));
      $("btn-follow").classList.toggle("is-on", on);
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
      const btn = e.target.closest("[data-play]");
      if (!btn) return;
      playHref(btn.dataset.href, btn.dataset.play);
    });
    window.addEventListener("hashchange", () => {
      const { id, ins } = parseHash();
      if (id) selectDevice(id, ins);
    });
    $("wav-file").addEventListener("change", () => {
      const f = $("wav-file").files[0];
      state.assetId = null;
      state.wavName = f ? f.name : "";
      setText($("wav-meta"), f ? `${f.name} · ${fmtBytes(f.size)}` : "");
    });

    // 把 WAV 直接拖到作业区就行，不用走文件选择框。
    const talk = $("talk");
    const zone = $("drop-zone");
    talk.addEventListener("dragover", (e) => {
      if (!e.dataTransfer || state.tombstone) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
      zone.classList.add("is-drop");
    });
    talk.addEventListener("dragleave", (e) => {
      if (e.target === talk) zone.classList.remove("is-drop");
    });
    talk.addEventListener("drop", (e) => {
      zone.classList.remove("is-drop");
      if (state.tombstone) return;
      const f = e.dataTransfer && e.dataTransfer.files[0];
      if (!f) return;
      e.preventDefault();
      if (!/\.wav$/i.test(f.name)) {
        flash("只收 .wav", "err");
        return;
      }
      attachFile(f);
      setText($("wav-meta"), `${f.name} · ${fmtBytes(f.size)}`);
      flash("已选入 " + f.name, "ok");
    });

    document.addEventListener("keydown", (e) => {
      const t = e.target;
      const typing = t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
      if (e.key === "Escape") {
        flash("");
        return;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        if (!$("btn-speak").disabled) {
          e.preventDefault();
          $("form-speak").requestSubmit();
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
  }

  // 挂靠区（新建面板）：三选级联 + 内联快速添加节点。
  function bindAttach() {
    $("sel-env").addEventListener("change", () => {
      state.regSel.env = $("sel-env").value;
      state.regSel.ent = regEnts()[0] ? regEnts()[0].short_name : "";
      state.regSel.typ = regTypes()[0] ? regTypes()[0].short_name : "";
      renderAttach();
    });
    $("sel-ent").addEventListener("change", () => {
      state.regSel.ent = $("sel-ent").value;
      state.regSel.typ = regTypes()[0] ? regTypes()[0].short_name : "";
      renderAttach();
    });
    $("sel-typ").addEventListener("change", () => {
      state.regSel.typ = $("sel-typ").value;
      renderAttach();
    });
    const toggles = [
      ["btn-add-env", "form-add-env"],
      ["btn-add-ent", "form-add-ent"],
      ["btn-add-typ", "form-add-typ"],
    ];
    for (const [btn, form] of toggles) {
      $(btn).addEventListener("click", () => {
        const f = $(form);
        f.hidden = !f.hidden;
        $(btn).setAttribute("aria-expanded", String(!f.hidden));
        if (!f.hidden) f.querySelector("input").focus();
      });
    }
    $("form-add-env").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const fd = new FormData(ev.currentTarget);
      const name = String(fd.get("name") || "").trim();
      try {
        await api("POST", "/registry/environments", { name, url: String(fd.get("url") || "").trim() });
        state.regSel = { env: name, ent: "", typ: "" };
        ev.target.reset();
        $("form-add-env").hidden = true;
        await loadRegistry();
        flash("已添加环境 " + name, "ok");
      } catch (err) {
        flash(err.status + " " + err.message, "err");
      }
    });
    $("form-add-ent").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (!state.regSel.env) {
        flash("先选环境", "err");
        return;
      }
      const fd = new FormData(ev.currentTarget);
      const short = String(fd.get("short_name") || "").trim();
      try {
        await api("POST", `/registry/environments/${encodeURIComponent(state.regSel.env)}/enterprises`, {
          name: String(fd.get("name") || "").trim(),
          short_name: short,
        });
        state.regSel.ent = short;
        state.regSel.typ = "";
        ev.target.reset();
        $("form-add-ent").hidden = true;
        await loadRegistry();
        flash("已添加厂商 " + short, "ok");
      } catch (err) {
        flash(err.status + " " + err.message, "err");
      }
    });
    $("form-add-typ").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (!state.regSel.env || !state.regSel.ent) {
        flash("先选环境与厂商", "err");
        return;
      }
      const fd = new FormData(ev.currentTarget);
      const short = String(fd.get("short_name") || "").trim();
      try {
        await api(
          "POST",
          `/registry/environments/${encodeURIComponent(state.regSel.env)}/enterprises/${encodeURIComponent(state.regSel.ent)}/device_types`,
          { name: String(fd.get("name") || "").trim(), short_name: short },
        );
        state.regSel.typ = short;
        ev.target.reset();
        $("form-add-typ").hidden = true;
        await loadRegistry();
        flash("已添加类型 " + short, "ok");
      } catch (err) {
        flash(err.status + " " + err.message, "err");
      }
    });
    // 配置表单的挂靠级联（事件委托：表单会被整块重渲）。
    $("form-config").addEventListener("change", (ev) => {
      const t = ev.target;
      if (t && t.name === "environment") updateConfigCascade("env");
      else if (t && t.name === "enterprise") updateConfigCascade("ent");
    });
  }

  async function init() {
    applyTheme(readTheme());
    bind();
    setFollow(true);
    await loadRegistry();
    await loadTemplates();
    await loadSamples();
    try {
      await refreshList();
    } catch (err) {
      flash("列表失败：" + err.message, "err");
    }
    const { id, ins } = parseHash();
    if (id) await selectDevice(id, ins);
    renderTape();
    setInterval(() => {
      if (document.hidden) return;
      refreshList().catch(() => {});
    }, 2000);
  }

  init();
})();
