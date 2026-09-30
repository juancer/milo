# Usage schedules and parental controls

Parents decide **when** each feature may be used, **who** may use it, and what the assistant may
do. The three tools work together:

| Tool | Where | Effect |
|---|---|---|
| Feature switches | Dashboard | A disabled feature never starts and touches no external service |
| Usage schedules | Schedules | A feature is only usable inside the allowed hours |
| Permissions | People | Who may send messages, raise emergencies, manage things |

## Schedules

For each feature you can turn a schedule on and add allowed windows per weekday, for example
Telegram Monday-Friday 08:00-20:30. Rules:

- Times use the device timezone (set it in Schedules). Daylight saving is handled.
- A window ending at `00:00` means midnight. A window that ends before it starts
  (`22:00 - 06:00`) continues into the next day.
- A feature with its schedule **off** is always available. A schedule that is **on** with no
  windows blocks the feature completely (the UI warns you).
- Schedule off/on and windows are stored in SQLite (`usage_schedules`, `usage_schedule_rules`).

What "closed" means:

- Child-side actions (talk, choose chat, ask the assistant...) are refused; the device plays a
  short error tone.
- Incoming messages are **stored, never deleted**. They arrive silently and stay pending.
- When the allowed hours begin, Milo plays one "piii" for the whole backlog (switch this off
  under Sound → "Announce messages that arrived outside usage hours").
- The child can still hear **emergency** messages with LISTEN.

## Emergency

See [emergency.md](emergency.md). Emergencies bypass schedules by design.
