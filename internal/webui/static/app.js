"use strict";
const $ = (id) => document.getElementById(id),
  token = document.querySelector('meta[name="heron-token"]').content;
const state = {
  apps: [],
  busy: {},
  selected: "",
  filter: "all",
  page: "apps",
  tab: "overview",
  online: false,
  localBusy: new Set(),
  cleared: new Map(),
  logKey: "",
  version: "",
  editID: "",
  editText: "",
};
const el = (tag, text, cls) => {
  const n = document.createElement(tag);
  if (text !== undefined) n.textContent = text;
  if (cls) n.className = cls;
  return n;
};
const button = (text, fn, cls) => {
  const b = el("button", text, cls);
  b.type = "button";
  b.onclick = fn;
  return b;
};
function notice(text) {
  $("notice").hidden = !text;
  $("notice").textContent = text;
}
async function api(path, body) {
  const r = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "X-Heron-Token": token, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok) throw Error(await r.text());
  return r.json();
}
const current = () => state.apps.find((a) => a.ID === state.selected);
function select(id) {
  state.selected = id;
  state.tab = "overview";
  state.logKey = "";
  state.lastEntries = [];
  render();
  refreshLogs().catch((e) => notice(e.message));
}
const pending = (a) =>
  state.busy[a.ID] ||
  state.localBusy.has(a.ID) ||
  ["starting", "building", "stopping"].includes(a.Status);
function actionButton(a, action, label) {
  const b = button(label, () => act(a.ID, action));
  const blockers = a.ActiveDependents || [];
  b.disabled =
    !state.online || pending(a) || (action !== "start" && blockers.length > 0);
  b.title =
    action !== "start" && blockers.length
      ? "Required by " + blockers.join(", ")
      : action + " " + a.ID;
  b.setAttribute("aria-label", b.title);
  return b;
}
async function act(id, action) {
  state.localBusy.add(id);
  notice("");
  render();
  try {
    await api("action", { ID: id, Action: action });
  } catch (e) {
    notice(e.message);
  } finally {
    state.localBusy.delete(id);
    await refresh();
  }
}
function status(a) {
  return el("span", a.Status, "status " + a.Status);
}
function endpointLink(app, endpoint, detailed = false) {
  if (!endpoint.URL) {
    const text = el(
      "span",
      detailed
        ? `${endpoint.Name} · ${endpoint.Address}`
        : `${endpoint.Name} · ${endpoint.Protocol}`,
      "muted",
    );
    text.title = "Use a protocol client: " + endpoint.Address;
    return text;
  }
  const link = el(
    "a",
    detailed ? endpoint.URL + " ↗" : endpoint.Name + " ↗",
    "endpoint-link",
  );
  link.href = endpoint.URL;
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.title = "Open " + endpoint.Name + ": " + endpoint.URL;
  link.setAttribute(
    "aria-label",
    `Open ${app.ID} ${endpoint.Name} in a new tab`,
  );
  link.dataset.focus = `endpoint-${detailed ? "detail" : "table"}-${app.ID}-${endpoint.Name}`;
  return link;
}
function render() {
  const focused = document.activeElement;
  const focusKey = focused?.dataset?.focus;
  const counts = { all: state.apps.length };
  for (const a of state.apps) counts[a.Status] = (counts[a.Status] || 0) + 1;
  $("filters").replaceChildren(
    ...[
      "all",
      "running",
      "stopped",
      "failed",
      ...["building", "starting", "stopping"].filter((x) => counts[x]),
    ].map((key) => {
      const b = button(
        key[0].toUpperCase() + key.slice(1),
        () => {
          state.filter = key;
          render();
        },
        state.filter === key ? "active" : "",
      );
      b.dataset.focus = "filter-" + key;
      b.append(el("span", counts[key] || 0, "count"));
      return b;
    }),
  );
  const query = $("search").value.toLowerCase();
  const apps = state.apps.filter(
    (a) =>
      (state.filter === "all" || a.Status === state.filter) &&
      a.ID.toLowerCase().includes(query),
  );
  $("rows").replaceChildren(
    ...apps.map((a) => {
      const tr = el("tr", undefined, a.ID === state.selected ? "selected" : "");
      const name = el("td");
      const b = button(a.ID, () => select(a.ID));
      b.dataset.focus = "app-" + a.ID;
      b.append(
        el(
          "small",
          a.External
            ? "External / custom stop"
            : a.Endpoints?.length
              ? "Local service"
              : "Background process",
        ),
      );
      name.append(b);
      tr.append(name);
      const st = el("td");
      st.append(status(a));
      const endpointCell = el("td");
      const endpointList = el("div", undefined, "endpoint-list");
      for (const endpoint of a.Endpoints || [])
        endpointList.append(endpointLink(a, endpoint));
      if (!a.Endpoints?.length) endpointList.append(el("span", "—", "muted"));
      endpointCell.append(endpointList);
      tr.append(
        st,
        endpointCell,
        el("td", a.CPU == null ? "—" : a.CPU.toFixed(1) + "%"),
        el("td", a.RSS == null ? "—" : (a.RSS / 1048576).toFixed(1) + " MB"),
      );
      const ac = el("td");
      const action = ["stopped", "failed"].includes(a.Status)
        ? "start"
        : "stop";
      const ab = actionButton(a, action, action === "start" ? "▷" : "□");
      ab.dataset.focus = "action-" + a.ID;
      ac.append(ab);
      tr.append(ac);
      return tr;
    }),
  );
  $("empty").hidden = apps.length > 0;
  $("empty").textContent = !state.online
    ? "Runtime disconnected. Reconnecting…"
    : state.apps.length
      ? "No applications match this filter."
      : "No apps configured. Add your first application.";
  $("totals").textContent =
    `${state.apps.length} apps · ${counts.running || 0} running`;
  renderInspector();
  if (focusKey) {
    for (const n of document.querySelectorAll("[data-focus]"))
      if (n.dataset.focus === focusKey) {
        n.focus({ preventScroll: true });
        break;
      }
  }
}
function renderInspector() {
  const root = $("inspector"),
    scroll = root.scrollTop,
    a = current();
  root.replaceChildren();
  if (!a) {
    root.append(
      el(
        "p",
        "Select an application to inspect its state, dependencies, and logs.",
      ),
    );
    return;
  }
  const head = el("div", undefined, "inspector-head");
  head.append(el("h2", a.ID), status(a));
  root.append(
    head,
    el(
      "p",
      a.External
        ? "Externally managed application"
        : "Managed local application",
    ),
  );
  const actions = el("div", undefined, "actions");
  for (const action of [
    ["stopped", "failed"].includes(a.Status) ? "start" : "stop",
    "restart",
  ]) {
    const b = actionButton(
      a,
      action,
      action[0].toUpperCase() + action.slice(1),
    );
    b.dataset.focus = "inspector-" + action;
    actions.append(b);
  }
  root.append(actions);
  if (a.ActiveDependents?.length)
    root.append(
      el(
        "div",
        "Required by " +
          a.ActiveDependents.length +
          " active app(s)\nStop " +
          a.ActiveDependents.join(", ") +
          " first. Restart is also blocked.",
        "warning",
      ),
    );
  const tabs = el("div", undefined, "tabs");
  for (const name of ["overview", "configuration"]) {
    const b = button(
      name[0].toUpperCase() + name.slice(1),
      () => {
        state.tab = name;
        renderInspector();
      },
      state.tab === name ? "active" : "",
    );
    b.dataset.focus = "tab-" + name;
    tabs.append(b);
  }
  root.append(tabs);
  if (state.tab === "configuration") {
    root.append(
      el(
        "p",
        "Inspect and edit the app’s source YAML. Environment values stay hidden until you explicitly reveal the editor.",
      ),
    );
    const b = button("Open configuration", () => openEditor(a.ID));
    b.disabled = !state.online;
    root.append(b);
    root.scrollTop = scroll;
    return;
  }
  const dl = el("dl");
  const fields = [
    ["Mode", a.External ? "Custom stop command" : "Process group"],
    ["PID", a.PID || "—"],
    ["Uptime", a.Status === "running" ? uptime(a.StartedAt) : "—"],
    ["Directory", a.Pwd || "—"],
    ["Idle timeout", a.Idle],
    ["Restarts", a.Restarts],
  ];
  for (const [k, v] of fields) dl.append(el("dt", k), el("dd", v));
  root.append(dl);
  const endpoints = el("section", undefined, "section");
  endpoints.append(el("h3", "Endpoints"));
  if (!a.Endpoints?.length)
    endpoints.append(el("p", "Process only · no network endpoint", "muted"));
  else
    for (const e of a.Endpoints) {
      const entry = el("div", undefined, "endpoint-detail");
      entry.append(el("strong", `${e.Name}${e.Primary ? " · Primary" : ""}`));
      entry.append(endpointLink(a, e, true));
      entry.append(el("span", `${e.Protocol} · backend :${e.Port}`, "muted"));
      endpoints.append(entry);
    }
  root.append(endpoints);
  for (const [title, ids] of [
    ["Dependencies", a.DependsOn],
    ["Kept alive by · direct", a.ActiveDependents],
    ["Transitive active dependents", transitive(a.ID)],
  ]) {
    const section = el("section", undefined, "section");
    section.append(el("h3", title));
    const chips = el("div", undefined, "chips");
    for (const id of ids || []) {
      const target = state.apps.find((x) => x.ID === id);
      chips.append(
        button(id + " · " + (target?.Status || "unknown"), () => select(id)),
      );
    }
    if (!ids?.length) chips.append(el("span", "None", "muted"));
    section.append(chips);
    root.append(section);
  }
  root.scrollTop = scroll;
}
function transitive(id) {
  const seen = new Set(),
    direct = new Set(
      state.apps.find((a) => a.ID === id)?.ActiveDependents || [],
    );
  function walk(key) {
    for (const d of state.apps.find((a) => a.ID === key)?.ActiveDependents ||
      [])
      if (!seen.has(d)) {
        seen.add(d);
        walk(d);
      }
  }
  walk(id);
  return [...seen].filter((x) => !direct.has(x));
}
function uptime(at) {
  const mins = Math.max(0, Math.floor((Date.now() - Date.parse(at)) / 60000));
  return mins >= 60 ? `${Math.floor(mins / 60)}h ${mins % 60}m` : `${mins}m`;
}
async function refresh() {
  try {
    const data = await api("state");
    state.apps = data.apps || [];
    state.busy = data.busy || {};
    state.online = true;
    $("config-path").textContent = data.configPath;
    $("connection").textContent = "Connected";
    $("connection").className = "connected";
    if (!current()) state.selected = state.apps[0]?.ID || "";
    render();
    await refreshLogs();
  } catch (e) {
    state.online = false;
    $("connection").textContent = "Disconnected · retrying";
    $("connection").className = "";
    $("live").textContent = "Disconnected";
    render();
  }
}
async function refreshLogs() {
  const id = state.selected,
    page = state.page;
  if (!id) {
    $("log-output").textContent = "Select an app to view output.";
    return;
  }
  const entries = await api("logs?app=" + encodeURIComponent(id));
  if (id !== state.selected) return;
  const output = $("log-output"),
    filter = $("log-filter").value.toLowerCase(),
    cut = state.cleared.get(id) || 0;
  state.lastEntries = entries || [];
  const text =
    (entries || [])
      .filter((e) => e.Seq > cut && e.Text.toLowerCase().includes(filter))
      .map(
        (e) =>
          new Date(e.At).toLocaleTimeString() +
          "  " +
          e.Stream.padEnd(6) +
          "  " +
          e.Text,
      )
      .join("\n") || "No output yet.";
  const key = id + "|" + filter + "|" + cut;
  // Freeze the viewport while reading history; the shared store remains bounded.
  if ($("autoscroll").checked || state.logKey !== key) {
    output.textContent = text;
    state.logKey = key;
    if ($("autoscroll").checked) output.scrollTop = output.scrollHeight;
  }
  $("log-title").textContent = "Logs / " + id;
  $("live").textContent = state.online ? "Live" : "Disconnected";
  $("tail").hidden = $("autoscroll").checked;
  if (page === "activity") {
    const [appEvents, globalEvents] = await Promise.all([
      api("logs?events=true&app=" + encodeURIComponent(id)),
      api("logs?events=true&app="),
    ]);
    if (id !== state.selected || state.page !== "activity") return;
    $("events").textContent =
      [...(appEvents || []), ...(globalEvents || [])]
        .sort((a, b) => a.Seq - b.Seq)
        .map(
          (e) =>
            new Date(e.At).toLocaleTimeString() +
            "  " +
            (e.Service || "heron") +
            "  " +
            e.Text,
        )
        .join("\n") || "No lifecycle events yet.";
  }
}
async function openEditor(id) {
  notice("");
  try {
    const data = await api("config?app=" + encodeURIComponent(id));
    state.version = data.version;
    state.editID = id;
    state.editText = data.yaml;
    $("app-name").value = id;
    $("app-name").disabled = !!id;
    $("editor-title").textContent = id ? "Configure " + id : "Add application";
    $("source").textContent = data.source;
    $("yaml").value = "";
    $("yaml-label").hidden = true;
    $("reveal").hidden = false;
    $("save").disabled = true;
    $("editor-error").textContent = "";
    $("editor").showModal();
  } catch (e) {
    notice(e.message);
  }
}
$("reveal").onclick = () => {
  $("yaml").value = state.editText;
  state.editText = "";
  $("yaml-label").hidden = false;
  $("reveal").hidden = true;
  $("save").disabled = false;
  $("yaml").focus();
};
$("close-editor").onclick = () => $("editor").close();
$("editor").addEventListener("close", () => {
  $("yaml").value = "";
  state.editText = "";
});
$("config-form").onsubmit = async (e) => {
  e.preventDefault();
  $("save").disabled = true;
  try {
    const id = $("app-name").value.trim();
    const data = await api("config?app=" + encodeURIComponent(id), {
      YAML: $("yaml").value,
      Version: state.version,
      Create: !state.editID,
    });
    $("editor").close();
    notice(data.message);
  } catch (e) {
    $("editor-error").textContent = e.message;
  } finally {
    $("save").disabled = false;
  }
};
$("add").onclick = () => openEditor("");
$("search").oninput = render;
$("log-filter").oninput = () => refreshLogs().catch((e) => notice(e.message));
$("clear").onclick = () => {
  state.cleared.set(
    state.selected,
    Math.max(0, ...(state.lastEntries || []).map((e) => e.Seq)),
  );
  refreshLogs().catch((e) => notice(e.message));
};
$("autoscroll").onchange = () => refreshLogs().catch((e) => notice(e.message));
$("tail").onclick = () => {
  $("autoscroll").checked = true;
  refreshLogs().catch((e) => notice(e.message));
};
$("log-output").addEventListener(
  "wheel",
  () => {
    $("autoscroll").checked = false;
    $("tail").hidden = false;
  },
  { passive: true },
);
$("log-output").addEventListener("keydown", (e) => {
  if (["PageUp", "ArrowUp", "Home"].includes(e.key)) {
    $("autoscroll").checked = false;
    $("tail").hidden = false;
  }
});
// Reparent the existing pane: filters, buffered output and listeners stay shared.
const logModal = $("log-modal"),
  logPane = $("log-pane");
const logAnchor = document.createComment("docked log pane");
logPane.before(logAnchor);
let logsWereCollapsed = false;
let expandedScroll = { top: 0, left: 0 };
function rememberExpandedScroll() {
  expandedScroll = {
    top: $("log-output").scrollTop,
    left: $("log-output").scrollLeft,
  };
}
// Capture before native Escape hides the dialog and resets layout measurements.
logModal.addEventListener("cancel", rememberExpandedScroll);
$("expand-logs").onclick = () => {
  if (logModal.open) {
    rememberExpandedScroll();
    logModal.close();
    return;
  }
  const top = $("log-output").scrollTop,
    left = $("log-output").scrollLeft;
  logsWereCollapsed = logPane.classList.contains("collapsed");
  logPane.classList.remove("collapsed");
  $("log-output").hidden = false;
  $("collapse").hidden = true;
  logModal.append(logPane);
  $("expand-logs").textContent = "Close expanded logs";
  $("expand-logs").setAttribute("aria-expanded", "true");
  logModal.showModal();
  $("expand-logs").focus({ preventScroll: true });
  $("log-output").scrollTop = $("autoscroll").checked
    ? $("log-output").scrollHeight
    : top;
  $("log-output").scrollLeft = left;
};
logModal.addEventListener("close", () => {
  const { top, left } = expandedScroll;
  logAnchor.after(logPane);
  logPane.classList.toggle("collapsed", logsWereCollapsed);
  $("log-output").hidden = logsWereCollapsed;
  $("collapse").hidden = false;
  $("expand-logs").textContent = "Expand logs";
  $("expand-logs").setAttribute("aria-expanded", "false");
  $("expand-logs").focus({ preventScroll: true });
  $("log-output").scrollTop = $("autoscroll").checked
    ? $("log-output").scrollHeight
    : top;
  $("log-output").scrollLeft = left;
});
$("collapse").onclick = () => {
  const closed = $("log-pane").classList.toggle("collapsed");
  $("log-output").hidden = closed;
  $("collapse").setAttribute("aria-expanded", String(!closed));
  $("collapse").textContent = closed ? "⌃" : "⌄";
  $("collapse").setAttribute(
    "aria-label",
    closed ? "Expand logs" : "Collapse logs",
  );
};
for (const b of document.querySelectorAll("[data-page]"))
  b.onclick = () => {
    state.page = b.dataset.page;
    for (const p of ["apps", "activity", "settings"])
      $(p + "-page").hidden = p !== state.page;
    for (const n of document.querySelectorAll("[data-page]"))
      n.classList.toggle("active", n === b);
    $("title").textContent =
      state.page === "apps"
        ? "Applications"
        : state.page === "activity"
          ? "Activity"
          : "Settings";
    $("subtitle").textContent =
      state.page === "apps"
        ? "Your local services, in one place."
        : state.page === "activity"
          ? "Understand what happened, and when."
          : "Local runtime and configuration.";
    $("search").hidden = state.page !== "apps";
    refreshLogs().catch((e) => notice(e.message));
  };
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "k") {
    e.preventDefault();
    (logModal.open ? $("log-filter") : $("search")).focus();
  }
});
async function poll() {
  await refresh();
  setTimeout(poll, 1000);
}
poll();
