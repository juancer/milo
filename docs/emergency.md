# Emergency channel

Emergency is a high-priority *message*, not a phone call: the Telegram Bot API cannot place a
real call. It is meant for "call me back / I need the child's attention now".

## Sending an alert

1. The relative needs the `emergency_alert` permission (PARENT has it by default; grant it to
   anyone else in **People**). Only such people see the 🚨 **Emergency** button in `/menu`,
   and `/emergency` works only for them.
2. The bot asks: *"⚠️ Send emergency alert?"* with **YES, SEND** / **CANCEL**.
3. On YES the device sounds **PIII — PIII — PIII** at once, interrupting any playback, even
   outside usage hours.
4. The message is stored with `type = emergency`, `priority = critical`.

The alert stays pending. When the child presses **LISTEN**, emergency messages are played first
and are the only ones that play outside usage hours.

For two minutes after an alert, anything the same person sends (a voice message, a text) is
marked urgent too, so they can explain what is happening.

## Guard rails against accidents

- The confirmation is single-use and expires after two minutes; cancelling invalidates it.
- The same person cannot send another alert for one minute.
- The permission is checked when the button is shown, when it is tapped and again by the
  Action Engine when the alert is raised. The LLM and the buttons cannot raise emergencies.
- Every alert is written to the audit log (who, when). Message content is never logged.

## Not included

There is no repeating alarm and no real-time call in this version.
