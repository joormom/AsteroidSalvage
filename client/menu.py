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
from panda3d.core import TextNode

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
import clipboard  # noqa: E402

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

    def __init__(self, base, on_connect, settings, profile=None, on_apply_loadout=None):
        self.base = base
        self.on_connect = on_connect
        self.settings = settings  # dict shared with the game for sensitivity etc.
        self.profile = profile
        self.on_apply_loadout = on_apply_loadout
        self.server_proc: subprocess.Popen | None = None
        self.team_name = ""
        # Palette entry the player picked; None until a screen sets one.
        self.team_color = None
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
        self._title(s, "HOST A GAME", y=0.78, scale=0.085)

        # Seven rows at 0.10 spacing, ending at 0.00 — the team-name field sits at -0.14
        # and adding an eighth row at the old 0.12 pitch would have run straight into it.
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
            s, "Rounds (best of)", [1, 3, 5, 7], 2, 0.54, lambda v: f"{v}"
        )
        self.cfg_round_len = Cycler(
            s, "Round length", [60, 90, 120, 180, 240, 300], 3, 0.44,
            lambda v: f"{v // 60}:{v % 60:02d}",
        )
        self.cfg_intermission = Cycler(
            s, "Shop time", [20, 30, 45, 60], 2, 0.34, lambda v: f"{v}s"
        )
        # Lives per team per round. The server clamps to 6-20 (sim.MinLives/MaxLives);
        # the presets step by two so the whole range fits without a long cycle.
        self.cfg_lives = Cycler(
            s, "Lives per team", [6, 8, 10, 12, 14, 16, 18, 20], 3, 0.24,
            lambda v: f"{v}",
        )
        self.cfg_teams = Cycler(
            s, "Teams", [1, 2, 3, 4], 3, 0.14, lambda v: f"{v}"
        )
        self.cfg_teamsize = Cycler(
            s, "Players per team", [1, 2, 3, 4, 5, 6], 3, 0.04, lambda v: f"{v}"
        )
        self.cfg_coop = Cycler(
            s, "Teams play", [0, 1], 0, -0.06, lambda v: "Co-op" if v else "Versus"
        )
        # Your crew's colour, out of the full sixteen. Applied after connecting, since it
        # is a property of the team on the server rather than a launch flag — which also
        # means it works when you JOIN someone else's game, not only when you host.
        self.cfg_color = Cycler(
            s, "Team colour", list(range(proto.TEAM_COLOR_COUNT)), 0, -0.16,
            lambda v: proto.TEAM_COLOR_NAMES.get(v, "?"),
        )

        # --- per-mode options ---
        #
        # One frame per mode, all built up front and only one ever visible. Building them
        # lazily would mean a mode's settings did not exist until it had been selected
        # once, which is exactly when _host would go looking for them.
        self.mode_panels: dict[int, DirectFrame] = {}

        salvage = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        DirectLabel(
            text="No extra settings - the classic game.",
            scale=0.040, pos=(-0.62, 0, -0.30), text_fg=MUTED_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=salvage,
        )
        self.mode_panels[proto.MODE_SALVAGE] = salvage

        hoard = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        self.cfg_hoard_target = Cycler(
            hoard, "Points to win a round", [0, 2000, 3000, 4000, 6000, 8000], 3, -0.30,
            lambda v: "no target" if v == 0 else f"{v:,}",
        )
        self.cfg_hoard_bonus = Cycler(
            hoard, "Rock value", [1.0, 1.3, 1.6, 2.0, 3.0], 2, -0.40,
            lambda v: f"x{v:g}",
        )
        self.mode_panels[proto.MODE_HOARD] = hoard

        koth = DirectFrame(frameColor=(0, 0, 0, 0), parent=s)
        self.cfg_koth_target = Cycler(
            koth, "Points to win a round", [200, 350, 500, 750, 1000], 2, -0.30,
            lambda v: f"{v:,}",
        )
        self.cfg_koth_rate = Cycler(
            koth, "Points per second", [3, 5, 7, 10, 15], 2, -0.40, lambda v: f"{v}/s",
        )
        self.cfg_koth_shift = Cycler(
            koth, "Hill moves every", [0, 20, 30, 45, 60, 90], 3,
            -0.50, lambda v: "never" if v == 0 else f"{v}s",
        )
        self.cfg_koth_credits = Cycler(
            koth, "Credits per second", [0, 10, 20, 35, 50], 2, -0.60,
            lambda v: "none" if v == 0 else f"{v} cr/s",
        )
        self.mode_panels[proto.MODE_KOTH] = koth

        self._on_mode(self.cfg_mode.value)

        DirectLabel(
            text="Your team name",
            scale=0.052, pos=(-0.62, 0, -0.14), text_fg=BODY_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
        )
        self.team_entry = DirectEntry(
            initialText="", scale=0.06, pos=(0.10, 0, -0.15), width=12, numLines=1,
            focus=0, frameColor=(0.10, 0.12, 0.18, 1.0), text_fg=(1, 1, 1, 1),
            text_align=TextNode.ALeft, parent=s,
        )

        # Address + copy button, so the host can paste it straight into chat.
        ip = local_ip()
        DirectLabel(
            text="Others join at",
            scale=0.05, pos=(-0.62, 0, -0.34), text_fg=BODY_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
        )
        DirectLabel(
            text=ip,
            scale=0.062, pos=(0.06, 0, -0.34), text_fg=TITLE_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=s,
        )
        DirectButton(
            text="COPY", scale=0.05, pos=(0.78, 0, -0.335), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.28, 0.12),
            command=self._copy_address, parent=s,
        )

        self.host_status = DirectLabel(
            text="", scale=0.048, pos=(0, 0, -0.48), text_fg=(0.5, 1.0, 0.6, 1.0),
            frameColor=(0, 0, 0, 0), parent=s,
        )

        self._button(s, "START", -0.64, self._host, GREEN)
        self._button(s, "BACK", -0.82, lambda: self.show("play"), GREY, width=0.5)

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
        s = self._screen("settings")
        self._title(s, "SETTINGS")

        sens_values = [0.008, 0.012, 0.018, 0.026, 0.036, 0.05]
        try:
            idx = sens_values.index(self.settings.get("sensitivity", 0.018))
        except ValueError:
            idx = 2
        self.set_sens = Cycler(
            s, "Mouse sensitivity", sens_values, idx, 0.42,
            lambda v: f"{sens_values.index(v) + 1} of {len(sens_values)}",
            on_change=lambda v: self.settings.__setitem__("sensitivity", v),
        )
        self.set_invert = Cycler(
            s, "Invert mouse Y", [0, 1], 1 if self.settings.get("invert_y") else 0, 0.28,
            lambda v: "On" if v else "Off",
            on_change=lambda v: self.settings.__setitem__("invert_y", bool(v)),
        )

        vols = [0.0, 0.2, 0.4, 0.6, 0.8, 1.0]
        Cycler(
            s, "Sound volume", vols, _nearest(vols, self.settings.get("sfx_volume", 0.7)),
            0.14, _pct, on_change=lambda v: self._set_volume("sfx_volume", v),
        )
        Cycler(
            s, "Music volume", vols,
            _nearest(vols, self.settings.get("music_volume", 0.5)), 0.00, _pct,
            on_change=lambda v: self._set_volume("music_volume", v),
        )

        DirectLabel(
            text=(
                "W/S thrust    A/D strafe    MOUSE aim\n"
                "LEFT-CLICK  laser        HOLD RIGHT-CLICK  tractor beam\n"
                "HOLD SHIFT boost    SPACE brake    TAB release mouse"
            ),
            scale=0.048, pos=(0, 0, -0.22), text_fg=MUTED_COLOR,
            frameColor=(0, 0, 0, 0), parent=s,
        )

        self._button(s, "BACK", -0.60, lambda: self.show("main"), GREY, width=0.5)

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
        self.team_color = self.cfg_color.value

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

        # The colour picker lives on the host screen, but the choice is claimed after
        # connecting, so it applies just as well to a game somebody else is running.
        self.team_color = self.cfg_color.value

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
