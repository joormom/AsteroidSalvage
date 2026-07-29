"""Between-round upgrade shop: four randomised offers in a 2x2 grid.

The server rolls a fresh set of offers each intermission and only accepts purchases by
slot, so what is on screen is exactly what can be bought. Cards are clickable and keys
1-4 do the same thing — the mouse is captured for flight, and forcing a player to
release it in a 40 second window would be irritating.

Each card carries a procedurally drawn icon (see icons.py) so the choice reads at a
glance rather than requiring four labels to be parsed under time pressure.
"""

from __future__ import annotations

import os
import sys

from direct.gui.DirectGui import DirectButton, DirectFrame, DirectLabel
from panda3d.core import TextNode, Vec4

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
from icons import make_icon  # noqa: E402

# Card geometry, in aspect2d units.
#
# Height is the constrained axis: two rows of tall cards overlapped the credits line
# above and the hint line below, hiding both. These numbers leave the grid spanning
# roughly +0.48 to -0.63, clear of the title at 0.72 and the footer at -0.78.
CARD_W = 0.46
CARD_H = 0.26
GAP_X = 0.06
GAP_Y = 0.07
GRID_TOP = 0.22

COLOR_CARD = (0.10, 0.13, 0.20, 0.96)
COLOR_CARD_POOR = (0.10, 0.10, 0.12, 0.92)
COLOR_ACCENT = (1.0, 0.85, 0.35, 1.0)


class Shop:
    def __init__(self, base, net):
        self.base = base
        self.net = net
        self.visible = False
        self._cards: list[dict] = []
        self._signature: tuple | None = None

        # Wide enough to cover ultrawide windows; the HUD showed around the edges of a
        # narrower panel and made the overlay look like a floating box.
        self.root = DirectFrame(
            frameColor=(0.02, 0.03, 0.06, 0.88),
            frameSize=(-2.2, 2.2, -1.1, 1.1),
            parent=base.aspect2d,
        )

        self.title = DirectLabel(
            text="",
            scale=0.085,
            pos=(0, 0, 0.72),
            text_fg=COLOR_ACCENT,
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )
        self.subtitle = DirectLabel(
            text="",
            scale=0.052,
            pos=(0, 0, 0.60),
            text_fg=(0.82, 0.86, 0.94, 1.0),
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )
        self.footer = DirectLabel(
            text="Click a card, or press 1-4.   Next round starts automatically.",
            scale=0.045,
            pos=(0, 0, -0.78),
            text_fg=(0.6, 0.64, 0.72, 1.0),
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )

        # The number keys are NOT bound here. They are shared with the item hotbar, and
        # accept() replaces a handler rather than adding to it — whichever of the two was
        # constructed last would silently win. main.py owns them and dispatches on which
        # of us is on screen.
        self.hide()

    # --- lifecycle ---------------------------------------------------------

    def buy(self, slot: int) -> None:
        """Buy by slot. Called by a card click and by main.py's number-key dispatch."""
        # The server is the authority on whether the shop is open and affordable; the
        # client just asks.
        if self.visible:
            self.net.buy_offer(slot)

    # Kept as the card button's command target.
    _buy = buy

    def hide(self) -> None:
        self.visible = False
        self.root.hide()

    def _show(self) -> None:
        self.visible = True
        self.root.show()

    def destroy(self) -> None:
        self._clear_cards()
        self.root.destroy()

    def _clear_cards(self) -> None:
        for card in self._cards:
            card["frame"].destroy()
        self._cards = []

    # --- per-frame ---------------------------------------------------------

    def update(self, match, player) -> None:
        if match is None or match.phase != proto.PHASE_INTERMISSION:
            if self.visible:
                self.hide()
                self._clear_cards()
                self._signature = None
            return

        offers = self.net.offers
        credits = player.credits if player else 0.0

        self._show()
        self.title.setText(f"UPGRADE  -  {match.time_left}s")
        self.subtitle.setText(f"{credits:,.0f} credits")

        # Rebuild only when the offer set actually changes; rebuilding every frame would
        # destroy and recreate GUI nodes 60 times a second.
        signature = tuple((o.upgrade, o.level, o.cost) for o in offers)
        if signature != self._signature:
            self._signature = signature
            self._build_cards(offers)

        # Affordability can change without the offers changing (a purchase spends
        # credits), so restyle every frame — it is only a colour swap.
        for card in self._cards:
            affordable = credits >= card["cost"]
            card["frame"]["frameColor"] = COLOR_CARD if affordable else COLOR_CARD_POOR
            card["cost_label"]["text_fg"] = (
                COLOR_ACCENT if affordable else (0.75, 0.42, 0.40, 1.0)
            )

    def _build_cards(self, offers) -> None:
        self._clear_cards()

        if not offers:
            self.footer.setText("Nothing left to buy - everything is maxed out.")
            return
        self.footer.setText(
            "Click a card, or press 1-4.   Next round starts automatically."
        )

        for i, offer in enumerate(offers[:4]):
            col, row = i % 2, i // 2
            cx = (col - 0.5) * (CARD_W * 2 + GAP_X) * 1.0
            cy = GRID_TOP - row * (CARD_H * 2 + GAP_Y)

            name, desc, max_level, _base, _growth = proto.UPGRADE_INFO.get(
                offer.upgrade, ("Unknown", "", 1, 0.0, 1.0)
            )

            frame = DirectButton(
                frameColor=COLOR_CARD,
                frameSize=(-CARD_W, CARD_W, -CARD_H, CARD_H),
                pos=(cx, 0, cy),
                relief=1,
                command=self._buy,
                extraArgs=[i],
                parent=self.root,
            )

            icon = make_icon(offer.upgrade)
            icon.reparentTo(frame)
            icon.setScale(0.115)
            icon.setPos(-CARD_W + 0.20, 0, 0.03)

            DirectLabel(
                text=f"{i + 1}",
                scale=0.048,
                pos=(-CARD_W + 0.06, 0, CARD_H - 0.09),
                text_fg=(0.55, 0.6, 0.7, 1.0),
                frameColor=(0, 0, 0, 0),
                parent=frame,
            )
            DirectLabel(
                text=name,
                scale=0.055,
                pos=(-0.06, 0, 0.10),
                text_fg=(1, 1, 1, 1),
                text_align=TextNode.ALeft,
                frameColor=(0, 0, 0, 0),
                parent=frame,
            )
            DirectLabel(
                text=f"level {offer.level} of {max_level}",
                scale=0.038,
                pos=(-0.06, 0, 0.02),
                text_fg=(0.62, 0.66, 0.74, 1.0),
                text_align=TextNode.ALeft,
                frameColor=(0, 0, 0, 0),
                parent=frame,
            )
            DirectLabel(
                text=_wrap(desc, 40),
                scale=0.034,
                pos=(-CARD_W + 0.06, 0, -0.10),
                text_fg=(0.78, 0.82, 0.9, 1.0),
                text_align=TextNode.ALeft,
                frameColor=(0, 0, 0, 0),
                parent=frame,
            )
            cost_label = DirectLabel(
                text=f"{offer.cost:,.0f} cr",
                scale=0.05,
                pos=(CARD_W - 0.07, 0, -CARD_H + 0.06),
                text_fg=COLOR_ACCENT,
                text_align=TextNode.ARight,
                frameColor=(0, 0, 0, 0),
                parent=frame,
            )

            self._cards.append(
                {"frame": frame, "cost": offer.cost, "cost_label": cost_label}
            )


def _wrap(text: str, width: int) -> str:
    """Naive word wrap; the descriptions are short and fixed."""
    words, lines, cur = text.split(), [], ""
    for w in words:
        if len(cur) + len(w) + 1 > width:
            lines.append(cur)
            cur = w
        else:
            cur = f"{cur} {w}".strip()
    if cur:
        lines.append(cur)
    return "\n".join(lines)


def proto_offers_max() -> int:
    """Number of hotkeys to bind. Mirrors OffersPerIntermission on the server."""
    return 4
