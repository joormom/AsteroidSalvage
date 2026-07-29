"""The end-of-match scoreboard, shown once the explosions have finished.

This is the last thing a match says, and the question it answers is "what did each of us
actually do" — so it is a table of pilots rather than a headline. The headline already
happened: matchend.py has spent seven seconds blowing up the losers and shouting about
it, and repeating the verdict in smaller letters would be an anticlimax.

Everything on screen is decided by the time it opens (see `0x88 MatchResults`), so the
table is built once on show rather than refreshed per frame — there is nothing left that
can change it.

Rows arrive from the server pre-sorted, by crew and then by what each pilot banked. The
order is deliberately not recomputed here: every player is meant to be looking at the
same table.
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

# Column positions in aspect2d units, chosen to stay inside a 4:3 window — the narrowest
# shape anyone plays on, and the one a wider column layout falls off the edge of.
COL_NAME = -1.02
COL_DELIVERED = 0.02
COL_BANKED = 0.44
COL_KD = 0.76
COL_CREDITS = 1.14

# The vertical band the pilot rows get. Sixteen players have to fit between the column
# headers and the footer, which is what sets the floor.
ROWS_TOP = 0.34
ROWS_BOTTOM = -0.78
ROW_SPACING_MAX = 0.072

COLOR_DIM = (0.62, 0.66, 0.74, 1.0)
COLOR_TEXT = (0.86, 0.90, 0.96, 1.0)
COLOR_ACCENT = (1.0, 0.85, 0.35, 1.0)


class ResultsScreen:
    """The final table. Built on show, torn down on hide."""

    def __init__(self, base):
        self.base = base
        self.visible = False
        self._rows: list = []

        # Wide enough to cover an ultrawide window. A narrower panel lets the HUD show
        # around its edges, which makes the overlay look like a floating box rather than
        # the end of the match.
        # Fully opaque. The match is over and the send-off has already had its moment
        # against the world; a table you read through a ship is just harder to read.
        self.root = DirectFrame(
            frameColor=(0.02, 0.03, 0.06, 1.0),
            frameSize=(-2.2, 2.2, -1.1, 1.1),
            parent=base.aspect2d,
        )

        self.title = DirectLabel(
            text="",
            scale=0.075,
            pos=(0, 0, 0.80),
            text_fg=COLOR_ACCENT,
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )
        self.rounds = DirectLabel(
            text="",
            scale=0.048,
            pos=(0, 0, 0.66),
            text_fg=COLOR_TEXT,
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )
        self.footer = DirectLabel(
            text="Esc to leave the match.",
            scale=0.042,
            pos=(0, 0, -0.90),
            text_fg=(0.55, 0.59, 0.68, 1.0),
            frameColor=(0, 0, 0, 0),
            parent=self.root,
        )

        self._headers = [
            self._label(text, x, 0.46, 0.040, COLOR_DIM, align)
            for text, x, align in (
                ("PILOT", COL_NAME, TextNode.ALeft),
                ("DELIVERED", COL_DELIVERED, TextNode.ARight),
                ("BANKED", COL_BANKED, TextNode.ARight),
                ("K / D", COL_KD, TextNode.ARight),
                ("CREDITS", COL_CREDITS, TextNode.ARight),
            )
        ]

        self.root.hide()

    def _label(self, text, x, y, scale, fg, align, parent=None):
        return DirectLabel(
            text=text,
            scale=scale,
            pos=(x, 0, y),
            text_fg=fg,
            text_align=align,
            frameColor=(0, 0, 0, 0),
            parent=parent if parent is not None else self.root,
        )

    # --- lifecycle ---------------------------------------------------------

    def show(self, results, match, teams, own_player: int) -> None:
        """Open the table. Both `results` (a proto.MatchResults) and `match` may be None.

        Called with whatever the client happens to have. A 0x88 that never arrived costs
        the pilot rows, but the verdict and the round tally come from MatchState and are
        still worth showing — a screen missing its table beats no screen at all, which
        would read as the game having hung on the last frame of the explosion.
        """
        self.visible = True
        self.root.show()

        winner = proto.NO_TEAM
        if results is not None:
            winner = results.winner
        elif match is not None:
            winner = match.winner

        self.title.setText(self._verdict(winner, teams))
        self.title["text_fg"] = (
            COLOR_ACCENT if winner == proto.NO_TEAM
            else proto.team_color(winner) + (1.0,)
        )
        self.rounds.setText(self._round_wins(match, teams))

        self._build_rows(results.players if results is not None else [], own_player)

    def hide(self) -> None:
        self.visible = False
        self.root.hide()
        self._clear_rows()

    def destroy(self) -> None:
        self._clear_rows()
        self.root.destroy()

    def _clear_rows(self) -> None:
        for node in self._rows:
            node.destroy()
        self._rows = []

    # --- content -----------------------------------------------------------

    def _verdict(self, winner: int, teams) -> str:
        if winner == proto.NO_TEAM:
            return "MATCH DRAWN"
        return f"{self._team_name(winner, teams).upper()} WINS THE MATCH"

    @staticmethod
    def _team_name(team: int, teams) -> str:
        for t in teams or ():
            if t.team_id == team:
                if t.name:
                    return t.name
        return f"Team {team + 1}"

    def _round_wins(self, match, teams) -> str:
        """The round tally, as one line: who took how many.

        Read from MatchState rather than counted from the pilot rows — rounds are won by
        holding the lead when the clock runs out, which is not something the per-player
        totals can be made to say.
        """
        if match is None or not match.round_wins:
            return ""
        parts = [
            f"{self._team_name(tid, teams)} {wins}"
            for tid, wins in sorted(match.round_wins.items())
        ]
        return "ROUNDS   " + "     ".join(parts)

    def _build_rows(self, players, own_player: int) -> None:
        self._clear_rows()
        if not players:
            self._rows.append(
                self._label("No pilot record arrived for this match.", 0, 0.1, 0.05,
                            COLOR_DIM, TextNode.ACenter)
            )
            return

        span = ROWS_TOP - ROWS_BOTTOM
        spacing = min(ROW_SPACING_MAX, span / max(1, len(players)))

        for i, r in enumerate(players):
            y = ROWS_TOP - i * spacing
            mine = r.player_id == own_player
            rgb = proto.team_color(r.team)

            if mine:
                # Your own row gets a bar behind it. In a sixteen-pilot table, finding
                # yourself by reading names is slower than it should be for the one row
                # you actually came here to look at.
                bar = DirectFrame(
                    frameColor=(rgb[0], rgb[1], rgb[2], 0.20),
                    frameSize=(COL_NAME - 0.04, COL_CREDITS + 0.04,
                               -spacing * 0.42, spacing * 0.52),
                    pos=(0, 0, y),
                    parent=self.root,
                )
                self._rows.append(bar)

            scale = min(0.044, spacing * 0.62)
            name_fg = rgb + (1.0,)
            value_fg = COLOR_TEXT if mine else COLOR_DIM

            cells = (
                (r.name or "pilot", COL_NAME, TextNode.ALeft, name_fg),
                (f"{r.delivered}", COL_DELIVERED, TextNode.ARight, value_fg),
                (f"{r.banked:,.0f}", COL_BANKED, TextNode.ARight, value_fg),
                (f"{r.kills} / {r.deaths}", COL_KD, TextNode.ARight, value_fg),
                (f"{r.credits:,.0f}", COL_CREDITS, TextNode.ARight, value_fg),
            )
            for text, x, align, fg in cells:
                self._rows.append(self._label(text, x, y, scale, fg, align))
