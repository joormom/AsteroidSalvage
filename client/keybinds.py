"""What every action is bound to, and where that is remembered.

One registry, read by both the flight controls and the game's own bindings in main.py.
Before this they each hard-coded their keys, which meant "the controls" were a list in
three places and rebinding one of them would have quietly desynchronised the others.

Only keyboard actions are rebindable. The mouse buttons are not in here on purpose: left
and right click are wired into pointer capture and into the item/beam split, and a player
who bound "fire" to the same button as "use item" would have no way back to the menu to
undo it. They are documented as fixed rather than offered and then fought with.

Bindings live next to the player profile, in the same per-user directory, and are saved
the moment one changes — a rebind that is lost on a crash is worse than no rebind.
"""

from __future__ import annotations

import json
import os

from playerprofile import profile_dir

# Action id -> (label for the settings screen, default key, order).
#
# The id is what the code asks for; the label is the only thing a player sees. Ids never
# change once shipped, since they are what a saved file is keyed on.
ACTIONS: list[tuple[str, str, str]] = [
    ("thrust_fwd", "Thrust forward", "w"),
    ("thrust_back", "Thrust back", "s"),
    ("strafe_left", "Strafe left", "a"),
    ("strafe_right", "Strafe right", "d"),
    ("boost", "Boost", "shift"),
    ("brake", "Brake", "space"),
    ("roll_mod", "Barrel roll (hold + strafe)", "alt"),
    ("pitch_mod", "Forward roll (hold + thrust)", "control"),
    ("item_1", "Select item 1", "1"),
    ("item_2", "Select item 2", "2"),
    ("item_3", "Select item 3", "3"),
    ("chat", "Chat", "enter"),
    ("release_mouse", "Release the mouse", "tab"),
]

DEFAULTS: dict[str, str] = {a: d for a, _label, d in ACTIONS}
LABELS: dict[str, str] = {a: label for a, label, _d in ACTIONS}

# Keys the game cannot give up, whatever a player asks for.
#
# Escape is the way out of every screen including this one — bound to something else, a
# player who made a mess of their controls would have no way to reach the menu and fix
# them. The mouse buttons are reserved for the reason in the module docstring.
RESERVED = {"escape", "mouse1", "mouse2", "mouse3", "wheel_up", "wheel_down"}

# Panda3D's names for keys are not what anyone calls them.
_PRETTY = {
    "space": "Space", "shift": "Shift", "control": "Ctrl", "alt": "Alt",
    "enter": "Enter", "tab": "Tab", "backspace": "Backspace", "escape": "Esc",
    "arrow_up": "Up", "arrow_down": "Down", "arrow_left": "Left",
    "arrow_right": "Right",
}


def pretty(key: str) -> str:
    """How a key is written in the settings screen."""
    if not key:
        return "-"
    if key in _PRETTY:
        return _PRETTY[key]
    if key.startswith("lshift") or key.startswith("rshift"):
        return "Shift"
    return key.upper() if len(key) == 1 else key.replace("_", " ").title()


def path() -> str:
    return os.path.join(profile_dir(), "keybinds.json")


def settings_path() -> str:
    return os.path.join(profile_dir(), "settings.json")


# What the sound and graphics screens remember between runs. Kept here rather than in the
# menu because the window has to be built from some of it before any menu exists.
SETTINGS_DEFAULTS = {
    "sensitivity": 0.018,
    "invert_y": False,
    "sfx_volume": 0.7,
    "music_volume": 0.5,
    "fullscreen": False,
    "vsync": True,
    "multisamples": 4,
    "no_dust": False,
}


def load_settings() -> dict:
    """Saved preferences, falling back to defaults for anything missing or unreadable."""
    out = dict(SETTINGS_DEFAULTS)
    try:
        with open(settings_path(), "r", encoding="utf-8") as f:
            saved = json.load(f)
    except Exception:
        return out

    for key, default in SETTINGS_DEFAULTS.items():
        value = saved.get(key, default)
        # Type-check against the default rather than trusting the file: a hand-edited
        # settings.json should not be able to put a string where a float belongs and
        # crash the window setup before anything is on screen.
        if isinstance(default, bool):
            out[key] = bool(value)
        elif isinstance(default, (int, float)) and isinstance(value, (int, float)):
            out[key] = type(default)(value)
        elif isinstance(value, type(default)):
            out[key] = value
    return out


def save_settings(settings: dict) -> bool:
    try:
        os.makedirs(profile_dir(), exist_ok=True)
        keep = {k: settings.get(k, v) for k, v in SETTINGS_DEFAULTS.items()}
        tmp = settings_path() + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(keep, f, indent=2)
        os.replace(tmp, settings_path())
        return True
    except Exception:
        return False


class Keybinds:
    """The live map, plus load/save."""

    def __init__(self, binds: dict[str, str] | None = None):
        self.binds = dict(DEFAULTS)
        if binds:
            # Only ids this build knows about, so a file from a newer version cannot
            # introduce an action nothing binds — and one from an older version simply
            # picks up the new defaults.
            for action, key in binds.items():
                if action in DEFAULTS and isinstance(key, str) and key:
                    self.binds[action] = key

    @classmethod
    def load(cls) -> "Keybinds":
        try:
            with open(path(), "r", encoding="utf-8") as f:
                return cls(json.load(f).get("binds"))
        except Exception:
            # A missing or corrupt file is not worth a crash on the way into a game.
            return cls()

    def save(self) -> bool:
        try:
            os.makedirs(profile_dir(), exist_ok=True)
            tmp = path() + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump({"binds": self.binds}, f, indent=2)
            os.replace(tmp, path())
            return True
        except Exception:
            return False

    # --- reading -----------------------------------------------------------

    def key(self, action: str) -> str:
        return self.binds.get(action, DEFAULTS.get(action, ""))

    def label(self, action: str) -> str:
        return pretty(self.key(action))

    def conflict(self, action: str, key: str) -> str | None:
        """Which other action already owns this key, if any."""
        for other, bound in self.binds.items():
            if other != action and bound == key:
                return other
        return None

    # --- writing -----------------------------------------------------------

    def rebind(self, action: str, key: str) -> str | None:
        """Assign a key, reporting what it was taken from.

        A key can only do one thing, so binding a used one **unbinds the other action**
        rather than refusing. Refusing would mean a player who wants to swap two keys has
        to find a spare one to park the first on; this way the displaced action shows as
        unset and is obviously the next thing to fix.
        """
        if action not in DEFAULTS or key in RESERVED or not key:
            return None

        taken = self.conflict(action, key)
        if taken is not None:
            self.binds[taken] = ""
        self.binds[action] = key
        self.save()
        return taken

    def reset(self) -> None:
        self.binds = dict(DEFAULTS)
        self.save()
