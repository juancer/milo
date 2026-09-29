import { api, type Status } from "../api";
import { clear, errorMessage, formatUptime, h, toast, when } from "../dom";

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

export async function render(root: HTMLElement): Promise<void> {
  const [status, features, audit] = await Promise.all([
    api<Status>("GET", "/api/status"),
    api<Feature[]>("GET", "/api/features"),
    api<AuditRow[]>("GET", "/api/audit"),
  ]);
  clear(root);

  const facts: [string, string][] = [
    ["Version", status.version],
    ["Uptime", formatUptime(status.uptime_seconds)],
    ["Hardware", status.hardware],
    ["Platform", status.arch],
    ["Audio", status.audio.available ? `${status.audio.backend}, ${status.audio.volume}%` : `unavailable${status.audio.detail ? ` (${status.audio.detail})` : ""}`],
    ["Schema", String(status.schema_version)],
  ];
  root.append(
    h("section", { class: "card" }, h("h2", {}, "Device"),
      h("dl", { class: "grid" }, ...facts.map(([k, v]) => h("div", {}, h("dt", {}, k), h("dd", {}, v)))),
      ...(status.hardware_errors ?? []).map((e) => h("p", { class: "error", role: "alert" }, e)),
    ),
    h("section", { class: "card" }, h("h2", {}, "Features"),
      features.length === 0 ? h("p", { class: "muted" }, "No features are available yet.") : null,
      h("ul", { class: "list" },
        ...features.map((f) => {
          const toggle = h("input", {
            type: "checkbox", checked: f.enabled, ariaLabel: `Enable ${f.name}`,
            onchange: async () => {
              toggle.disabled = true;
              try {
                await api("PUT", `/api/features/${encodeURIComponent(f.name)}`, { enabled: toggle.checked });
              } catch (e) {
                toast(errorMessage(e), true);
              }
              await render(root);
            },
          });
          return h("li", { class: "row" },
            h("span", {}, f.name),
            h("span", { class: f.last_error ? "error" : "muted" }, f.last_error ? `error: ${f.last_error}` : f.running ? "running" : "stopped"),
            toggle);
        })),
    ),
    h("section", { class: "card" }, h("h2", {}, "Recent activity"),
      audit.length === 0 ? h("p", { class: "muted" }, "Nothing recorded yet.") : null,
      h("ul", { class: "list" },
        ...audit.map((r) => h("li", { class: "row" },
          h("span", {}, r.target ? `${r.action} · ${r.target}` : r.action),
          h("span", { class: "muted" }, `${r.actor} · ${when(r.at)}`)))),
    ),
  );
}
