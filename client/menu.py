"""Front-end menus: main, play (host/join), host setup, and settings.

A conventional stack of screens rather than one panel. Only one is visible at a time and
`_show` swaps between them, so adding a screen later (Online Multiplayer) means adding a
builder, not restructuring.

The host setup screen collects match rules and passes them to the bundled server as
command-line flags — the server already accepts them, so no extra protocol is needed for
the host to configure a match.
"""

from __future__ import annotations

import os
import socket
import subprocess
import sys
import time

from direct.gui.DirectGui import (
    DirectButton,
    DirectEntry,
    DirectFrame,
    DirectLabel,
)
from panda3d.core import TextNode, WindowProperties

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
import clipboard  # noqa: E402
import controls as controls_mod  # noqa: E402
import icons  # noqa: E402
import keybinds  # noqa: E402

SERVER_PORT = 8080

TITLE_COLOR = (1.0, 0.85, 0.35, 1.0)
BODY_COLOR = (0.84, 0.88, 0.95, 1.0)
MUTED_COLOR = (0.55, 0.58, 0.66, 1.0)
GREEN = (0.20, 0.45, 0.32, 1.0)
BLUE = (0.20, 0.32, 0.55, 1.0)
GREY = (0.18, 0.20, 0.26, 1.0)
DISABLED = (0.13, 0.14, 0.17, 1.0)


def local_ip() -> str:
    """Best guess at this machine's LAN address.

    Opens a UDP socket toward a public address and reads back which local interface the
    OS chose. Nothing is sent. `gethostbyname(gethostname())` commonly returns 127.0.0.1
    and is useless for telling a friend where to connect.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("8.8.8.8", 80))
        return s.getsockname()[0]
    except Exception:
        return "127.0.0.1"
    finally:
        s.close()


def find_server_executable() -> str | None:
    """Locate the bundled server, both packaged and running from source."""
    here = os.path.dirname(os.path.abspath(sys.argv[0]))
    candidates = [
        os.path.join(here, "server.exe"),
        os.path.join(here, "..", "server.exe"),
    ]
    if "__file__" in globals():
        candidates += [
            os.path.join(os.path.dirname(__file__), "..", "bin", "server.exe"),
            os.path.join(os.path.dirname(__file__), "..", "server.exe"),
        ]
    for c in candidates:
        if os.path.isfile(c):
            return os.path.abspath(c)
    return None


def _port_in_use(port: int) -> bool:
    """Is something already listening locally on this port?"""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(0.25)
        return sock.connect_ex(("127.0.0.1", port)) == 0


def _pct(v: float) -> str:
    return "Off" if v <= 0 else f"{int(round(v * 100))}%"


def _nearest(values: list[float], target: float) -> int:
    """Index of the closest option. A value from a config file or an older build will
    not necessarily be one of the presets."""
    best, best_d = 0, None
    for i, v in enumerate(values):
        d = abs(v - target)
        if best_d is None or d < best_d:
            best, best_d = i, d
    return best


class Cycler:
    """A labelled setting the player steps through with < and > buttons.

    Discrete choices rather than a slider or a text field: every match setting here has
    a small set of sensible values, and this makes an invalid one impossible to enter.
    """

    def __init__(self, parent, label, values, index, y, fmt=str, on_change=None):
        self.values = values
        self.index = index
        self.fmt = fmt
        self.on_change = on_change

        DirectLabel(
            text=label,
            scale=0.052,
            pos=(-0.62, 0, y),
            text_fg=BODY_COLOR,
            text_align=TextNode.ALeft,
            frameColor=(0, 0, 0, 0),
            parent=parent,
        )
        DirectButton(
            text="<",
            scale=0.055,
            pos=(0.26, 0, y),
            frameColor=GREY,
            text_fg=(1, 1, 1, 1),
            relief=1,
            pad=(0.22, 0.10),
            command=self._step,
            extraArgs=[-1],
            parent=parent,
        )
        self.readout = DirectLabel(
            text="",
            scale=0.052,
            pos=(0.52, 0, y),
            text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0),
            parent=parent,
        )
        DirectButton(
            text=">",
            scale=0.055,
            pos=(0.78, 0, y),
            frameColor=GREY,
            text_fg=(1, 1, 1, 1),
            relief=1,
            pad=(0.22, 0.10),
            command=self._step,
            extraArgs=[1],
            parent=parent,
        )
        self._refresh()

    def _step(self, delta: int) -> None:
        self.index = (self.index + delta) % len(self.values)
        self._refresh()
        if self.on_change:
            self.on_change(self.value)

    def _refresh(self) -> None:
        self.readout.setText(self.fmt(self.values[self.index]))

    @property
    def value(self):
        return self.values[self.index]


class Menus:
    """Owns every front-end screen and the server process if this client hosts."""

    def __init__(self, base, on_connect, settings, profile=None, on_apply_loadout=None,
                 on_start_match=None, on_leave_lobby=None, on_set_team=None,
                 player_name="pilot", binds=None, on_rebind=None,
                 on_settings_changed=None):
        self.base = base
        self.on_connect = on_connect
        self.settings = settings  # dict shared with the game for sensitivity etc.
        self.profile = profile
        self.on_apply_loadout = on_apply_loadout
        self.on_start_match = on_start_match
        self.on_leave_lobby = on_leave_lobby
        self.on_set_team = on_set_team
        # Shown in the seat you are about to take, before there is a roster to read
        # it from. The server sanitises the real one; this is only ever drawn.
        self.player_name = player_name or "pilot"
        # The shared keybinding registry, and the callback that makes a
        # change take effect without leaving the screen.
        self.binds = binds if binds is not None else keybinds.Keybinds()
        self.on_rebind = on_rebind
        self.on_settings_changed = on_settings_changed
        self._rebinding: str | None = None
        self.server_proc: subprocess.Popen | None = None
        self.team_name = ""
        # Which crew to ask for, 0xFF = let the server choose. Set when a screen connects.
        self.team_pref = 0xFF

        # lobby_active means this client has opened a lobby and is sitting in it: the
        # host screen stays up, connected, until the match is started.
        self.lobby_active = False
        self.own_player = 0
        self._roster = None
        self.editor = None

        # Oversized so it covers the whole window whatever the aspect ratio — a narrower
        # panel let the in-game HUD show around the edges of the menus.
        self.backdrop = DirectFrame(
            frameColor=(0.02, 0.03, 0.06, 0.97),
            frameSize=(-2.2, 2.2, -1.1, 1.1),
            parent=base.aspect2d,
        )

        self.screens: dict[str, DirectFrame] = {}
        self._build_main()
        self._build_play()
        self._build_host()
        self._build_settings()

        if self.profile is not None:
            from shipeditor import ShipEditor  # local import keeps the menu importable
                                               # without a profile (tests, tools)

            self.editor = ShipEditor(base, self.backdrop, self.profile,
                                     lambda: self.show("main"),
                                     on_apply=self.on_apply_loadout)
            self.screens["editor"] = self.editor.frame

        self.current = ""
        self.show("main")

    # --- screen plumbing ---------------------------------------------------

    def _screen(self, name: str) -> DirectFrame:
        f = DirectFrame(frameColor=(0, 0, 0, 0), parent=self.backdrop)
        f.hide()
        self.screens[name] = f
        return f

    def show(self, name: str) -> None:
        for key, frame in self.screens.items():
            frame.show() if key == name else frame.hide()
        self.current = name

        if name == "host":
            self._refresh_address()
            self._refresh_lobby()

        # The editor owns a 3D preview node that has to be hidden with its screen, not
        # just its 2D frame.
        if self.editor is not None:
            self.editor.show() if name == "editor" else self.editor.hide()

    def hide(self) -> None:
        self.backdrop.hide()
        if self.editor is not None:
            self.editor.hide()

    def show_all(self) -> None:
        self.backdrop.show()
        self.show(self.current or "main")

    def update(self, dt: float) -> None:
        if self.editor is not None:
            self.editor.update(dt)

    def _title(self, parent, text, y=0.72, scale=0.11):
        return DirectLabel(
            text=text, scale=scale, pos=(0, 0, y), text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0), parent=parent,
        )

    def _button(self, parent, text, y, command, color=BLUE, width=0.42, enabled=True):
        return DirectButton(
            text=text,
            scale=0.075,
            pos=(0, 0, y),
            frameColor=color if enabled else DISABLED,
            text_fg=(1, 1, 1, 1) if enabled else (0.42, 0.44, 0.5, 1.0),
            relief=1,
            pad=(width, 0.13),
            command=command if enabled else None,
            parent=parent,
        )

    # --- main --------------------------------------------------------------

    def _build_main(self) -> None:
        s = self._screen("main")
        self._title(s, "ASTEROID SALVAGE", y=0.66, scale=0.14)
        DirectLabel(
            text="Haul asteroids home. Best of 5 rounds.",
            scale=0.055, pos=(0, 0, 0.52), text_fg=BODY_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )

        self._button(s, "PLAY", 0.30, lambda: self.show("play"), GREEN)
        self._button(s, "SHIP EDITOR", 0.12, lambda: self.show("editor"), BLUE,
                     width=0.28)
        self._button(s, "SETTINGS", -0.06, lambda: self.show("settings"), BLUE)
        # Deliberately dead for now — the slot is here so the shape of the menu does not
        # change when internet play arrives.
        self._button(s, "ONLINE MULTIPLAYER", -0.26, None, DISABLED, width=0.16,
                     enabled=False)
        DirectLabel(
            text="coming later",
            scale=0.042, pos=(0, 0, -0.38), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self._button(s, "QUIT", -0.56, self.base.user_exit, GREY)

    # --- play (host or join) ----------------------------------------------

    def _build_play(self) -> None:
        s = self._screen("play")
        self._title(s, "PLAY")

        self._button(s, "HOST A GAME", 0.34, lambda: self.show("host"), GREEN)
        DirectLabel(
            text="Set the rules and run the server on this PC.",
            scale=0.046, pos=(0, 0, 0.22), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )

        DirectLabel(
            text="JOIN A GAME ON YOUR NETWORK",
            scale=0.055, pos=(0, 0, 0.02), text_fg=BODY_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self.join_entry = DirectEntry(
            initialText="", scale=0.075, pos=(-0.42, 0, -0.12), width=11, numLines=1,
            focus=0, frameColor=(0.10, 0.12, 0.18, 1.0), text_fg=(1, 1, 1, 1),
            text_align=TextNode.ALeft, command=lambda _t: self._join(), parent=s,
        )
        DirectLabel(
            text="host address, e.g. 192.168.1.42",
            scale=0.042, pos=(0, 0, -0.22), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self._button(s, "JOIN", -0.38, self._join, BLUE)

        self.play_status = DirectLabel(
            text="", scale=0.05, pos=(0, 0, -0.56), text_fg=(1.0, 0.5, 0.45, 1.0),
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self._button(s, "BACK", -0.76, lambda: self.show("main"), GREY, width=0.5)

    # --- host setup --------------------------------------------------------

    def _build_host(self) -> None:
        s = self._screen("host")
        self._title(s, "HOST A GAME", y=0.86, scale=0.075)

        # The settings live in their own frame, scaled down and pushed left, so the right
        # half of the screen is free for the lobby. Scaling a parent rather than moving
        # every widget keeps the rows written in one comfortable coordinate space — the
        # Cycler class hard-codes its own column positions, and they would otherwise all
        # have to be threaded through it.
        s = DirectFrame(frameColor=(0, 0, 0, 0), parent=self.screens["host"])
        s.setScale(0.78)
        s.setPos(-0.60, 0, 0.02)
        self.host_cfg = s

        # Rows at 0.09 spacing. The pitch is what fixes two collisions the old layout
        # had: the team-name field sat at -0.14 on top of the team-colour cycler at
        # -0.16, and the per-mode panels began at -0.30 underneath the address line.
        # Mode first, because it changes what the rest of the screen means. The rows
        # below it are shared by every mode; each mode's own settings live in their own
        # frame, shown and hidden by _on_mode so the screen only ever offers settings
        # that apply to the game being started.
        self.cfg_mode = Cycler(
            s, "Game mode",
            [proto.MODE_SALVAGE, proto.MODE_HOARD, proto.MODE_KOTH], 0, 0.70,
            lambda v: proto.MODE_NAMES.get(v, "?"),
            on_change=lambda v: self._on_mode(v),
        )
        self.mode_blurb = DirectLabel(
            text="", scale=0.040, pos=(-0.62, 0, 0.635), text_fg=MUTED_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
        )

        self.cfg_rounds = Cycler(
            s, "Rounds (best of)", [1, 3, 5, 7], 2, 0.55, lambda v: f"{v}"
        )
        self.cfg_round_len = Cycler(
            s, "Round length", [60, 90, 120, 180, 240, 300], 3, 0.46,
            lambda v: f"{v // 60}:{v % 60:02d}",
        )
        self.cfg_intermission = Cycler(
            s, "Shop time", [20, 30, 45, 60], 2, 0.37, lambda v: f"{v}s"
        )
        # Lives per team per round. The server clamps to 6-20 (sim.MinLives/MaxLives);
        # the presets step by two so the whole range fits without a long cycle.
        self.cfg_lives = Cycler(
            s, "Lives per team", [6, 8, 10, 12, 14, 16, 18, 20], 3, 0.28,
            lambda v: f"{v}",
        )
        # Teams and players per team decide how many seats the lobby draws, so both
        # redraw it — changing them before anyone has joined is the normal case.
        self.cfg_teams = Cycler(
            s, "Teams", [1, 2, 3, 4], 3, 0.19, lambda v: f"{v}",
            on_change=lambda _v: self._refresh_lobby(),
        )
        self.cfg_teamsize = Cycler(
            s, "Players per team", [1, 2, 3, 4, 5, 6], 3, 0.10, lambda v: f"{v}",
            on_change=lambda _v: self._refresh_lobby(),
        )
        self.cfg_coop = Cycler(
            s, "Teams play", [0, 1], 0, 0.01, lambda v: "Co-op" if v else "Versus",
            on_change=lambda _v: self._refresh_lobby(),
        )
        # Which crew you join. Sent as the Hello's team preference, so it applies to a
        # game somebody else is hosting too — the server honours it while that crew has a
        # seat and falls back to the emptiest one when it does not.
        #
        # 0xFF is "auto", and it is the default: in a four-player game nobody wants to
        # negotiate crews, and the lobby shows where everyone actually landed.
        self.cfg_team = Cycler(
            s, "Your team", [0xFF, 0, 1, 2, 3], 0, -0.08,
            lambda v: "Auto" if v == 0xFF else proto.DEFAULT_TEAM_NAMES[v],
            on_change=lambda _v: self._refresh_lobby(),
        )
        # There is no colour picker. Picking a crew already picks a colour — each one
        # wears its own by default — and two settings for one decision meant you could
        # choose Team B and then paint it red, which is what Team A looks like. The
        # sixteen-colour palette is still on the wire (0x06 SetTeamColor); nothing in the
        # front end sends it any more.

        # --- per-mode options ---
        #
        # One frame per mode, all built up front and only one ever visible. Building them
        # lazily would mean a mode's settings did not exist until it had been selected
        # once, which is exactly when _host would go looking for them.
        self.mode_panels: dict[int, DirectFrame] = {}

        salvage = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        DirectLabel(
            text="No extra settings - the classic game.",
            scale=0.040, pos=(-0.62, 0, -0.32), text_fg=MUTED_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=salvage,
        )
        self.mode_panels[proto.MODE_SALVAGE] = salvage

        hoard = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        self.cfg_hoard_target = Cycler(
            hoard, "Points to win a round", [0, 2000, 3000, 4000, 6000, 8000], 3, -0.32,
            lambda v: "no target" if v == 0 else f"{v:,}",
        )
        self.cfg_hoard_bonus = Cycler(
            hoard, "Rock value", [1.0, 1.3, 1.6, 2.0, 3.0], 2, -0.41,
            lambda v: f"x{v:g}",
        )
        self.mode_panels[proto.MODE_HOARD] = hoard

        koth = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        self.cfg_koth_target = Cycler(
            koth, "Points to win a round", [200, 350, 500, 750, 1000], 2, -0.32,
            lambda v: f"{v:,}",
        )
        self.cfg_koth_rate = Cycler(
            koth, "Points per second", [3, 5, 7, 10, 15], 2, -0.41, lambda v: f"{v}/s",
        )
        self.cfg_koth_shift = Cycler(
            koth, "Hill moves every", [0, 20, 30, 45, 60, 90], 3,
            -0.50, lambda v: "never" if v == 0 else f"{v}s",
        )
        self.cfg_koth_credits = Cycler(
            koth, "Credits per second", [0, 10, 20, 35, 50], 2, -0.59,
            lambda v: "none" if v == 0 else f"{v} cr/s",
        )
        self.mode_panels[proto.MODE_KOTH] = koth

        self._on_mode(self.cfg_mode.value)

        DirectLabel(
            text="Your team name",
            scale=0.052, pos=(-0.62, 0, -0.19), text_fg=BODY_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
        )
        self.team_entry = DirectEntry(
            initialText="", scale=0.06, pos=(0.10, 0, -0.20), width=12, numLines=1,
            focus=0, frameColor=(0.10, 0.12, 0.18, 1.0), text_fg=(1, 1, 1, 1),
            text_align=TextNode.ALeft, parent=s,
        )

        # Back out to the screen root for everything below and to the right of the
        # settings column — those are laid out full-size.
        root = self.screens["host"]
        self._build_lobby_panel(root)

        self.host_status = DirectLabel(
            text="", scale=0.045, pos=(0, 0, -0.57), text_fg=(0.5, 1.0, 0.6, 1.0),
            frameColor=(0, 0, 0, 0), parent=root,
        )

        # START opens the lobby; it becomes BEGIN MATCH once the server is up and this
        # client is the host. Kept as one button because it is one decision made twice,
        # and two buttons would leave a dead one on screen either side of the join.
        self.host_button = DirectButton(
            text="START", scale=0.072, pos=(-0.34, 0, -0.71), frameColor=GREEN,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.40, 0.13),
            command=self._host_button, parent=root,
        )
        DirectButton(
            text="BACK", scale=0.072, pos=(0.42, 0, -0.71), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.34, 0.13),
            command=self._leave_host, parent=root,
        )

        # The address strip is last, along the bottom. It used to sit at -0.34, where it
        # ran straight through the per-mode settings panel — and being the one thing on
        # the screen you have to read out to other people, it is better as a footer than
        # as a row competing with the settings.
        self.address_label = DirectLabel(
            text="", scale=0.055, pos=(0.06, 0, -0.88), text_fg=TITLE_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=root,
        )
        DirectLabel(
            text="Others join at",
            scale=0.046, pos=(-0.66, 0, -0.88), text_fg=BODY_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=root,
        )
        self.copy_button = DirectButton(
            text="COPY", scale=0.046, pos=(0.72, 0, -0.875), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.30, 0.14),
            command=self._copy_address, parent=root,
        )
        self._refresh_address()

    # --- lobby -------------------------------------------------------------

    def _build_lobby_panel(self, root) -> None:
        """The right-hand column: one block per crew, with a row per seat."""
        self.lobby_frame = DirectFrame(
            frameColor=(0.06, 0.08, 0.13, 0.85),
            frameSize=(-0.60, 0.60, -1.06, 0.10),
            pos=(0.68, 0, 0.50),
            parent=root,
        )
        DirectLabel(
            text="LOBBY", scale=0.058, pos=(0, 0, 0.005), text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0), parent=self.lobby_frame,
        )
        self.lobby_hint = DirectLabel(
            text="", scale=0.038, pos=(0, 0, -0.06), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=self.lobby_frame,
        )
        # Rebuilt whenever the seating changes; see _refresh_lobby.
        self.lobby_rows: list = []
        self._lobby_signature: tuple | None = None
        self._seats_total = 0
        self._team_states = None
        self._refresh_lobby()

    def _refresh_lobby(self, roster=None, team_states=None) -> None:
        """Redraw the seats.

        With no roster this draws the shape of the match the settings describe — empty
        slots for the crews you are about to open. That is deliberately the same widget
        as the populated one: the host sees the seating they configured before anybody
        arrives, and joins land in slots that were already on screen.

        Once connected, the crews come from the server's own TeamState rather than from
        the cyclers on the left. Those are *this* client's settings, and a joiner's are
        whatever they last happened to leave them on — so drawing a joiner's lobby from
        them would show a four-crew match to somebody who had joined a two-crew one.
        """
        if roster is not None:
            self._roster = roster
        if team_states:
            self._team_states = team_states

        roster = getattr(self, "_roster", None)
        team_states = getattr(self, "_team_states", None)
        coop = self.cfg_coop.value == 1

        if team_states:
            team_ids = sorted(t.team_id for t in team_states)
            names = {t.team_id: t.name for t in team_states}
        else:
            team_ids = [0] if coop else list(range(self.cfg_teams.value))
            names = {}

        capacity = (
            roster.capacity if roster is not None
            else (self.cfg_teamsize.value * (self.cfg_teams.value if coop else 1))
        )

        # Which crew is yours: the one the roster says you are on once connected, and
        # before that a preview of where you would land.
        #
        # "Auto" is answerable rather than blank — the server fills crews breadth-first,
        # so the first player through the door takes the first crew's first seat. Showing
        # that is better than showing nothing: the point of the panel is to say where you
        # will be sitting, and "somewhere" is not an answer.
        mine = None
        preview = None
        if roster is not None:
            mine = next((r.team for r in roster.players
                         if r.player_id == self.own_player), None)
        elif not coop:
            pick = self.cfg_team.value
            mine = 0 if pick == 0xFF else pick
            preview = mine

        signature = (
            tuple(team_ids), capacity, coop, tuple(sorted(names.items())), mine, preview,
            self.player_name,
            tuple((r.player_id, r.team, r.name) for r in (roster.players if roster else ())),
            roster.host_id if roster else 0,
        )
        if signature == self._lobby_signature:
            return
        self._lobby_signature = signature

        for node in self.lobby_rows:
            # The rows are a mix of DirectGui widgets and the crown, which is a bare
            # NodePath from LineSegs. They are torn down differently, and calling the
            # wrong one is an AttributeError rather than a leak.
            if hasattr(node, "destroy"):
                node.destroy()
            else:
                node.removeNode()
        self.lobby_rows = []

        joined = len(roster.players) if roster is not None else 0
        self._seats_total = len(team_ids) * capacity
        self.lobby_hint.setText(f"{joined} / {self._seats_total} joined")

        # One header plus `capacity` seats per crew, fitted into the panel. Four crews of
        # four is twenty lines, which is what sets the smallest pitch here.
        lines = len(team_ids) * (capacity + 1)
        pitch = min(0.052, 0.92 / max(1, lines))
        y = -0.14

        for team in team_ids:
            here = roster.by_team(team) if roster is not None else []
            rgb = proto.team_color(team)
            # The server's own default naming, so the panel does not say "TEAM 1" before
            # you connect and "Team A" afterwards for the same crew.
            default = (proto.DEFAULT_TEAM_NAMES[team]
                       if team < len(proto.DEFAULT_TEAM_NAMES) else f"Team {team + 1}")
            name = names.get(team) or ("Everyone" if coop else default)

            if team == mine:
                # A band behind your crew's header. Which team you are on is the one
                # thing on this panel you cannot work out by reading it — everyone's name
                # looks the same, including yours.
                self.lobby_rows.append(DirectFrame(
                    frameColor=(rgb[0], rgb[1], rgb[2], 0.22),
                    frameSize=(-0.58, 0.58, -pitch * 0.40, pitch * 0.55),
                    pos=(0, 0, y), parent=self.lobby_frame,
                ))

            label = name + ("   - YOU" if team == mine else "")
            if self.lobby_active and roster is not None and team != mine:
                # A crew you are not on is a button: click it to move. Only once the
                # lobby is live — before that there is no server to ask, and the picker
                # on the left is what sets the crew you arrive on.
                full = len(here) >= capacity
                self.lobby_rows.append(DirectButton(
                    text=label + ("   FULL" if full else "   >  JOIN"),
                    scale=min(0.042, pitch * 0.85), pos=(-0.54, 0, y),
                    text_fg=rgb + (1.0,) if not full else (0.42, 0.45, 0.52, 1.0),
                    text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), relief=None,
                    command=(None if full else self._pick_team), extraArgs=[team],
                    parent=self.lobby_frame,
                ))
            else:
                self.lobby_rows.append(DirectLabel(
                    text=label, scale=min(0.042, pitch * 0.85), pos=(-0.54, 0, y),
                    text_fg=rgb + (1.0,), text_align=TextNode.ALeft,
                    frameColor=(0, 0, 0, 0), parent=self.lobby_frame,
                ))
            self.lobby_rows.append(DirectLabel(
                text=f"{len(here)}/{capacity}", scale=min(0.038, pitch * 0.78),
                pos=(0.54, 0, y), text_fg=MUTED_COLOR, text_align=TextNode.ARight,
                frameColor=(0, 0, 0, 0), parent=self.lobby_frame,
            ))
            y -= pitch

            for slot in range(capacity):
                seat = here[slot] if slot < len(here) else None
                # Numbered, so a seat has an address you can say out loud: "team 2,
                # slot 3". Without it the panel is two lists of names and the only way
                # to describe where somebody is sitting is to count.
                self.lobby_rows.append(DirectLabel(
                    text=f"{slot + 1}.", scale=min(0.030, pitch * 0.62),
                    pos=(-0.50, 0, y), text_fg=(0.40, 0.43, 0.50, 1.0),
                    text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0),
                    parent=self.lobby_frame,
                ))

                if seat is None and team == preview and slot == 0:
                    # Not connected yet: put the player in the seat they are about to
                    # take, greyed to say it has not happened. An empty crew you are
                    # "on" still looks like an empty crew, and this is the panel whose
                    # whole job is to answer where you will be sitting.
                    self.lobby_rows.append(DirectLabel(
                        text=f"{self.player_name}  (you)",
                        scale=min(0.036, pitch * 0.74), pos=(-0.32, 0, y),
                        text_fg=(rgb[0] * 0.7 + 0.15, rgb[1] * 0.7 + 0.15,
                                 rgb[2] * 0.7 + 0.15, 0.75),
                        text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0),
                        parent=self.lobby_frame,
                    ))
                    y -= pitch
                    continue

                if seat is None:
                    # An empty seat is drawn rather than left blank, so the panel shows
                    # how much room is left instead of only who turned up.
                    self.lobby_rows.append(DirectLabel(
                        text="- empty -", scale=min(0.034, pitch * 0.70),
                        pos=(-0.32, 0, y), text_fg=(0.34, 0.37, 0.44, 1.0),
                        text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0),
                        parent=self.lobby_frame,
                    ))
                    y -= pitch
                    continue

                yours = seat.player_id == self.own_player
                self.lobby_rows.append(DirectLabel(
                    text=seat.name or "pilot", scale=min(0.036, pitch * 0.74),
                    pos=(-0.32, 0, y),
                    # Your own seat in your crew's colour, everyone else in plain white:
                    # the row you are looking for is the one about you.
                    text_fg=(rgb + (1.0,)) if yours else (0.88, 0.91, 0.96, 1.0),
                    text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0),
                    parent=self.lobby_frame,
                ))
                if roster is not None and seat.player_id == roster.host_id:
                    crown = icons.make_crown()
                    crown.reparentTo(self.lobby_frame)
                    crown.setScale(min(0.028, pitch * 0.58))
                    crown.setPos(0.44, 0, y + pitch * 0.26)
                    self.lobby_rows.append(crown)
                y -= pitch

    def _pick_team(self, team: int) -> None:
        """Clicked a crew in the lobby. The server decides whether it can be honoured."""
        if self.on_set_team is not None:
            self.on_set_team(team)

    def enter_lobby(self, roster, own_player: int, team_states=None) -> None:
        """Show the lobby for a connected client, host or not.

        Someone who joined a lobby lands here too. They get the same panel, without the
        settings column: those belong to whoever launched the server, and offering a
        joiner a row of cyclers that change nothing would be a worse lie than not showing
        them at all.
        """
        self.own_player = own_player
        self.lobby_active = True
        if self.current != "host":
            self.show("host")

        is_host = roster is not None and roster.host_id == own_player
        self.host_cfg.show() if is_host else self.host_cfg.hide()

        self._refresh_lobby(roster, team_states)

        if roster is None:
            return

        seats = len(roster.players)
        total = self._seats_total
        if is_host:
            self._set_host_button("BEGIN MATCH", GREEN)
            self.lobby_hint.setText(f"{seats} / {total} joined  -  start when ready")
        else:
            # A joiner cannot start the match, so the button says what is happening
            # rather than sitting there looking broken.
            self._set_host_button("WAITING", DISABLED)
            self.lobby_hint.setText(
                f"{seats} / {total} joined  -  waiting for the host to start"
            )

    def _set_host_button(self, text: str, color) -> None:
        """Relabel the button and refit its frame.

        DirectButton sizes its frame from the text it was built with, so setting a
        longer label leaves the background the width of the old one — "BEGIN MATCH"
        spilled out of a frame still cut for "START".
        """
        if self.host_button["text"] == text:
            return
        self.host_button["text"] = text
        self.host_button["frameColor"] = color
        self.host_button.resetFrameSize()

    def _refresh_address(self) -> None:
        """Re-read the LAN address. Done on every show, not once at build time: a laptop
        that moved between networks since launch would otherwise hand out a stale one."""
        self.address_label.setText(local_ip())

    def _copy_address(self) -> None:
        ip = local_ip()
        if clipboard.copy(ip):
            self.host_status["text_fg"] = (0.5, 1.0, 0.6, 1.0)
            self.host_status.setText(f"copied  {ip}")
        else:
            self.host_status["text_fg"] = (1.0, 0.6, 0.4, 1.0)
            self.host_status.setText(f"could not copy - the address is {ip}")

    # --- settings ----------------------------------------------------------

    def _build_settings(self) -> None:
        """The settings hub: three doors, nothing else.

        One screen with everything on it had run to four separate concerns stacked down
        the page — aim, audio, and a control reference that grew into a table. Splitting
        them means each can be as long as it needs to be, and none of them is scrolled
        past to reach another.
        """
        s = self._screen("settings")
        self._title(s, "SETTINGS")

        self._button(s, "SOUND", 0.32, lambda: self.show("settings_sound"), BLUE)
        self._button(s, "GRAPHICS", 0.12, lambda: self.show("settings_video"), BLUE,
                     width=0.32)
        self._button(s, "CONTROLS", -0.08, lambda: self.show("settings_controls"), BLUE,
                     width=0.32)
        self._button(s, "BACK", -0.40, lambda: self.show("main"), GREY, width=0.5)

        self._build_settings_sound()
        self._build_settings_video()
        self._build_settings_controls()

    def _build_settings_sound(self) -> None:
        s = self._screen("settings_sound")
        self._title(s, "SOUND")

        vols = [0.0, 0.2, 0.4, 0.6, 0.8, 1.0]
        Cycler(
            s, "Sound volume", vols, _nearest(vols, self.settings.get("sfx_volume", 0.7)),
            0.34, _pct, on_change=lambda v: self._set_volume("sfx_volume", v),
        )
        Cycler(
            s, "Music volume", vols,
            _nearest(vols, self.settings.get("music_volume", 0.5)), 0.22, _pct,
            on_change=lambda v: self._set_volume("music_volume", v),
        )
        DirectLabel(
            text="Changes are heard straight away, not on the next match.",
            scale=0.042, pos=(0, 0, 0.06), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self._button(s, "BACK", -0.30, lambda: self.show("settings"), GREY, width=0.5)

    def _build_settings_video(self) -> None:
        s = self._screen("settings_video")
        self._title(s, "GRAPHICS")

        Cycler(
            s, "Fullscreen", [0, 1], 1 if self.settings.get("fullscreen") else 0, 0.34,
            lambda v: "On" if v else "Off",
            on_change=lambda v: self._set_fullscreen(bool(v)),
        )
        Cycler(
            s, "Space dust", [0, 1], 0 if self.settings.get("no_dust") else 1, 0.22,
            lambda v: "On" if v else "Off",
            on_change=lambda v: self._set_video("no_dust", not v),
        )
        aa = [0, 2, 4, 8]
        Cycler(
            s, "Antialiasing", aa, _nearest(aa, self.settings.get("multisamples", 4)),
            0.10, lambda v: "Off" if v == 0 else f"{v}x",
            on_change=lambda v: self._set_video("multisamples", v),
        )
        Cycler(
            s, "Vertical sync", [0, 1], 1 if self.settings.get("vsync", True) else 0,
            -0.02, lambda v: "On" if v else "Off",
            on_change=lambda v: self._set_video("vsync", bool(v)),
        )

        DirectLabel(
            text=("Fullscreen and dust apply now.\n"
                  "Antialiasing and vertical sync apply the next time you launch —\n"
                  "they are chosen when the window is created."),
            scale=0.042, pos=(0, 0, -0.20), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )
        self._button(s, "BACK", -0.46, lambda: self.show("settings"), GREY, width=0.5)

    def _build_settings_controls(self) -> None:
        s = self._screen("settings_controls")
        self._title(s, "CONTROLS", y=0.86, scale=0.085)

        sens_values = [0.008, 0.012, 0.018, 0.026, 0.036, 0.05]
        try:
            idx = sens_values.index(self.settings.get("sensitivity", 0.018))
        except ValueError:
            idx = 2
        self.set_sens = Cycler(
            s, "Mouse sensitivity", sens_values, idx, 0.70,
            lambda v: f"{sens_values.index(v) + 1} of {len(sens_values)}",
            on_change=lambda v: self.settings.__setitem__("sensitivity", v),
        )
        self.set_invert = Cycler(
            s, "Invert mouse Y", [0, 1], 1 if self.settings.get("invert_y") else 0, 0.60,
            lambda v: "On" if v else "Off",
            on_change=lambda v: self.settings.__setitem__("invert_y", bool(v)),
        )

        self.bind_hint = DirectLabel(
            text="Click a key to change it.", scale=0.042, pos=(0, 0, 0.49),
            text_fg=MUTED_COLOR, frameColor=(0, 0, 0, 0), parent=s,
        )

        # Two columns: the list is thirteen rows and a single column would run off the
        # bottom of the screen.
        self.bind_buttons: dict[str, DirectButton] = {}
        rows = keybinds.ACTIONS
        half = (len(rows) + 1) // 2
        for i, (action, label, _default) in enumerate(rows):
            col, row = divmod(i, half)
            x = -0.68 + col * 0.72
            y = 0.38 - row * 0.075
            DirectLabel(
                text=label, scale=0.040, pos=(x, 0, y), text_fg=BODY_COLOR,
                text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
            )
            self.bind_buttons[action] = DirectButton(
                text="", scale=0.040, pos=(x + 0.62, 0, y), frameColor=GREY,
                text_fg=TITLE_COLOR, relief=1, pad=(0.34, 0.10),
                text_align=TextNode.ACenter,
                command=self._begin_rebind, extraArgs=[action], parent=s,
            )

        # The controls that are not up for negotiation, so the screen is still a complete
        # reference rather than only the parts that happen to be rebindable.
        DirectLabel(
            text=("Mouse aims.    Left click fires.    Right click uses the selected "
                  "item, or the tractor beam.\n"
                  "Mouse wheel selects a slot.    Esc pauses.    "
                  "Tab switches ALL / TEAM while chatting."),
            scale=0.040, pos=(0, 0, -0.44), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )

        self._button(s, "RESET TO DEFAULTS", -0.62, self._reset_binds, GREY, width=0.10)
        self._button(s, "BACK", -0.80, self._leave_controls, GREY, width=0.5)
        self._refresh_binds()

    # --- key rebinding -----------------------------------------------------

    def _set_raw_capture(self, event: str) -> None:
        """Route every keystroke to one event, or stop doing so with "".

        Rebinding has to see keys that are *already bound to something*, so it reads the
        button thrower directly rather than going through named events — otherwise
        pressing W to rebind it would also thrust.

        Tolerates there being no thrower at all, which is the case on an offscreen window
        with no input attached. That is how the menus are exercised headlessly, and a
        settings screen that could not be constructed without a real window would be a
        settings screen nothing could test.
        """
        throwers = getattr(self.base, "buttonThrowers", None)
        if throwers:
            throwers[0].node().setButtonDownEvent(event)

    def _refresh_binds(self) -> None:
        """Redraw every key button from the registry."""
        for action, button in self.bind_buttons.items():
            key = self.binds.key(action)
            button["text"] = keybinds.pretty(key) if key else "- unset -"
            button["text_fg"] = TITLE_COLOR if key else (0.85, 0.45, 0.40, 1.0)
            button.resetFrameSize()

    def _begin_rebind(self, action: str) -> None:
        """Arm a slot and wait for the next key.

        Grabs raw keystrokes the same way the chat composer does, because a rebind has to
        capture keys that are *already bound to something* — going through the normal
        event names would fire the action being rebound at the same time.
        """
        if self._rebinding is not None:
            self._cancel_rebind()

        self._rebinding = action
        self.bind_buttons[action]["text"] = "press a key"
        self.bind_buttons[action]["text_fg"] = (0.55, 0.90, 1.0, 1.0)
        self.bind_buttons[action].resetFrameSize()
        self.bind_hint.setText("Press a key, or Esc to cancel.")

        self._set_raw_capture("rebind-key")
        self.base.accept("rebind-key", self._rebind_pressed)

    def _cancel_rebind(self) -> None:
        self._rebinding = None
        self.base.ignore("rebind-key")
        self._set_raw_capture("")
        self.bind_hint.setText("Click a key to change it.")
        self._refresh_binds()

    def _rebind_pressed(self, key: str) -> None:
        action, self._rebinding = self._rebinding, None
        self.base.ignore("rebind-key")
        self._set_raw_capture("")

        if action is None or key == "escape":
            self._cancel_rebind()
            return
        if key in keybinds.RESERVED:
            self.bind_hint.setText(f"{keybinds.pretty(key)} is reserved.")
            self._refresh_binds()
            return

        # Sides of a modifier are one key as far as a binding is concerned.
        for generic in ("shift", "alt", "control"):
            if key in (f"l{generic}", f"r{generic}"):
                key = generic

        taken = self.binds.rebind(action, key)
        if taken is not None:
            self.bind_hint.setText(
                f"{keybinds.pretty(key)} taken from {keybinds.LABELS[taken]}, "
                "which is now unset."
            )
        else:
            self.bind_hint.setText("Click a key to change it.")

        self._refresh_binds()
        if self.on_rebind is not None:
            self.on_rebind()

    def _reset_binds(self) -> None:
        if self._rebinding is not None:
            self._cancel_rebind()
        self.binds.reset()
        self.bind_hint.setText("Back to the defaults.")
        self._refresh_binds()
        if self.on_rebind is not None:
            self.on_rebind()

    def _leave_controls(self) -> None:
        # A rebind left armed would eat the next keystroke on whatever screen came next.
        if self._rebinding is not None:
            self._cancel_rebind()
        self.show("settings")

    # --- graphics ----------------------------------------------------------

    def _set_video(self, key: str, value) -> None:
        self.settings[key] = value
        if self.on_settings_changed is not None:
            self.on_settings_changed()

    def _set_fullscreen(self, on: bool) -> None:
        """Applied immediately — it is the one graphics option that can be."""
        self.settings["fullscreen"] = on
        props = WindowProperties()
        props.setFullscreen(on)
        self.base.win.requestProperties(props)
        if self.on_settings_changed is not None:
            self.on_settings_changed()

    def _set_volume(self, key: str, value: float) -> None:
        self.settings[key] = value
        # Applied immediately so the change can be heard while the slider is still on
        # screen, rather than only after the next scene change.
        audio = getattr(self.base, "audio", None)
        if audio is not None:
            audio.apply_volumes()

    # --- actions -----------------------------------------------------------

    def _on_mode(self, mode: int) -> None:
        """Show only the selected mode's options, and describe what it is."""
        self.mode_blurb.setText(proto.MODE_BLURBS.get(mode, ""))
        for key, panel in self.mode_panels.items():
            panel.show() if key == mode else panel.hide()

    def _host_button(self) -> None:
        """One button, two jobs: open the lobby, then start the match from it."""
        if self.lobby_active:
            if self.on_start_match is not None:
                self.on_start_match()
            return
        self._host()

    def _leave_host(self) -> None:
        """BACK. Leaves a lobby properly rather than walking away from a live one.

        For the host that means shutting the server down; for a joiner it means
        disconnecting. Either way the screen has to be put back the way it started, since
        a joiner's visit hid the settings column.
        """
        if self.lobby_active:
            self.lobby_active = False
            self._roster = None
            self._team_states = None
            self._lobby_signature = None
            self._set_host_button("START", GREEN)
            self.host_status.setText("")
            self.host_cfg.show()
            self._refresh_lobby()

            if self.server_proc is not None:
                self.stop_server()
            elif self.on_leave_lobby is not None:
                # A joiner has no server to stop — it is the connection that has to go.
                self.on_leave_lobby()
                return
        self.show("play")

    def _host(self) -> None:
        exe = find_server_executable()
        if exe is None:
            self.host_status["text_fg"] = (1.0, 0.5, 0.45, 1.0)
            self.host_status.setText("server.exe not found next to the game")
            return

        # Refuse to host onto an occupied port.
        #
        # Starting a second server on a port that is already taken fails to bind and the
        # process dies immediately — and the old code then connected anyway, to whatever
        # was already listening. Every setting on this screen was silently discarded and
        # you played the *other* server's match, which showed up as "round length does
        # nothing, it is always three minutes". Better to say so.
        if _port_in_use(SERVER_PORT):
            self.host_status["text_fg"] = (1.0, 0.5, 0.45, 1.0)
            self.host_status.setText(
                f"a server is already running on :{SERVER_PORT} — close it, or JOIN it"
            )
            return

        self.team_name = self.team_entry.get().strip()
        self.team_pref = self.cfg_team.value

        args = [
            exe,
            "-addr", f":{SERVER_PORT}",
            "-bestof", str(self.cfg_rounds.value),
            "-round", str(self.cfg_round_len.value),
            "-intermission", str(self.cfg_intermission.value),
            "-teams", str(self.cfg_teams.value),
            "-teamsize", str(self.cfg_teamsize.value),
            "-coop", str(self.cfg_coop.value),
            "-lives", str(self.cfg_lives.value),
            "-mode", proto.MODE_FLAGS.get(self.cfg_mode.value, "salvage"),
            "-hoard-target", str(self.cfg_hoard_target.value),
            "-hoard-bonus", str(self.cfg_hoard_bonus.value),
            "-koth-target", str(self.cfg_koth_target.value),
            "-koth-rate", str(self.cfg_koth_rate.value),
            "-koth-shift", str(self.cfg_koth_shift.value),
            "-koth-credits", str(self.cfg_koth_credits.value),
            # Hold in the lobby instead of running the warmup clock down into round one,
            # so people can still be arriving while this screen is up.
            "-lobby",
        ]

        self.host_status["text_fg"] = (0.5, 1.0, 0.6, 1.0)
        self.host_status.setText("starting server...")
        try:
            # CREATE_NO_WINDOW keeps a console from flashing up in a packaged build.
            flags = 0x08000000 if os.name == "nt" else 0
            self.server_proc = subprocess.Popen(
                args, cwd=os.path.dirname(exe), creationflags=flags
            )
        except Exception as exc:  # noqa: BLE001 - shown on screen
            self.host_status["text_fg"] = (1.0, 0.5, 0.45, 1.0)
            self.host_status.setText(f"could not start server: {exc}")
            return

        # Make sure the process we just launched is the one we are about to talk to. If it
        # exited (a bad flag, a port that came into use in the last few milliseconds),
        # connecting would again mean joining a stranger's match under our own settings.
        if not self._server_came_up():
            self.host_status["text_fg"] = (1.0, 0.5, 0.45, 1.0)
            self.host_status.setText("the server started but then stopped — see bin/")
            self.server_proc = None
            return

        # The screen stays up: connecting now puts this client in the lobby rather than
        # in a match, and _await_connection leaves the menu alone until the server says
        # the match has actually begun.
        self.lobby_active = True
        self.host_status["text_fg"] = (0.5, 1.0, 0.6, 1.0)
        self.host_status.setText("lobby open - others can join now")
        self.on_connect(f"ws://localhost:{SERVER_PORT}/ws")

    def _server_came_up(self, timeout: float = 6.0) -> bool:
        """Wait for our own server process to start listening."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.server_proc.poll() is not None:
                return False  # it died
            if _port_in_use(SERVER_PORT):
                return True
            time.sleep(0.15)
        return False

    def _join(self) -> None:
        host = self.join_entry.get().strip()
        if not host:
            self.play_status.setText("type the host's address first")
            return

        # The colour and team pickers live on the host screen, but both apply just as
        # well to a game somebody else is running — the colour is claimed after
        # connecting, and the team rides in the Hello.
        self.team_pref = self.cfg_team.value

        if host.startswith("ws://") or host.startswith("wss://"):
            url = host
        else:
            if ":" not in host:
                host = f"{host}:{SERVER_PORT}"
            url = f"ws://{host}/ws"

        self.play_status["text_fg"] = (0.5, 1.0, 0.6, 1.0)
        self.play_status.setText(f"connecting to {url} ...")
        self.on_connect(url)

    # --- status / teardown -------------------------------------------------

    def set_status(self, text: str, ok: bool = False) -> None:
        target = self.host_status if self.current == "host" else self.play_status
        target["text_fg"] = (0.5, 1.0, 0.6, 1.0) if ok else (1.0, 0.5, 0.45, 1.0)
        target.setText(text)

    def destroy(self) -> None:
        if self.editor is not None:
            self.editor.destroy()
            self.editor = None
        self.backdrop.destroy()

    def stop_server(self) -> None:
        """Shut down a server this client started, so hosting never orphans one."""
        if self.server_proc is not None and self.server_proc.poll() is None:
            try:
                self.server_proc.terminate()
            except Exception:
                pass
            self.server_proc = None
