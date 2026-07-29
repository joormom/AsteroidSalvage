"""In-game chat: a fading log, and a composer you open with Enter.

Two channels. **TAB switches between ALL and TEAM while composing**, which is the one
control here worth stating plainly: you do not pick a channel and then type, you start
typing and then decide who hears it. Getting that backwards means every message meant for
your crew goes to the room the moment you forget.

The composer takes the keyboard while it is open — Panda3D delivers keys to the flight
controls otherwise, and a player typing "what" would strafe, brake and thrust. Everything
it grabs is handed back on send or cancel.

The log fades rather than scrolling away, so a quiet screen stays quiet, but nothing
vanishes mid-read: a line holds at full opacity for its whole life and only fades over the
last second.
"""

from __future__ import annotations

import os
import sys

from direct.gui.DirectGui import DirectFrame, DirectLabel
from panda3d.core import TextNode

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

# How long a line stays up, and how much of that is spent fading out.
LINE_SECONDS = 12.0
FADE_SECONDS = 1.5

MAX_LINES = 8

# Bottom left, out of the way of the crosshair and clear of the centre column, which from
# the bottom up is the item hotbar, then the hull/charge/boost bars, then the composer.
LOG_X = 0.04
LOG_Y = 0.56
LINE_PITCH = 0.045

# The composer sits in the gap between the log and the status bars. It only exists while
# somebody is typing, but it must not appear on top of the hotbar when it does — a bar
# that covers the thing you are about to press is worse than one slightly out of the way.
ENTRY_Y = -0.55


class Chat:
    """The log and the composer."""

    def __init__(self, base, net_send, channel_name):
        self.base = base
        self._send = net_send
        self._channel_name = channel_name

        self.composing = False
        self.channel = proto.CHAT_ALL
        self._text = ""
        self._lines: list[dict] = []

        self.log_root = base.a2dBottomLeft.attachNewNode("chatlog")
        self.labels = [
            DirectLabel(
                text="", scale=0.038, pos=(LOG_X, 0, LOG_Y + i * LINE_PITCH),
                text_fg=(1, 1, 1, 1), text_align=TextNode.ALeft,
                frameColor=(0, 0, 0, 0), parent=self.log_root,
            )
            for i in range(MAX_LINES)
        ]

        # The composer. Its own frame so it can be shown and hidden as one thing.
        self.entry_root = DirectFrame(
            frameColor=(0.04, 0.06, 0.10, 0.88),
            frameSize=(-1.35, 1.35, -0.030, 0.038),
            pos=(0, 0, ENTRY_Y),
            parent=base.aspect2d,
        )
        self.prompt = DirectLabel(
            text="", scale=0.042, pos=(-1.30, 0, -0.012),
            text_fg=(1.0, 0.85, 0.35, 1.0), text_align=TextNode.ALeft,
            frameColor=(0, 0, 0, 0), parent=self.entry_root,
        )
        self.typed = DirectLabel(
            text="", scale=0.042, pos=(-1.30, 0, -0.012),
            text_fg=(1, 1, 1, 1), text_align=TextNode.ALeft,
            frameColor=(0, 0, 0, 0), parent=self.entry_root,
        )
        self.entry_root.hide()

    # --- composing ---------------------------------------------------------

    def open(self, channel: int | None = None) -> None:
        if self.composing:
            return
        self.composing = True
        if channel is not None:
            self.channel = channel
        self._text = ""
        self.entry_root.show()
        self._refresh_prompt()

        # Take the keyboard. buttonThrowers deliver raw keystrokes, which is the only way
        # to type without every letter also being a flight binding.
        self.base.buttonThrowers[0].node().setButtonDownEvent("chat-key")
        self.base.accept("chat-key", self._on_key)

    def close(self) -> None:
        if not self.composing:
            return
        self.composing = False
        self._text = ""
        self.entry_root.hide()

        self.base.ignore("chat-key")
        self.base.buttonThrowers[0].node().setButtonDownEvent("")

    def toggle_channel(self) -> None:
        """TAB. Only meaningful while composing."""
        self.channel = (proto.CHAT_TEAM if self.channel == proto.CHAT_ALL
                        else proto.CHAT_ALL)
        self._refresh_prompt()

    def _refresh_prompt(self) -> None:
        if self.channel == proto.CHAT_TEAM:
            label, colour = "[TEAM]", (0.45, 1.0, 0.55, 1.0)
        else:
            label, colour = "[ALL]", (1.0, 0.85, 0.35, 1.0)
        self.prompt.setText(f"{label} ")
        self.prompt["text_fg"] = colour
        # Push the typed text clear of a prompt whose width changes with the channel.
        self.typed.setPos(-1.30 + 0.042 * len(label) * 0.62, 0, -0.012)
        self.typed.setText(self._text + "_")

    def _on_key(self, key: str) -> None:
        """Raw keystroke while the composer is open."""
        if key == "escape":
            self.close()
            return
        if key == "enter":
            text = self._text.strip()
            self.close()
            if text:
                self._send(self.channel, text)
            return
        if key == "tab":
            self.toggle_channel()
            return
        if key == "backspace":
            self._text = self._text[:-1]
            self._refresh_prompt()
            return
        if key == "space":
            key = " "

        # Single printable characters only. Panda3D reports modifiers and function keys
        # by name ("lshift", "f1"), and those are exactly the multi-character ones.
        if len(key) == 1 and key.isprintable():
            if len(self._text.encode("utf-8")) < proto.CHAT_MAX_BYTES:
                self._text += key
                self._refresh_prompt()

    # --- log ---------------------------------------------------------------

    def add(self, say: proto.ChatSay) -> None:
        tag = "[TEAM]" if say.channel == proto.CHAT_TEAM else ""
        rgb = proto.team_color(say.team)
        self._lines.append({
            "text": f"{tag}{' ' if tag else ''}{say.name}: {say.text}",
            "rgb": rgb,
            "age": 0.0,
        })
        del self._lines[:-MAX_LINES]

    def add_notice(self, text: str) -> None:
        """A local line nobody else sees — connection notices and the like."""
        self._lines.append({"text": text, "rgb": (0.70, 0.74, 0.82), "age": 0.0})
        del self._lines[:-MAX_LINES]

    def update(self, dt: float) -> None:
        for line in self._lines:
            line["age"] += dt
        self._lines = [ln for ln in self._lines if ln["age"] < LINE_SECONDS]

        # Newest at the bottom, which is where a reader's eye already is.
        for i, label in enumerate(self.labels):
            idx = len(self._lines) - 1 - i
            if idx < 0:
                label.setText("")
                continue
            line = self._lines[idx]
            label.setText(line["text"])

            left = LINE_SECONDS - line["age"]
            alpha = 1.0 if left > FADE_SECONDS else max(0.0, left / FADE_SECONDS)
            r, g, b = line["rgb"]
            label["text_fg"] = (r, g, b, alpha)

    def set_visible(self, visible: bool) -> None:
        self.log_root.show() if visible else self.log_root.hide()
        if not visible:
            self.close()

    def destroy(self) -> None:
        self.close()
        self.log_root.removeNode()
        self.entry_root.destroy()
