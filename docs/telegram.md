# Telegram

Telegram is Nexo's family channel: authorized relatives send voice or text messages to the
device, and the child sends voice messages back to chosen relatives. Telegram is only a
transport. It identifies people by **Telegram user id** (never phone numbers) and never
authorizes anyone by itself.

## Create your bot (once per installation)

There is no shared "Nexo bot". Each family creates its own:

1. In Telegram, talk to **@BotFather**, send `/newbot` and follow the steps.
2. Copy the token it gives you (`123456:ABC...`). Treat it like a password.
3. In the admin UI open **Telegram**, paste the token and save. It is stored encrypted.
4. On the Dashboard turn on the **telegram** feature.

Nexo asks the bot for its own username, so invitation links always point at *your* bot.

## Inviting someone

Anyone can find a bot, so being able to message it grants nothing. To let a relative in:

1. **Telegram → Invite someone**: choose a role (PARENT, FAMILY or CHILD), how long the link
   is valid and an optional note. An invitation can never grant OWNER.
2. Send the link (`https://t.me/<your bot>?start=<token>`) to the person. It is shown once;
   only its SHA-256 is stored.
3. They open it. Nexo records a **pending authorization request** (name, username, Telegram
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

Permissions are explicit on top of roles: **People** shows a checkbox per permission and marks
differences from the role as *custom*. For example, allow one grandparent to send emergency
alerts while other family members cannot. Deactivating a person blocks them at once.

The People page cannot promote anyone to OWNER (like invitations). Chat names must be unique
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

- Messages are never read aloud automatically and the sender is not announced (unless you set
  `telegram.announce_sender` to `1`).
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
with a reply saying so (emergency follow-ups are not counted). A voice note that cannot be decoded
is tried three times and then marked failed, so it never blocks the messages behind it.

The **Message activity** list shows who and when, never the content.

## Group chats

A chat can point at a Telegram group (add the bot to the group, use the group's chat id,
usually negative). Nexo only *sends* to groups; it never listens to them.

## Privacy

Message content is stored in `nexo.db` until pruned. Messages you no longer need are removed
after `messages.retention_days` (default 30) once played or sent. Logs never contain tokens or
message text.

## Development without Telegram

`make dev` starts a simulator with fake family phones and a fake Bot API, see
[development.md](development.md).
