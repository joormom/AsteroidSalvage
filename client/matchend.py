"""The end-of-match payoff: the losing motherships come apart, in big letters.

Driven entirely by the client. The server has already decided the winner and says so in
MatchState; nothing here changes the outcome, so it can be as theatrical as it likes
without any risk of desyncing anyone.

Structured as a small timeline rather than Panda3D intervals so the whole sequence is
one readable function and can be scrubbed, restarted, or cut short by leaving the match
without leaving orphaned tasks behind.
"""

from __future__ import annotations

import os
import random
import sys

from direct.gui.OnscreenText import OnscreenText
from panda3d.core import TextNode, Vec3, Vec4

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

# Beat timings, in seconds from the start of the sequence.
CHARGE_UNTIL = 1.1  # the hull shudders and glows
BLAST_AT = 1.1  # the big one
LETTERS_AT = 1.5  # "LOSERS!" lands just after the flash, not before
SEQUENCE_END = 7.0

# Secondary detonations keep going through the charge-up so it builds rather than
# arriving all at once.
SECONDARY_EVERY = 0.16


class MatchEndSequence:
    """Blows up the losers' motherships and mocks them for it."""

    def __init__(self, base, scene):
        self.base = base
        self.scene = scene

        self.banner = OnscreenText(
            text="",
            pos=(0, 0.12),
            scale=0.30,
            fg=(1.0, 0.22, 0.18, 1.0),
            align=TextNode.ACenter,
            mayChange=True,
            parent=base.aspect2d,
            shadow=(0, 0, 0, 0.9),
        )
        self.banner.hide()

        self.subtitle = OnscreenText(
            text="",
            pos=(0, -0.10),
            scale=0.075,
            fg=(1.0, 0.85, 0.35, 1.0),
            align=TextNode.ACenter,
            mayChange=True,
            parent=base.aspect2d,
            shadow=(0, 0, 0, 0.9),
        )
        self.subtitle.hide()

        self.active = False
        self._t = 0.0
        self._next_secondary = 0.0
        self._blown = False
        self._losers: list[int] = []
        self._won = False

        # finished distinguishes "played all the way through" from "never started",
        # which `active` alone cannot: both are False. The results screen waits on it.
        self.finished = False

    # --- control -----------------------------------------------------------

    def start(self, winner: int, own_team: int, teams) -> None:
        """Begin the sequence. `winner` is 0xFF on a draw, in which case nobody blows up.

        A draw deliberately gets no explosion: mocking everyone equally is not a joke,
        it is just noise.
        """
        if self.active:
            return

        self._won = winner == own_team
        self._losers = [
            t.team_id for t in teams
            if t.team_id != winner and winner != 0xFF
        ]

        self.active = True
        self._t = 0.0
        self._next_secondary = 0.0
        self._blown = False
        self.finished = False

    def stop(self) -> None:
        self.active = False
        self.finished = False
        self.clear_banner()

    def clear_banner(self) -> None:
        """Take the letters down without ending the sequence.

        The results screen calls this as it opens: the banner used to stay up because
        there was nothing behind it to look at, and now there is a table there.
        """
        self.banner.hide()
        self.subtitle.hide()

    # --- per-frame ---------------------------------------------------------

    def update(self, dt: float) -> None:
        if not self.active:
            return

        self._t += dt

        if self._t < CHARGE_UNTIL:
            self._charge()
        elif not self._blown:
            self._blow()
        elif self._t >= LETTERS_AT:
            self._letters()

        if self._t >= SEQUENCE_END:
            # The banner stays up until the results screen takes over, but the sequence
            # stops doing work here.
            self.active = False
            self.finished = True

    def _charge(self) -> None:
        """Small detonations rippling across the doomed hulls."""
        if self._t < self._next_secondary:
            return
        self._next_secondary = self._t + SECONDARY_EVERY

        for team in self._losers:
            entity = self.scene.mothership_of(team)
            if entity is None:
                continue
            pos = self.scene.entity_pos(entity)
            if pos is None:
                continue
            # Scatter the pops around the hull rather than dead centre, so it reads as a
            # ship coming apart instead of one light flashing.
            offset = Vec3(
                random.uniform(-1, 1),
                random.uniform(-1, 1),
                random.uniform(-1, 1),
            )
            spot = Vec3(pos) + offset * 22.0
            self.scene.effects.add_flash(spot, 7.0, (1.0, 0.72, 0.25))

    def _blow(self) -> None:
        self._blown = True
        for team in self._losers:
            entity = self.scene.mothership_of(team)
            if entity is None:
                continue
            pos = self.scene.entity_pos(entity)
            if pos is None:
                continue

            rgb = proto.team_color(team)
            # Three overlapping bursts: a white-hot core, the team's own colours coming
            # apart, and a wide slow ring of debris. One burst reads as a firework;
            # layered ones read as a capital ship dying.
            self.scene.effects.add_explosion(pos, 90.0, (1.0, 0.95, 0.80), shards=10)
            self.scene.effects.add_explosion(pos, 62.0, rgb, shards=26)
            self.scene.effects.add_debris(pos, 120.0, (1.0, 0.55, 0.20),
                                          count=34, speed=95.0)

            # The station is gone. The server keeps its body — the match is over and
            # nothing collides with it any more — so hiding it is the client's job, and
            # without it the debris flies out of a hull that is visibly still intact.
            self.scene.hide_entity(entity)

    def _letters(self) -> None:
        if self.banner.isHidden():
            # Three outcomes, three banners. The winner used to be shown the same
            # LOSERS! card as everybody else, with a small aside about somebody else's
            # ship — which meant the one player who earned a payoff never got one.
            if not self._losers:
                self.banner.setText("DRAW")
                self.banner.setFg(Vec4(0.85, 0.87, 0.92, 1.0))
                self.subtitle.setText("nobody wins")
            elif self._won:
                self.banner.setText("WINNER!")
                self.banner.setFg(Vec4(0.40, 1.00, 0.52, 1.0))
                self.subtitle.setText("good job not being a loser")
            else:
                self.banner.setText("LOSERS!")
                self.banner.setFg(Vec4(1.0, 0.22, 0.18, 1.0))
                self.subtitle.setText("that's your ship")
            self.banner.show()
            self.subtitle.show()

        # A hard pulse on the letters for the first second, then hold. Scaling rather
        # than fading: the joke is that it is shouting.
        age = self._t - LETTERS_AT
        if age < 1.0:
            punch = 1.0 + 0.35 * max(0.0, 1.0 - age * 3.0)
            self.banner.setScale(0.30 * punch)
        else:
            self.banner.setScale(0.30)
