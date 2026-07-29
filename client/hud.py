"""On-screen readouts.

Deliberately minimal: what am I carrying, how far is home, what is the score, and is the
connection healthy. Everything else belongs in the dev console.
"""

from __future__ import annotations

import math
import os
import sys

from direct.gui.DirectGui import DirectFrame
from direct.gui.OnscreenText import OnscreenText
from panda3d.core import LineSegs, NodePath, TextNode, Vec4

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

EVENT_NAMES = {
    proto.EVENT_GRABBED: "grabbed",
    proto.EVENT_DROPPED: "dropped!",
    proto.EVENT_DEPOSITED: "banked",
    proto.EVENT_DAMAGED: "damaged",
    proto.EVENT_DESTROYED: "destroyed!",
}

TOAST_SECONDS = 2.5

# Weapon bars. Sized so both fit above the help line without crowding the centre of the
# screen, which is where the player is actually looking.
BAR_WIDTH = 0.46
BAR_HEIGHT = 0.030

ENERGY_FULL = (0.35, 0.85, 1.00, 1.0)
ENERGY_LOW = (1.00, 0.45, 0.30, 1.0)
HULL_FULL = (0.35, 0.90, 0.45, 1.0)
HULL_LOW = (1.00, 0.35, 0.30, 1.0)
BOOST_FULL = (1.00, 0.82, 0.15, 1.0)
BOOST_BURNING = (1.00, 0.95, 0.55, 1.0)  # brighter while actually burning
BOOST_EMPTY = (0.55, 0.42, 0.14, 1.0)  # locked out until you let go
BOOST_STALLED = (0.62, 0.20, 0.16, 1.0)  # ran dry: not even refilling yet
# Charged, but under the floor needed to start a burn. Distinct from usable, because
# pressing the key here does nothing and a bar that looks ready would be lying.
BOOST_CHARGING = (0.72, 0.56, 0.16, 1.0)

# Must match sim.BoostReengageSeconds. Only affects how the bar is coloured — the server
# decides whether a burn actually starts.
BOOST_REENGAGE_SECONDS = 1.0

# Bar rows, bottom-centre and stacked upward above the help line.
# Raised to clear the item hotbar, which now owns the bottom centre. The help line that
# used to sit under these is gone (it lives in the settings screen), so the stack moved up
# rather than the bars getting thinner.
BAR_Y_HULL = 0.375
BAR_Y_ENERGY = 0.320
BAR_Y_BOOST = 0.265

# The item shield, above the hull — it is the layer damage reaches first, so it is drawn
# where damage arrives from. Shown only while one is up: a permanently visible empty bar
# would be a fourth thing to ignore, and the whole point is that it is exceptional.
BAR_Y_SHIELD = 0.430
SHIELD_FULL = (0.55, 1.00, 0.65, 1.0)
SHIELD_LOW = (0.95, 1.00, 0.45, 1.0)

# Must match sim.ShieldPool. Only affects the "n / 30" label; the bar itself is driven by
# the fraction the server reports.
SHIELD_POOL = 30.0


def _bolt_icon(color=BOOST_FULL) -> NodePath:
    """A small lightning bolt, drawn in a [-1, 1] box.

    Vector line art rather than an image: the project ships no assets, and a frozen build
    cannot rely on anything outside the executable.
    """
    ls = LineSegs()
    ls.setThickness(3.0)
    ls.setColor(Vec4(*color))
    # Classic zigzag: down-left, across, down-left again, then back up the other side.
    for x, y in ((0.30, 0.95), (-0.35, 0.10), (0.05, 0.10), (-0.30, -0.95),
                 (0.40, -0.05), (0.00, -0.05), (0.30, 0.95)):
        if ls.isEmpty():
            ls.moveTo(x, 0, y)
        else:
            ls.drawTo(x, 0, y)
    np = NodePath(ls.create())
    np.setLightOff()
    return np


class HUD:
    def __init__(self, base):
        self.base = base
        self._toast_timer = 0.0

        def text(pos, align=TextNode.ALeft, scale=0.045, color=(1, 1, 1, 1)):
            return OnscreenText(
                text="",
                pos=pos,
                scale=scale,
                fg=color,
                align=align,
                mayChange=True,
                parent=base.a2dTopLeft if align == TextNode.ALeft else base.a2dTopRight,
                shadow=(0, 0, 0, 0.75),
            )

        self.status = text((0.04, -0.10))
        self.cargo = text((0.04, -0.17), color=(1.0, 0.80, 0.35, 1.0))
        self.distance = text((0.04, -0.23))
        self.scores = text((-0.04, -0.10), align=TextNode.ARight)

        # Round / clock banner, centred at the top where a scoreboard belongs.
        self.match = OnscreenText(
            text="",
            pos=(0, -0.12),
            scale=0.055,
            fg=(0.95, 0.95, 1.0, 1.0),
            align=TextNode.ACenter,
            mayChange=True,
            parent=base.a2dTopCenter,
            shadow=(0, 0, 0, 0.8),
        )

        self.toast = OnscreenText(
            text="",
            pos=(0, -0.35),
            scale=0.06,
            fg=(1.0, 0.85, 0.4, 1.0),
            align=TextNode.ACenter,
            mayChange=True,
            parent=base.aspect2d,
            shadow=(0, 0, 0, 0.75),
        )

        # There is no control-hint line here any more. It was reference material printed
        # under the crosshair for the whole match; it now lives in the settings screen,
        # which is where you go when you want to look something up. See
        # keybinds.ACTIONS.

        # --- status bars ---
        #
        # Bottom centre, stacked above the help line, so a glance down covers "can I take
        # another hit", "can I shoot" and "can I run" without leaving the flight picture.
        self._shield_track, self._shield_fill = self._bar(base, BAR_Y_SHIELD)
        self._hull_track, self._hull_fill = self._bar(base, BAR_Y_HULL)
        self._energy_track, self._energy_fill = self._bar(base, BAR_Y_ENERGY)
        self._boost_track, self._boost_fill = self._bar(base, BAR_Y_BOOST)

        def bar_label(y, color):
            return OnscreenText(
                text="",
                pos=(BAR_WIDTH / 2 + 0.03, y - 0.010),
                scale=0.035,
                fg=color,
                align=TextNode.ALeft,
                mayChange=True,
                parent=base.a2dBottomCenter,
                shadow=(0, 0, 0, 0.75),
            )

        self.shield_label = bar_label(BAR_Y_SHIELD, SHIELD_FULL)
        self.energy_label = bar_label(BAR_Y_ENERGY, (0.80, 0.85, 0.95, 1.0))
        # Hull in absolute numbers, not just a bar: "3 / 30" tells you how many more hits
        # you have in a way that a green sliver does not.
        self.hull_label = bar_label(BAR_Y_HULL, HULL_FULL)
        self.boost_label = bar_label(BAR_Y_BOOST, BOOST_FULL)

        # Lightning bolt to the left of the boost bar, so the yellow row is identifiable
        # at a glance without a text label competing with the energy readout.
        self._boost_icon = _bolt_icon()
        self._boost_icon.reparentTo(base.a2dBottomCenter)
        self._boost_icon.setPos(-(BAR_WIDTH / 2 + 0.045), 0, BAR_Y_BOOST)
        self._boost_icon.setScale(0.032)

        # Start hidden. The bars are shown by _set_bars once there is a PlayerState to
        # drive them, and leaving a game hides them again — but between launching and
        # joining anything, nothing had ever hidden them, so three empty tracks and a
        # lightning bolt sat over the menus. The same reasoning as the help line: the
        # front end is not the place for flight instruments.
        for np in self._bar_nodes():
            np.hide()

        # Respawn countdown: big and central, because being dead is the one state where
        # the player has nothing else to read.
        self.respawn = OnscreenText(
            text="",
            pos=(0, 0.10),
            scale=0.095,
            fg=(1.0, 0.42, 0.36, 1.0),
            align=TextNode.ACenter,
            mayChange=True,
            parent=base.aspect2d,
            shadow=(0, 0, 0, 0.85),
        )

    def _bar(self, base, y: float):
        """A dark track with a coloured fill anchored to its left edge.

        The fill is scaled rather than resized: DirectFrame's frameSize is a construction
        parameter and rewriting it every frame churns geometry, while setScale on the X
        axis is a transform. Anchoring at the left means the bar drains rightward.
        """
        track = DirectFrame(
            frameColor=(0.10, 0.12, 0.16, 0.85),
            frameSize=(-BAR_WIDTH / 2, BAR_WIDTH / 2, -BAR_HEIGHT / 2, BAR_HEIGHT / 2),
            pos=(0, 0, y),
            parent=base.a2dBottomCenter,
        )
        fill = DirectFrame(
            frameColor=ENERGY_FULL,
            # Modelled 0..1 wide so the X scale IS the fraction.
            frameSize=(0, BAR_WIDTH, -BAR_HEIGHT / 2 + 0.004, BAR_HEIGHT / 2 - 0.004),
            pos=(-BAR_WIDTH / 2, 0, y),
            parent=base.a2dBottomCenter,
        )
        return track, fill

    def _all_nodes(self):
        return (self.status, self.cargo, self.distance, self.scores, self.match,
                self.toast, self.respawn, self.energy_label, self.hull_label,
                self.boost_label, self.shield_label) + self._bar_nodes()

    def set_visible(self, visible: bool) -> None:
        """Show or hide the whole HUD.

        Full-screen overlays need this. The HUD hangs off a2dTopLeft, a2dBottomCenter and
        friends rather than off one root, and those are drawn after an overlay's frame —
        so without this the clock, the scores and the status bars sit on top of the
        end-of-match table instead of behind it.
        """
        for np in self._all_nodes():
            np.show() if visible else np.hide()

        if visible:
            # The bars are driven by _set_bars and must not reappear just because the
            # overlay closed: there may be no PlayerState behind them.
            for np in self._bar_nodes():
                np.hide()

    def _bar_nodes(self):
        return (self._hull_track, self._hull_fill,
                self._energy_track, self._energy_fill,
                self._boost_track, self._boost_fill,
                self._boost_icon,
                self._shield_track, self._shield_fill)

    def _shield_nodes(self):
        return (self._shield_track, self._shield_fill)

    def _set_shield(self, player) -> None:
        """The item shield, drawn only while one is up.

        Shields do not stack: using another replaces whatever was left, so this is always
        one pool of 30 rather than a total that could climb. The label is absolute for the
        same reason the hull's is — "12 / 30" says how many more hits it will eat, which a
        shrinking green sliver does not.
        """
        pool = getattr(player, "item_shield", 0.0)
        if pool <= 0:
            for np in self._shield_nodes():
                np.hide()
            self.shield_label.setText("")
            return

        for np in self._shield_nodes():
            np.show()
        frac = max(0.0, min(1.0, pool / SHIELD_POOL))
        self._shield_fill.setScale(max(0.001, frac), 1, 1)
        low = frac <= 0.34
        self._shield_fill["frameColor"] = SHIELD_LOW if low else SHIELD_FULL
        self.shield_label.setText(f"{math.ceil(pool - 1e-6):.0f} / {int(SHIELD_POOL)}")
        self.shield_label.setFg(Vec4(*(SHIELD_LOW if low else SHIELD_FULL)))

    def _set_bars(self, player, boosting: bool = False) -> None:
        """Drive the hull, charge and boost bars from the personal PlayerState."""
        if player is None:
            for np in self._bar_nodes():
                np.hide()
            self.energy_label.setText("")
            self.hull_label.setText("")
            self.boost_label.setText("")
            self.shield_label.setText("")
            self.respawn.setText("")
            return

        for np in self._bar_nodes():
            np.show()
        # Shown or hidden on its own terms, after the blanket show above.
        self._set_shield(player)

        hull = max(0.0, min(1.0, player.health))
        self._hull_fill.setScale(max(0.001, hull), 1, 1)
        low = hull <= 0.34
        self._hull_fill["frameColor"] = HULL_LOW if low else HULL_FULL

        # Rounded up, so a ship that is still flying never reads as 0 HP.
        points = math.ceil(hull * proto.SHIP_MAX_HEALTH - 1e-6)
        self.hull_label.setText(f"{points} / {int(proto.SHIP_MAX_HEALTH)}")
        self.hull_label.setFg(Vec4(*(HULL_LOW if low else HULL_FULL)))

        max_energy = player.max_energy or 1.0
        charge = max(0.0, min(1.0, player.energy / max_energy))
        self._energy_fill.setScale(max(0.001, charge), 1, 1)
        # Red below one full shot: the bar's only real question is "can I fire now".
        self._energy_fill["frameColor"] = (
            ENERGY_LOW if player.energy < 1.0 else ENERGY_FULL
        )

        shots = int(player.energy)
        self.energy_label.setText(f"{shots} / {int(max_energy)}")
        self.energy_label.setFg(
            Vec4(*(ENERGY_LOW if shots == 0 else (0.80, 0.85, 0.95, 1.0)))
        )

        max_boost = player.max_boost or 1.0
        tank = max(0.0, min(1.0, player.boost / max_boost))
        self._boost_fill.setScale(max(0.001, tank), 1, 1)

        # Four states, and they have to look different, because pressing the key does
        # something different in each: burning, ready, charging-but-not-yet-usable, and
        # stalled. The old bar collapsed the last two into "short and dull", which made a
        # dead key look like a nearly-usable one.
        if player.boost_stalled:
            fill = BOOST_STALLED
            self.boost_label.setText(f"STALLED  {player.boost_cooldown:.1f}s")
            self.boost_label.setFg(Vec4(*BOOST_STALLED))
        elif boosting:
            fill = BOOST_BURNING
            self.boost_label.setText("")
        elif player.boost < BOOST_REENGAGE_SECONDS:
            fill = BOOST_CHARGING
            self.boost_label.setText("charging")
            self.boost_label.setFg(Vec4(*BOOST_CHARGING))
        elif tank <= 0.001:
            fill = BOOST_EMPTY
            self.boost_label.setText("")
        else:
            fill = BOOST_FULL
            self.boost_label.setText("")

        self._boost_fill["frameColor"] = fill
        self._boost_icon.setColorScale(
            Vec4(1.4, 1.4, 1.4, 1.0) if boosting else Vec4(1, 1, 1, 1)
        )

        if player.grounded:
            # No countdown, because none is coming. Saying "respawning in 0" to someone
            # who is out for the round is the worst possible thing to show them.
            self.respawn.setText("DESTROYED\nyour team is out of lives")
        elif player.dead:
            self.respawn.setText(f"DESTROYED\nrespawning in {player.respawn_in:.0f}")
        else:
            self.respawn.setText("")

    def reset(self) -> None:
        """Blank every readout. Called when leaving a game so stale scores and cargo
        text do not linger behind the main menu."""
        for label in (self.status, self.cargo, self.distance, self.scores,
                      self.toast, self.match, self.energy_label, self.hull_label,
                      self.boost_label, self.shield_label, self.respawn):
            label.setText("")
        for np in self._bar_nodes():
            np.hide()
        self._toast_timer = 0.0

    def show_toast(self, msg: str, seconds: float = TOAST_SECONDS) -> None:
        """Put a message in the toast slot. Re-setting it each frame is how a persistent
        condition (a station under fire) stays up for as long as it lasts."""
        self.toast.setText(msg)
        self._toast_timer = seconds

    def show_unlock(self, name: str) -> None:
        """Announce an unlocked cosmetic. Uses the same toast slot as game events —
        unlocks are rare enough that competing for it is not a problem."""
        self.toast.setText(f"UNLOCKED:  {name}")
        self._toast_timer = TOAST_SECONDS * 1.6

    def show_event(self, event: proto.Event, own_player: int, own_ship: int = 0) -> None:
        # A kill is worth announcing whoever scored it — that is the one thing in a
        # firefight everybody wants to know. Everything else is only surfaced when it
        # happened to *this* player, or a 16-player match is a wall of other people's
        # noise.
        if event.type == proto.EVENT_SHIP_DESTROYED:
            if event.player == own_player:
                self.toast.setText("KILL!")
            elif event.entity == own_ship:
                self.toast.setText("YOU WERE DESTROYED")
            else:
                self.toast.setText("a ship went down")
            self._toast_timer = TOAST_SECONDS
            return

        if event.player != own_player and event.type != proto.EVENT_DESTROYED:
            return

        name = EVENT_NAMES.get(event.type, "?")
        if event.type == proto.EVENT_DEPOSITED:
            msg = f"banked {event.value:,.0f} credits"
        elif event.type == proto.EVENT_DAMAGED:
            msg = "cargo damaged"
        elif event.type == proto.EVENT_ASTEROID_DESTROYED:
            tier = proto.TIER_NAMES.get(int(event.value), "rock")
            msg = f"shattered a {tier}"
        elif event.type == proto.EVENT_ITEM_PICKED_UP:
            item = proto.ITEM_INFO.get(int(event.value), ("something",))[0]
            msg = f"picked up {item}"
        elif event.type == proto.EVENT_ITEM_USED:
            # No toast. The hotbar slot emptying and the effect line appearing already
            # say it, and a toast for something you deliberately pressed is noise.
            return
        elif event.type in (proto.EVENT_SHIP_HIT, proto.EVENT_ASTEROID_HIT,
                            proto.EVENT_MOTHERSHIP_HIT, proto.EVENT_SHIELD_ABSORBED):
            # Hits land several times a second; a toast per hit would strobe. The station
            # alarm is raised from _react_to_event instead, which knows whose it is.
            return
        else:
            msg = name

        self.toast.setText(msg)
        self._toast_timer = TOAST_SECONDS

    def update(
        self,
        dt: float,
        connected: bool,
        error: str | None,
        held_radius: float | None,
        held_tier: int | None,
        distance_home: float | None,
        teams,
        own_team: int,
        buffered: int,
        match=None,
        player=None,
        untowable: bool = False,
        boosting: bool = False,
        stations=None,
        hill=None,
    ) -> None:
        if error:
            self.status.setText(f"DISCONNECTED: {error}")
            self.status.setFg(Vec4(1.0, 0.4, 0.35, 1.0))
        elif not connected:
            self.status.setText("connecting...")
            self.status.setFg(Vec4(0.85, 0.85, 0.5, 1.0))
        else:
            self.status.setText(f"online   buffer {buffered}")
            self.status.setFg(Vec4(0.6, 0.9, 0.65, 1.0))

        # Deposit volume radius on the server (sim.Config.DepositRadius). Cargo banks
        # automatically once it is inside, so the HUD counts you down to it rather than
        # asking for a button press.
        deposit_radius = 45.0

        if held_radius is not None:
            if held_tier in proto.TIER_NEEDS_CREW:
                # Without this the rock simply refuses to move and the player has no way
                # to know the beam is working but under-strength.
                self.cargo.setText("MASSIVE  -  needs a second beam!")
                self.cargo.setFg(Vec4(1.0, 0.45, 0.40, 1.0))
            elif held_tier in proto.TIER_PRECIOUS:
                # A core is worth many ordinary hauls, so losing one to a careless
                # collision is the expensive mistake in the game. Say so.
                self.cargo.setText("CORE SEAM  -  get it back in one piece!")
                self.cargo.setFg(Vec4(0.95, 0.85, 1.0, 1.0))
            else:
                name = proto.TIER_NAMES.get(held_tier, "cargo")
                # Mass is not on the wire, but it tracks radius cubed on the server, so
                # size is an honest stand-in for "how much will this fight me".
                self.cargo.setText(f"HAULING  {name}  {held_radius * 2:.1f} m across")
                self.cargo.setFg(Vec4(1.0, 0.80, 0.35, 1.0))
        elif untowable:
            # The server refuses the lock outright on the biggest rocks. Left
            # unexplained that reads as a broken tractor beam rather than as a rule.
            self.cargo.setText("TOO BIG TO TOW  -  shoot it apart for the core!")
            self.cargo.setFg(Vec4(0.80, 0.72, 1.0, 1.0))
        else:
            self.cargo.setText("")

        # King of the Hill replaces the distance-to-base line entirely: in that mode the
        # station is not where you are trying to be, and telling someone how far home is
        # while they are fighting over a zone is answering a question nobody asked.
        if hill is not None:
            self._set_hill_line(hill, own_team)
        elif distance_home is not None:
            if held_radius is not None:
                if distance_home <= deposit_radius:
                    self.distance.setText("DELIVERING...")
                    self.distance.setFg(Vec4(0.45, 1.0, 0.55, 1.0))
                else:
                    # No arrow any more: the range and your own team's colours on the
                    # station are the navigation.
                    self.distance.setText(
                        f"BASE  {distance_home:,.0f} m  -  head for your colours"
                    )
                    self.distance.setFg(Vec4(0.55, 1.0, 0.65, 1.0))
            else:
                self.distance.setText(f"base {distance_home:,.0f} m")
                self.distance.setFg(Vec4(1.0, 1.0, 1.0, 1.0))
        else:
            self.distance.setText("")

        if teams:
            lines = []
            for t in sorted(teams, key=lambda x: x.team_id):
                marker = ">" if t.team_id == own_team else " "
                wins = ""
                if match is not None:
                    won = match.round_wins.get(t.team_id, 0)
                    # Pips read faster than a number at a glance mid-flight.
                    wins = "  " + ("*" * won if won else "-")
                label = t.name or f"team {t.team_id}"
                # Lives are the number that decides a round once a fight starts, so they
                # sit right next to the score rather than in a corner of their own.
                lives = f"  x{t.lives}" if t.lives else "  OUT"
                lines.append(f"{marker} {label}  {t.score:,.0f}{lives}{wins}"
                             + self._station_note(t, stations))
            self.scores.setText("\n".join(lines))

        self._update_match_banner(match, own_team)
        self._set_bars(player, boosting)

        if self._toast_timer > 0.0:
            self._toast_timer -= dt
            if self._toast_timer <= 0.0:
                self.toast.setText("")

    def _set_hill_line(self, hill, own_team: int) -> None:
        """Where the control point is and who is holding it."""
        dist = hill.get("distance")
        who = hill.get("team", proto.NO_TEAM)
        inside = dist is not None and dist <= hill.get("radius", 0)

        if who == proto.NO_TEAM:
            # Empty and contested both pay nobody, but they call for opposite actions:
            # one says go there, the other says you are already losing a fight there.
            if hill.get("contested"):
                self.distance.setText("HILL CONTESTED  -  clear them out")
                self.distance.setFg(Vec4(1.0, 0.72, 0.30, 1.0))
            elif inside:
                self.distance.setText("HOLDING THE HILL")
                self.distance.setFg(Vec4(0.45, 1.0, 0.55, 1.0))
            else:
                self.distance.setText(f"HILL  {dist:,.0f} m  -  unclaimed" if dist
                                      else "HILL  -  unclaimed")
                self.distance.setFg(Vec4(0.85, 0.88, 0.95, 1.0))
            return

        if who == own_team:
            self.distance.setText("YOUR HILL  -  hold it")
            self.distance.setFg(Vec4(0.45, 1.0, 0.55, 1.0))
        else:
            label = f"HILL  {dist:,.0f} m  -  " if dist is not None else "HILL  -  "
            self.distance.setText(label + f"held by {proto.team_color_name(who)}")
            self.distance.setFg(Vec4(1.0, 0.45, 0.40, 1.0))

    @staticmethod
    def _station_note(team, stations) -> str:
        """The station's condition, appended to a team's scoreboard line.

        Only shown once a station has been hit. A row of "2500/2500" against every team is
        noise for the majority of a match in which nobody attacks anyone's home — but the
        moment yours is being shot at, this is the only warning you get while you are out
        in the belt with your back turned.
        """
        if not stations:
            return ""
        health = stations.get(team.team_id)
        if health is None or health >= 0.999:
            # Still worth flagging what a station is armed with, even undamaged: it is
            # what tells you whether flying at it is a plan or a mistake.
            marks = ("S" * team.shields) + ("T" * team.turrets)
            return f"   [{marks}]" if marks else ""

        points = math.ceil(health * proto.MOTHERSHIP_MAX_HEALTH - 1e-6)
        return f"   BASE {points:,}"

    def _update_match_banner(self, match, own_team: int) -> None:
        if match is None:
            self.match.setText("")
            return

        if match.phase == proto.PHASE_MATCH_OVER:
            if match.winner == 0xFF:
                self.match.setText("MATCH DRAWN")
                self.match.setFg(Vec4(0.9, 0.9, 0.9, 1.0))
            elif match.winner == own_team:
                self.match.setText("MATCH WON")
                self.match.setFg(Vec4(0.45, 1.0, 0.55, 1.0))
            else:
                self.match.setText(f"MATCH LOST  -  team {match.winner} wins")
                self.match.setFg(Vec4(1.0, 0.45, 0.40, 1.0))
            return

        if match.phase == proto.PHASE_LOBBY:
            # A client can be in the world during a lobby — anything started with --url
            # skips the menu entirely. Without this the round line read "ROUND 0 of 5
            # 0:00", which describes a broken match rather than one that has not been
            # started yet. There is no countdown to show: the lobby ends when the host
            # ends it.
            self.match.setText("WAITING FOR THE HOST TO START")
            self.match.setFg(Vec4(0.85, 0.85, 0.5, 1.0))
            return

        if match.phase == proto.PHASE_WARMUP:
            self.match.setText(f"WARMUP  -  starts in {match.time_left}s")
            self.match.setFg(Vec4(0.85, 0.85, 0.5, 1.0))
            return

        if match.phase == proto.PHASE_INTERMISSION:
            # The shop panel owns the detail during intermissions.
            self.match.setText("")
            return

        if match.best_of == 0:
            # Sandbox: an endless round with no clock and no shop.
            self.match.setText("FREE PLAY")
            self.match.setFg(Vec4(0.75, 0.80, 0.90, 1.0))
            return

        mins, secs = divmod(match.time_left, 60)
        self.match.setText(
            f"ROUND {match.round} of {match.best_of}   {mins}:{secs:02d}"
        )
        # Red for the last 30 seconds, so the clock pressure is felt without reading it.
        self.match.setFg(
            Vec4(1.0, 0.45, 0.40, 1.0) if match.time_left <= 30
            else Vec4(0.95, 0.95, 1.0, 1.0)
        )
