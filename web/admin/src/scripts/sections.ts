/**
 * The configurable areas of the device: what Home shows as tiles and what opens in a sheet.
 * Adding a section = one entry here + `nav.<id>` in the locale files + a view module.
 */
import type { IconName } from "../components/icons";
import type { Tone } from "../components/tile";
import * as admins from "./views/admins";
import * as ai from "./views/ai";
import * as assistant from "./views/assistant";
import * as backup from "./views/backup";
import * as buttons from "./views/buttons";
import * as chats from "./views/chats";
import * as dashboard from "./views/dashboard";
import * as people from "./views/people";
import * as schedules from "./views/schedules";
import * as simulator from "./views/simulator";
import * as skills from "./views/skills";
import * as spotify from "./views/spotify";
import * as sound from "./views/sound";
import * as system from "./views/system";
import * as telegram from "./views/telegram";
import * as voice from "./views/voice";
import * as wakeword from "./views/wakeword";
import * as wifi from "./views/wifi";

export type Group = "connect" | "family" | "assistant" | "device";

export interface Section {
  id: string;
  group: Group;
  icon: IconName;
  tone: Tone;
  render: (root: HTMLElement) => Promise<void>;
  stop?: () => void;
  devOnly?: boolean;
  /** Features (see /api/features) switched on and off from this section's sheet. */
  features?: string[];
}

export const GROUPS: Group[] = ["connect", "family", "assistant", "device"];

export const SECTIONS: Section[] = [
  { id: "telegram", group: "connect", icon: "telegram", tone: "blue", render: telegram.render, features: ["telegram"] },
  { id: "wifi", group: "connect", icon: "wifi", tone: "teal", render: wifi.render },
  { id: "people", group: "family", icon: "people", tone: "orange", render: people.render },
  { id: "chats", group: "family", icon: "chats", tone: "green", render: chats.render },
  { id: "schedules", group: "family", icon: "schedules", tone: "indigo", render: schedules.render },
  { id: "ai", group: "assistant", icon: "ai", tone: "indigo", render: ai.render },
  { id: "voice", group: "assistant", icon: "voice", tone: "teal", render: voice.render },
  { id: "assistant", group: "assistant", icon: "assistant", tone: "purple", render: assistant.render, features: ["assistant"] },
  { id: "wakeword", group: "assistant", icon: "wakeword", tone: "pink", render: wakeword.render, stop: wakeword.stop, features: ["wakeword"] },
  { id: "skills", group: "assistant", icon: "skills", tone: "orange", render: skills.render, stop: skills.stop, features: ["timer", "reminders", "weather", "music", "calendar"] },
  { id: "spotify", group: "assistant", icon: "music", tone: "green", render: spotify.render, stop: spotify.stop, features: ["spotify"] },
  { id: "sound", group: "assistant", icon: "sound", tone: "red", render: sound.render },
  { id: "buttons", group: "device", icon: "buttons", tone: "gray", render: buttons.render },
  { id: "device", group: "device", icon: "device", tone: "blue", render: dashboard.renderDevice },
  { id: "activity", group: "device", icon: "activity", tone: "green", render: dashboard.renderActivity },
  { id: "system", group: "device", icon: "system", tone: "gray", render: system.render },
  { id: "backup", group: "device", icon: "backup", tone: "teal", render: backup.render },
  { id: "admins", group: "device", icon: "admins", tone: "indigo", render: admins.render },
  { id: "simulator", group: "device", icon: "simulator", tone: "purple", render: simulator.render, stop: simulator.stop, devOnly: true },
];

export const sectionById = (id: string): Section | undefined => SECTIONS.find((s) => s.id === id);
