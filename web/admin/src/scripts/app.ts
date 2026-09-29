interface SessionInfo {
  authenticated: boolean;
  setup_required: boolean;
}
interface Status {
  version: string;
  uptime_seconds: number;
  hardware: string;
  schema_version: number;
  go_version: string;
  arch: string;
}
interface Feature {
  name: string;
  enabled: boolean;
  running: boolean;
  last_error?: string;
}
interface AuditRow {
  at: string;
  actor: string;
  action: string;
  target: string;
}

class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-Requested-With": "nexo-admin" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
  return data as T;
}

function el<T extends HTMLElement>(id: string): T {
  const node = document.getElementById(id);
  if (!node) throw new Error(`missing #${id}`);
  return node as T;
}

function show(id: string, visible: boolean): void {
  el(id).hidden = !visible;
}

function make(tag: string, className?: string, text?: string): HTMLElement {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function formatUptime(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function renderStatus(s: Status): void {
  const rows: [string, string][] = [
    ["Version", s.version],
    ["Uptime", formatUptime(s.uptime_seconds)],
    ["Hardware", s.hardware],
    ["Platform", s.arch],
    ["Schema", String(s.schema_version)],
  ];
  const dl = el("status");
  dl.replaceChildren(
    ...rows.map(([k, v]) => {
      const wrap = make("div");
      wrap.append(make("dt", undefined, k), make("dd", undefined, v));
      return wrap;
    }),
  );
}

function renderFeatures(list: Feature[]): void {
  show("features-empty", list.length === 0);
  el("features").replaceChildren(
    ...list.map((f) => {
      const li = make("li", "row");
      const label = make("span", undefined, f.name);
      const state = make(
        "span",
        "muted",
        f.last_error ? `error: ${f.last_error}` : f.running ? "running" : "stopped",
      );
      const toggle = make("input") as HTMLInputElement;
      toggle.type = "checkbox";
      toggle.checked = f.enabled;
      toggle.setAttribute("aria-label", `Enable ${f.name}`);
      toggle.addEventListener("change", async () => {
        toggle.disabled = true;
        try {
          await api("PUT", `/api/features/${encodeURIComponent(f.name)}`, { enabled: toggle.checked });
        } finally {
          await loadDashboard();
        }
      });
      li.append(label, state, toggle);
      return li;
    }),
  );
}

function renderAudit(rows: AuditRow[]): void {
  show("audit-empty", rows.length === 0);
  el("audit").replaceChildren(
    ...rows.map((r) => {
      const li = make("li", "row");
      const what = r.target ? `${r.action} · ${r.target}` : r.action;
      li.append(make("span", undefined, what), make("span", "muted", `${r.actor} · ${new Date(r.at).toLocaleString()}`));
      return li;
    }),
  );
}

async function loadDashboard(): Promise<void> {
  const [status, features, audit] = await Promise.all([
    api<Status>("GET", "/api/status"),
    api<Feature[]>("GET", "/api/features"),
    api<AuditRow[]>("GET", "/api/audit"),
  ]);
  renderStatus(status);
  renderFeatures(features);
  renderAudit(audit);
}

async function refresh(): Promise<void> {
  show("loading", false);
  try {
    const info = await api<SessionInfo>("GET", "/api/session");
    show("setup", info.setup_required);
    show("login", !info.setup_required && !info.authenticated);
    show("dashboard", info.authenticated);
    show("logout", info.authenticated);
    if (info.authenticated) await loadDashboard();
  } catch {
    el("loading").textContent = "Cannot reach the device.";
    show("loading", true);
  }
}

el<HTMLFormElement>("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = el<HTMLInputElement>("password");
  const error = el("login-error");
  error.hidden = true;
  try {
    await api("POST", "/api/login", { password: input.value });
    input.value = "";
    await refresh();
  } catch (e) {
    error.textContent = e instanceof Error ? e.message : "Sign in failed";
    error.hidden = false;
  }
});

el("logout").addEventListener("click", async () => {
  await api("POST", "/api/logout");
  await refresh();
});

void refresh();
