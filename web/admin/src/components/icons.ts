/**
 * Inline SVG icon set (24x24, 1.75 stroke, currentColor), one place for every glyph the admin draws.
 * SVG is built with createElementNS, so nothing here is parsed as HTML.
 */
const NS = "http://www.w3.org/2000/svg";

const PATHS = {
  telegram: ["M21 4 3 11l6 2.2L11 20l3-4.5 4.5 3.3L21 4Z", "M9 13.2 21 4"],
  people: ["M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z", "M2.5 20c.6-3.4 3.2-5.5 6.5-5.5s5.9 2.1 6.5 5.5", "M16 4.3a3.2 3.2 0 0 1 0 6.2", "M18 14.8c1.9.6 3.1 2.3 3.5 5.2"],
  chats: ["M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H11l-4.5 4v-4h0A2.5 2.5 0 0 1 4 13.5v-8Z"],
  buttons: ["M12 20a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z", "M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z"],
  schedules: ["M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z", "M12 7v5l3.2 2"],
  assistant: ["M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9L12 3Z", "M18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8.8-2.2Z"],
  ai: ["M9 3v5M15 3v5", "M7 8h10v4a5 5 0 0 1-10 0V8Z", "M12 17v4"],
  voice: ["M4 10v4M8 6v12M12 3v18M16 7v10M20 10v4"],
  wakeword: ["M12 15a3.5 3.5 0 0 0 3.5-3.5v-5a3.5 3.5 0 0 0-7 0v5A3.5 3.5 0 0 0 12 15Z", "M5.5 11.5a6.5 6.5 0 0 0 13 0", "M12 18v3"],
  skills: ["M4 4h6.5v6.5H4z", "M13.5 4H20v6.5h-6.5z", "M4 13.5h6.5V20H4z", "M16.75 13.5v6.5", "M13.5 16.75H20"],
  music: ["M9 18V6l10-2v12", "M9 18a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0Z", "M19 16a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0Z"],
  sound: ["M4 9.5v5h3.5L12 18.5v-13L7.5 9.5H4Z", "M15.5 9a4 4 0 0 1 0 6", "M18 6.5a7.5 7.5 0 0 1 0 11"],
  wifi: ["M2.5 9a14 14 0 0 1 19 0", "M5.5 12.3a9.5 9.5 0 0 1 13 0", "M8.6 15.5a5 5 0 0 1 6.8 0", "M12 19h.01"],
  system: ["M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z", "M19.4 13.5a7.6 7.6 0 0 0 0-3l1.8-1.4-1.8-3.1-2.1.8a7.5 7.5 0 0 0-2.6-1.5L14.3 4h-3.6l-.4 2.3a7.5 7.5 0 0 0-2.6 1.5l-2.1-.8-1.8 3.1 1.8 1.4a7.6 7.6 0 0 0 0 3l-1.8 1.4 1.8 3.1 2.1-.8a7.5 7.5 0 0 0 2.6 1.5l.4 2.3h3.6l.4-2.3a7.5 7.5 0 0 0 2.6-1.5l2.1.8 1.8-3.1-1.8-1.4Z"],
  backup: ["M3.5 7.5h17v3h-17z", "M5 10.5V19a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-8.5", "M10 14h4"],
  admins: ["M12 3 4.5 6v5.5c0 4.4 3.1 8 7.5 9.5 4.4-1.5 7.5-5.1 7.5-9.5V6L12 3Z", "M9 12l2.2 2.2L15.5 10"],
  device: ["M7 7h10v10H7z", "M10 10h4v4h-4z", "M9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4"],
  activity: ["M3 12h4l2.5-6 4 12 2.5-6H21"],
  simulator: ["M9.5 3h5", "M10.5 3v6.2L5.2 18a2 2 0 0 0 1.7 3h10.2a2 2 0 0 0 1.7-3l-5.3-8.8V3", "M8 15h8"],
  close: ["M6 6l12 12M18 6 6 18"],
  chevron: ["M9.5 6 15.5 12l-6 6"],
  check: ["M5 12.5l4.2 4.2L19 7"],
} as const;

export type IconName = keyof typeof PATHS;

export function icon(name: IconName, size = 22): SVGSVGElement {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.75");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  for (const d of PATHS[name]) {
    const path = document.createElementNS(NS, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  return svg;
}
