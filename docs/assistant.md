# The assistant

Press the GPT button (or, later, say the wake word) and talk. Milo records until you stop
talking, turns the recording into text, asks a language model, and speaks the answer.

```
microphone → speech-to-text → moderation → AI model ⇄ actions → moderation → text-to-speech → speaker
```

## Providers (all replaceable)

| Job | Default | Where it runs | Setting |
|---|---|---|---|
| Language model | `openai/gpt-oss-20b:free` on OpenRouter | cloud | Assistant → Providers |
| Speech-to-text | `whisper-large-v3-turbo` on Groq, or `whisper.cpp` | cloud / on the device | Assistant → Providers |
| Text-to-speech | Piper, or any OpenAI-compatible `/audio/speech` voice | on the device / cloud | Assistant → Providers |

Both cloud providers use the OpenAI-compatible wire format, so any compatible service works:
change the address, model name and key. OpenRouter is only an LLM provider; it does not do
speech recognition, which is why speech-to-text is a separate provider. Free models are rate
limited: Milo retries with backoff and, if the service stays unavailable, the child hears
*"I can't think right now. Please try again in a little while."* instead of silence. A per-hour
question limit (default 60) protects free-tier quotas.

API keys are stored encrypted (see [security.md](security.md)) and are never shown again. If you change a provider address to a different host, its stored key is removed and you must enter it again.

### Choosing engines

- **Speech-to-text:** *Cloud* sends only the recording to an OpenAI-compatible service (Groq by
  default). *whisper.cpp* keeps audio on the device: install `whisper-cli`, download a model
  (`ggml-tiny.bin` is the only one a Raspberry Pi 3B can run, slowly) and set its path.
- **Text-to-speech:** *Piper* runs on the device. *Cloud voice* calls an OpenAI-compatible
  `/audio/speech` endpoint (address, model, voice and a key in Assistant → Providers) and is
  useful when the Pi is too slow for Piper; the text of each answer is then sent to that service.

### Talking to it

Press the GPT button once and speak; it stops listening when you fall silent. Press it again
while it is listening to finish early. Press it while Milo is thinking or speaking to interrupt.

### Text-to-speech with Piper

Install a Piper build for ARM64 and a voice, then set the voice file (a `.onnx` path) under
Assistant → Providers. Milo runs `piper --model <voice> --output_file <tmp>` with the text on
standard input; the command line is fixed. With text-to-speech off, the child hears tones
instead of spoken answers.

## What the model can do: only registered actions

The model never runs commands. It can only ask Milo to run **actions** that are explicitly
registered *and* marked as available to the assistant. Every request goes through the Action
Engine, which checks that the action exists, that the feature is enabled, that the usage
schedule allows it and that the parameters are valid.

Available to the assistant today: `set_volume`, `stop_audio`, `get_time`, `get_device_status`
and, when Telegram is on, `send_message`, `read_messages`, `get_pending_messages`.

Never available to the assistant: emergency alerts, delivering messages *as* a family member,
changing settings, contacts, schedules, permissions or secrets. There is no shell, file or
network tool. Tool results are given to the model as data with an instruction not to obey
them, and the assistant is never shown message contents (it can only make Milo play them).

## Safety

Milo adds its own checks around the model, because a model cannot be trusted with a child:

- **Input check.** What the child says is checked before the model sees it.
- **Output check.** What the model says is checked before it is spoken, and shortened to the
  configured length.
- **Your words.** Add blocked words or phrases under Assistant → Blocked words.
- **Worrying phrases.** If the child says something that suggests they may be unsafe or very
  upset (for example about hurting themselves), the model is not asked. The child hears a fixed
  kind answer that suggests telling a trusted grown-up, and the parents receive a Telegram
  message with the sentence. Switch the message off with *Message the parents…*.
- **Age and personality.** The system prompt adapts vocabulary and tone to the age you set.

These checks use short word lists (English and Spanish) and are a safety net, not a guarantee.
Review the conversations regularly.

## Conversation history

Conversations are stored in SQLite for 7, 30 or 90 days, or not at all
(Assistant → Keep conversation history). Older ones are deleted automatically. You can read
them, or erase everything, under Assistant → Conversations. Within one conversation the
assistant remembers the last few turns; there is no long-term memory.

## What leaves the device

| Data | Goes to | When |
|---|---|---|
| The recording of the child's question | Speech-to-text provider | Each time the child talks to the assistant |
| The transcript, recent turns, the system prompt and tool results | Language-model provider | Each question |
| Nothing | — | Wake-word detection (local), text-to-speech (local), Telegram voice messages (only to Telegram) |

The system prompt contains the child's first name only if you enter it. No family names,
contacts, message contents or credentials are sent.
