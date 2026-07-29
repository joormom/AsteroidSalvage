"""The three item slots along the bottom of the screen.

Slots are fixed positions, not a list that reshuffles: 1 is always 1. An item picked up
lands in the first free slot and stays there until it is used, so muscle memory survives
picking something up mid-fight — which is the only time it matters.

There are four selection states, not three. The extra one is **BEAM**, and it is the
default: right-click fires the selected item, and with nothing selected right-click is the
tractor beam it has always been. Without that state, picking up an item would silently
take your beam away, and the first you would know is a rock you failed to grab.

Keys 1-3 use a slot outright whatever is selected. The wheel cycles BEAM → 1 → 2 → 3 and
round again, so getting back to the beam is always at most three clicks in one direction.

Icons rather than labels. "SHD" has to be read and then translated; a shield is recognised.
That difference is the whole point of the bar at the moment you are deciding to press it.

The bar draws what the *server* says is in the slots (PlayerState), never what the client
thinks it picked up. A slot that empties because the server refused a use has to look
empty.
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
import icons  # noqa: E402

# The selection that means "no item": right-click stays the tractor beam.
BEAM = -1

SLOT_W = 0.105
SLOT_H = 0.070
SLOT_GAP = 0.020

# The bottom centre, below the hull/charge/boost stack — which was moved up to make room.
# Both are glanced at rather than read, so they share a corner; they must not share pixels.
BAR_Y = 0.085
EFFECTS_Y = 0.193

EMPTY_FILL = (0.10, 0.12, 0.17, 0.72)
EMPTY_EDGE = (0.30, 0.33, 0.40, 1.0)


class Hotbar:
    """Three slots, drawn from PlayerState."""

    def __init__(self, base):
        self.base = base
        # Start on the beam. Anything else would mean a fresh pilot's right-click did
        # nothing until they noticed the bar.
        self.selected = BEAM
        self._signature: tuple | None = None
        self._icons: dict[int, object] = {}

        self.root = base.a2dBottomCenter.attachNewNode("hotbar")

        # The beam tile sits to the left of the numbered slots, as position zero — it is
        # part of the same cycle, so it is part of the same row.
        span = (proto.ITEM_SLOTS + 1) * SLOT_W * 2 + proto.ITEM_SLOTS * SLOT_GAP
        left = -span / 2 + SLOT_W

        self.beam_slot = DirectFrame(
            frameColor=EMPTY_FILL,
            frameSize=(-SLOT_W, SLOT_W, -SLOT_H, SLOT_H),
            pos=(left, 0, BAR_Y),
            parent=self.root,
        )
        self.beam_label = DirectLabel(
            text="BEAM", scale=0.032, pos=(0, 0, -0.010),
            text_fg=(0.55, 0.90, 1.0, 1.0), frameColor=(0, 0, 0, 0),
            parent=self.beam_slot,
        )

        self.slots = []
        for i in range(proto.ITEM_SLOTS):
            x = left + (i + 1) * (SLOT_W * 2 + SLOT_GAP)
            frame = DirectFrame(
                frameColor=EMPTY_FILL,
                frameSize=(-SLOT_W, SLOT_W, -SLOT_H, SLOT_H),
                pos=(x, 0, BAR_Y),
                parent=self.root,
            )
            key = DirectLabel(
                text=str(i + 1), scale=0.028,
                pos=(-SLOT_W + 0.020, 0, SLOT_H - 0.032),
                text_fg=(0.55, 0.59, 0.68, 1.0), text_align=TextNode.ALeft,
                frameColor=(0, 0, 0, 0), parent=frame,
            )
            self.slots.append({"frame": frame, "key": key})

        # The active-effect strip, above the slots. Timers rather than icons: what a
        # player needs mid-fight is how long they have left, not which one it is.
        self.effects = DirectLabel(
            text="", scale=0.034, pos=(0, 0, EFFECTS_Y),
            text_fg=(0.85, 0.90, 0.98, 1.0), frameColor=(0, 0, 0, 0),
            parent=self.root,
        )
        self.root.hide()

    # --- selection ---------------------------------------------------------

    def cycle(self, delta: int) -> None:
        """Wheel. Cycles BEAM -> 1 -> 2 -> 3 -> BEAM."""
        # Shifted by one so BEAM (-1) sits at index 0 of a four-position ring.
        pos = (self.selected + 1 + delta) % (proto.ITEM_SLOTS + 1)
        self.selected = pos - 1
        self._signature = None  # force a restyle so the highlight moves

    def select(self, slot: int) -> None:
        self.selected = slot
        self._signature = None

    def item_in(self, player, slot: int) -> int:
        """What is in a slot according to the server, or ITEM_NONE."""
        if player is None or slot < 0 or slot >= len(player.items):
            return proto.ITEM_NONE
        return player.items[slot]

    def selected_item(self, player) -> int:
        """The item right-click would fire, or ITEM_NONE for the tractor beam."""
        return self.item_in(player, self.selected)

    def set_visible(self, visible: bool) -> None:
        self.root.show() if visible else self.root.hide()

    def destroy(self) -> None:
        self.root.removeNode()

    # --- per-frame ---------------------------------------------------------

    def update(self, player) -> None:
        if player is None:
            self.root.hide()
            return
        self.root.show()

        items = list(player.items)
        signature = (tuple(items), self.selected)
        if signature != self._signature:
            self._signature = signature
            self._restyle(items)

        self.effects.setText(self._effect_line(player))

    @staticmethod
    def _highlight(frame) -> None:
        """Mark the selection by lifting the fill — DirectFrame has no border colour."""
        c = frame["frameColor"]
        frame["frameColor"] = (
            min(1.0, c[0] + 0.18), min(1.0, c[1] + 0.18),
            min(1.0, c[2] + 0.18), min(1.0, c[3] + 0.12),
        )

    def _restyle(self, items) -> None:
        for old in self._icons.values():
            old.removeNode()
        self._icons = {}

        beam_rgb = (0.20, 0.42, 0.52)
        self.beam_slot["frameColor"] = (beam_rgb[0], beam_rgb[1], beam_rgb[2], 0.80)
        if self.selected == BEAM:
            self._highlight(self.beam_slot)

        for i, slot in enumerate(self.slots):
            item = items[i] if i < len(items) else proto.ITEM_NONE

            if item == proto.ITEM_NONE:
                slot["frame"]["frameColor"] = EMPTY_FILL
                slot["key"]["text_fg"] = (0.42, 0.45, 0.52, 1.0)
            else:
                rgb = proto.ITEM_COLORS.get(item, (1, 1, 1))
                # A filled slot is tinted with its own item's colour, so which slot holds
                # what is readable without looking at the icons at all.
                slot["frame"]["frameColor"] = (rgb[0] * 0.30, rgb[1] * 0.30,
                                               rgb[2] * 0.30, 0.85)
                slot["key"]["text_fg"] = (0.85, 0.88, 0.94, 1.0)

                icon = icons.make_item_icon(item)
                if icon is not None:
                    icon.reparentTo(slot["frame"])
                    icon.setScale(SLOT_H * 0.62)
                    icon.setPos(0.012, 0, -0.004)
                    self._icons[i] = icon

            if i == self.selected:
                self._highlight(slot["frame"])

    @staticmethod
    def _effect_line(player) -> str:
        parts = []
        if player.item_damage > 0:
            parts.append(f"OVERCHARGE {player.item_damage:.0f}s")
        if player.item_speed > 0:
            parts.append(f"AFTERBURNER {player.item_speed:.0f}s")
        # No shield here. It is a pool rather than a timer, and it now has its own bar
        # above the hull — which is where damage arrives, and where you are already
        # looking when it matters.
        return "     ".join(parts)
