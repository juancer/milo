# Telegram

Telegram is Milo's family channel: authorized relatives send voice or text messages to the
device, and the child sends voice messages back to chosen relatives. Telegram is only a
transport. It identifies people by **Telegram user id** (never phone numbers) and never
authorizes anyone by itself.

## Create your bot (once per installation)

There is no shared "Milo bot". Each family creates its own:

1. In Telegram, talk to **@BotFather**, send `/newbot` and follow the steps.
2. Copy the token it gives you (`123456:ABC...`). Treat it like a password.
3. In the admin UI open the **Telegram** tile, paste the token and save. It is stored encrypted.
4. Switch the **Telegram** feature on at the top of that sheet. If you switch it on before adding
   the token, Milo asks for the token right there and turns the feature on once it is saved.

Milo asks the bot for its own username, so invitation links always point at *your* bot.

## Inviting someone

Anyone can find a bot, so being able to message it grants nothing. To let a relative in:

1. In the **Telegram** tile, **Invite someone**: choose a role (PARENT, FAMILY or CHILD), how long the link
   is valid and an optional note. An invitation can never grant OWNER.
2. Send the link (`https://t.me/<your bot>?start=<token>`) to the person. It is shown once;
   only its SHA-256 is stored.
3. They open it. Milo records a **pending authorization request** (name, username, Telegram
   id, role from the invitation). The invitation is now used up.
4. You see the request under **Authorization requests** and approve or reject it. Approving
   creates the user and can add them as a chat the child can talk to.

Invitations are single-use, expiring and revocable. Someone without a valid invitation gets
only *"You are not authorized to use this device."* (at most once every 30 seconds).

## Roles and permissions

| Role | Default permissions |
|---|---|
| OWNER | everything |
| PARENT | send/receive messages, emergency alerts, manage contacts and schedules |
| FAMILY | send/receive messages |
| CHILD | none (child-facing actions go through the device) |

Permissions are explicit on top of roles: the **People** tile shows a checkbox per permission and marks
differences from the role as *custom*. For example, allow one grandparent to send emergency
alerts while other family members cannot. Deactivating a person blocks them at once.

The People tile cannot promote anyone to OWNER (like invitations). Chat names must be unique
without regard to case, because the assistant matches them that way. After an emergency alert,
at most three further messages within two minutes are marked urgent, and only while the sender
still holds `emergency_alert`. The bot saves its update offset after each update, so a crash can
repeat one message; a failed save is logged.

`send_message` is required to deliver messages to the device; `receive_message` is required
for the child's messages to be sent to that person.

## How messages flow

Parent to child:

```
Relative → bot → authorized? → stored in SQLite as pending → short "piii" → child presses LISTEN
```

- Messages are never read aloud automatically and the sender is not announced (unless you turn on
  **System → Say who a message is from before playing it**, `telegram.announce_sender`).
- The notification is only a sound, so it reveals nothing.
- Outside usage hours messages are stored silently and announced when hours begin
  ([schedules.md](schedules.md)).
- Urgent alerts: see [emergency.md](emergency.md).

Child to parent:

- **CHAT** cycles through your favorite chats (the device says the name if text-to-speech is
  configured, otherwise it beeps the chat's position).
- **TALK** starts recording; press again to stop and send to the selected chat as a Telegram
  voice message. The selected chat is independent of any AI conversation.
- Messages wait in an outbox and are retried with backoff until Telegram accepts them, so a
  Wi-Fi outage delays them but never loses them.

Each person can have at most 50 unheard messages waiting on the device; further ones are refused
with a reply saying so (a small extra allowance of 8 applies to an emergency alert and its follow-ups). Deactivating a person, or removing
their permission to send, also drops their unheard messages: they are no longer played. A voice note that cannot be decoded
is tried three times and then marked failed, so it never blocks the messages behind it.

The **Message activity** list (Telegram tile) shows who and when, never the content.

## Managing Milo from Telegram

Away from home, a person who holds the right permissions sees an **Administration** button in
the bot's `/menu`. Each section needs its own permission, and **every tap re-checks it on the
device** (and that the person is still active), so a button left on screen, or one forged by
hand, does nothing once the permission is gone. Give the permissions in the **People** tile: the
person is the Telegram family member, not a web administrator account. An invited person is a
PARENT, who does not hold these permissions by default, so tick the boxes below for the ones you
trust.

| Section | What you can do | Permission |
|---|---|---|
| Status and volume | uptime, running features, volume (− / +, shown as 0-10 like "volume 7") | `manage_configuration` |
| Access requests | approve or reject, with buttons; a new request is also pushed to you | `manage_users` |
| Features | turn features on or off | `manage_features` |
| Usage hours | switch a feature's hours on or off (the windows are edited in the web admin) | `manage_schedules` |
| My alerts and copies | opt in to device alerts and to message copies (below) | `manage_configuration` / `manage_users` |

- Turning on a feature that cannot start yet (no AI key, no place...) leaves it off and says it
  needs to be set up in the web admin. You never see a raw error.
- Hours are never switched on when they have no window, because that would shut the feature for
  good. Telegram itself cannot be turned off from Telegram.
- **Secrets never travel through Telegram.** The bot token, API keys, passwords and backups are
  web-only, and the bot never asks for them.
- Every change is recorded in the audit log with the person as the actor (for example
  `telegram:Dad`). Taps closer than 250 ms together are ignored, and stale buttons answer
  politely ("that request was already decided").

### Alerts (opt-in)

Switch **Alerts** on under *My alerts and copies* (needs `manage_configuration`) to be told when a
feature fails to start or Milo starts. The message names the feature only, never an error text. It is off for everyone
until they turn it on.

### Hearing what people send to the device (opt-in copies)

Messages left for the child are never read aloud by Telegram and the content is not shown in the
admin. If an administrator wants to hear or read them too, for example while travelling, they can
turn on **Copy of messages left for the device** (in the bot, or in the **People** tile):

- It is **off by default** and only available to people with `manage_users`. If the permission is
  removed, copies stop at once.
- Each text or voice note that someone else sends to the device is then sent to that person's
  Telegram as a copy (voice notes as the original voice note), captioned with who sent it and when,
  in their own language.
- Nobody receives a copy of their own message. A copy that cannot be delivered is retried a few
  times, then dropped; it never delays or blocks delivery to the device.
- **The sender is not told each time.** Say so when you invite people. Emergency alerts are not
  copied (they already sound the device).

## Language

Every message the bot sends comes from the translation catalogs ([i18n.md](i18n.md)), never from
code. It writes to each person in their own language (**People → Language**) and otherwise in the
notification language (**System → Languages**); strangers always get the notification language.
The bot's username is your own bot's: Milo never assumes a name.

## Group chats

A chat can point at a Telegram group (add the bot to the group, use the group's chat id,
usually negative). Milo only *sends* to groups; it never listens to them.

## Privacy

Message content is stored in `milo.db` until pruned. Messages you no longer need are removed
after `messages.retention_days` (default 30) once played or sent. Logs never contain tokens or
message text.

## Development without Telegram

`make dev` starts a simulator with fake family phones and a fake Bot API, see
[development.md](development.md). The same stack can talk to your real bot instead; that page
explains how.
