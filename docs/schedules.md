# Usage schedules and parental controls

Parents decide **when** each feature may be used, **who** may use it, and what the assistant may
do. The three tools work together:

| Tool | Where | Effect |
|---|---|---|
| Feature switches | Each feature's tile sheet (Telegram, Assistant, Wake word, Skills) | A disabled feature never starts and touches no external service |
| Usage schedules | Schedules | A feature is only usable inside the allowed hours |
| Permissions | People | Who may send messages, raise emergencies, manage things |

## Schedules

For each feature you can turn a schedule on and add allowed windows per weekday, for example
Telegram Monday-Friday 08:00-20:30. Rules:

- Times use the device timezone (set it in the **Schedules** tile). Daylight saving is handled.
- A window ending at `00:00` means midnight. A window that ends before it starts
  (`22:00 - 06:00`) continues into the next day.
- A feature with its schedule **off** is always available. A schedule that is **on** with no
  windows blocks the feature completely (the UI warns you).
- Schedule off/on and windows are stored in SQLite (`usage_schedules`, `usage_schedule_rules`).
- Away from home, a person with `manage_schedules` can switch a feature's schedule on or off from
  the bot's Administration menu ([telegram.md](telegram.md)); the windows are edited in the web admin.
  A schedule with no windows is never switched on that way.

What "closed" means:

- Child-side actions (talk, choose chat, ask the assistant...) are refused; the device plays a
  short error tone.
- Incoming messages are **stored, never deleted**. They arrive silently and stay pending.
- When the allowed hours begin, Milo plays one "piii" for the whole backlog (switch this off
  in the **Sound** tile: "Announce messages that arrived outside usage hours").
- The child can still hear **emergency** messages with LISTEN.

## Emergency

See [emergency.md](emergency.md). Emergencies bypass schedules by design.
