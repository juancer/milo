#!/usr/bin/env python3
"""EXAMPLE helper for Nexo's "external" wake-word engine using openWakeWord.

Untested on real hardware. Nexo does not need Python; this file only exists for people who
want a neural detector. Install it yourself:

    python3 -m venv /opt/nexo/wake-venv
    /opt/nexo/wake-venv/bin/pip install openwakeword
    sudo install -m 0755 -o root -g root openwakeword-helper.py /opt/nexo/wake-helper
    # edit the first line of the installed copy to /opt/nexo/wake-venv/bin/python

Then choose "External helper" in the Wake word tab and enter /opt/nexo/wake-helper.

Protocol: raw 16 kHz mono signed 16-bit little-endian PCM arrives on stdin; print one line
to stdout each time the wake phrase is heard.
"""
import sys

import numpy as np
from openwakeword.model import Model

MODEL = "hey_jarvis"  # pick or train the phrase you want; see the openWakeWord docs
THRESHOLD = 0.5
CHUNK_BYTES = 1280 * 2  # openWakeWord works in 80 ms frames

model = Model(wakeword_models=[MODEL], inference_framework="onnx")
stdin = sys.stdin.buffer
while True:
    data = stdin.read(CHUNK_BYTES)
    if len(data) < CHUNK_BYTES:
        break
    scores = model.predict(np.frombuffer(data, dtype=np.int16))
    if any(v > THRESHOLD for v in scores.values()):
        print("DETECTED", flush=True)
