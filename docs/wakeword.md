# Wake word

Say the phrase ("Hey Milo" by default) and the assistant starts listening, hands-free. The
buttons work with or without it.

## Privacy

Detection is **local**. While the wake word is on, the microphone is analysed on the device
only; nothing is recorded, stored or sent anywhere. Only after the phrase is recognised does
a normal assistant conversation start (and only then is audio sent to the speech-to-text
provider, exactly as when the GPT button is pressed). The wake word goes through the same
Action Engine path as the GPT button, so the assistant feature switch and its usage schedule
apply to it too.

## "No escuches": switching the wake word off

Say "Hey Milo, no escuches" (or "deja de escuchar", "stop listening"). Milo confirms, and from then on
**the microphone is not opened for the wake word at all** until a button brings it back. It is
remembered across restarts, so a Milo that was told not to listen does not listen after a reboot.

- The way back is a button: map the action **`listening_toggle`** (or `listening_on`) to a button press
  on the Buttons tile. On the reference device the green (talk) button does it with a **long press**
  (hold it about a second); a falling tone means "not listening", a rising one "listening again".
- Buttons, Telegram and everything else keep working while the wake word is off.
- `listening_off` is also available to the assistant, so it works when asked in other words.

## Engines

| Engine | Needs | How it decides | Good for |
|---|---|---|---|
| **Built-in** (default) | nothing | Compares what it hears with recordings of the phrase you make on the device (speech features + dynamic time warping) | A quick start with the voices you record. **Experimental.** |
| **External helper** | your own program | The helper does the detection and prints a line when it hears the phrase | Neural detectors: openWakeWord, Porcupine... |

### Why not openWakeWord built in?

openWakeWord is the obvious open-source choice, but it is a Python + ONNX/TFLite stack, which
would break Milo's promise of a single self-contained binary with no Python on the Pi, and a
Raspberry Pi 3B has little CPU to spare. So Milo ships a small pure-Go detector and offers a
plug for the neural ones. Porcupine is proprietary and needs a licence key, so it is only
available through the helper plug.

### Built-in engine: noise prefilter (optional)

A prefilter can clean the microphone audio before the detector listens: a 100 Hz high-pass and spectral
noise reduction (minimum-statistics noise estimate, then a make-up gain). It is **off by default**
(`wakeword.prefilter` = `0`): on a quiet room it showed no clear gain, and it can cost quiet voices.
Turn it on (`1`) for a room with steady hiss or hum.

Whether it is on or off, the detector separates speech from the background by comparing each 20 ms block
with the quietest fifth of the last three seconds, so it follows a noisy room without being told.
Samples recorded with the prefilter on do not match audio heard with it off, and the other way round:
**record the samples again after changing this setting**.

### Built-in engine: recording samples

1. Open the **Wake word** tile and turn the **wakeword** feature on. (Without samples it shows
   "needs setup"; its Set up button takes you to the recorder.)
2. In the same sheet, **Record a sample**, then say the phrase clearly, close to the device.
3. Repeat three to five times: near and far, quiet and normal, ideally by the people who
   will use it. Delete any sample that scored badly.
4. Use **Live check**: say the phrase and other words. Each line shows how different the
   sound was from your samples and whether it would have woken the device. Adjust
   **Sensitivity** until the phrase wakes it and ordinary talk does not.

Limits to be aware of: it is speaker-dependent (it wakes best for voices it has heard), it
can be fooled by phrases that sound alike, and TV or music can cause false wake-ups. Tests use
synthetic speech, so real-world accuracy has not been measured; treat it as a starting point.

### External helper protocol

A helper is any executable you install yourself. In the admin, give its absolute path (no
arguments). It must be executable and not writable by group or others. Milo starts it and
restarts it if it stops.

- **stdin**: raw audio, 16 kHz, mono, signed 16-bit little-endian.
- **stdout**: one line every time the wake phrase is heard (content is ignored).

`scripts/wakeword/openwakeword-helper.py` is an untested example for openWakeWord.

Because a helper is a program you chose to run, only administrators can set it, and only from
the admin UI. Nothing the child, Telegram or the LLM says can change it.

## Microphone sharing

The microphone has one user at a time. While the wake word listens, any recording (talking to
the assistant, a voice message, recording a sample) takes over and the wake word resumes when
it is done.
