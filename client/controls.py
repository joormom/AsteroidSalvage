"""Mouse-look flight controls.

WASD moves, the mouse aims, and holding the left button grabs and pulls. That is the
whole control scheme — no arrow keys, no roll, no separate up/down thrust. If you want
to go up, point up and burn, exactly like flying.

Mouse capture uses absolute pointer reads with a manual recentre each frame rather than
Panda3D's M_relative mode, which is inconsistent across platforms and drivers. Reading
the pointer and warping it back to the middle works the same everywhere.
"""

from __future__ import annotations

from panda3d.core import ModifierButtons, WindowProperties

import keybinds

# Turn command per pixel of mouse movement, before clamping.
#
# Deliberately low. The command becomes torque on the server, so it integrates into
# rotation rather than setting an angle directly — the same number that feels fine for
# an FPS crosshair sends a spaceship spinning. This is tuned so a normal mouse sweep is
# a firm turn, not a barrel roll.
DEFAULT_SENSITIVITY = 0.018

# Lerp factor toward the raw mouse delta each frame. Higher is snappier. Mouse deltas
# are noisy frame to frame so some smoothing is needed, but too much reads as the ship
# ignoring you — the aim command already becomes torque server-side, which adds its own
# lag on top.
SMOOTHING = 0.62


class Controls:
    """Tracks keys and mouse look, and produces an input frame."""

    # The flight actions this class owns. The rest (items, chat) are bound in main.py,
    # from the same registry.
    FLIGHT_ACTIONS = ("thrust_fwd", "thrust_back", "strafe_left", "strafe_right",
                      "boost", "brake", "roll_mod", "pitch_mod")

    def __init__(self, base, sensitivity: float = DEFAULT_SENSITIVITY, binds=None):
        self.base = base
        self.sensitivity = sensitivity
        # Where the keys come from. Defaults when nothing is passed, which is what the
        # headless tests and any caller that predates rebinding get.
        self.binds = binds if binds is not None else keybinds.Keybinds()
        self._bound_events: list[str] = []

        self._down: dict[str, bool] = {}
        self._yaw = 0.0
        self._pitch = 0.0
        self.captured = False
        self.invert_y = False

        # Flight controls are inert until the game actually starts.
        #
        # Without this, clicking a menu button re-captured the mouse and hid the cursor,
        # because the same mouse1 binding serves both "fire the tractor beam" and
        # "take the pointer back after Tab". Clicking PLAY made every later menu
        # unusable.
        self.active = False

        # typing means the chat composer owns the keyboard. Flight bindings still fire —
        # Panda3D delivers a keystroke to every handler that asked for it — so they have
        # to be ignored here rather than unbound, or typing "was" would thrust, strafe
        # and brake on the way past.
        self.typing = False

        # Stop Panda3D folding modifiers into event names.
        #
        # By default a ButtonThrower prefixes held modifiers, so holding Control and
        # pressing W throws "control-w" rather than "w" — and an accept("w") never fires.
        # That breaks every modified binding at once: Alt to roll, Control to pitch, and
        # Shift-to-boost while already thrusting. Clearing the set makes every key throw
        # its own name whatever else is held, and the modifiers still arrive as their own
        # buttons, which is exactly how they are used here.
        base.buttonThrowers[0].node().setModifierButtons(ModifierButtons())

        self.apply_bindings()

        # Mouse buttons need to both register AND re-capture the pointer, so they go
        # through one combined handler.
        #
        # accept() REPLACES any existing handler for an event rather than adding to it.
        # Registering "mouse3" twice — once to track the button, once to re-capture —
        # silently threw away the tracking, so right-click re-captured the mouse and the
        # tractor beam never fired at all.
        for key in ("mouse1", "mouse3"):
            base.accept(key, self._on_mouse_down, [key])
            base.accept(key + "-up", self._set, [key, False])

    # --- mouse capture -----------------------------------------------------

    def capture_mouse(self) -> None:
        if not self.active:
            return
        props = WindowProperties()
        props.setCursorHidden(True)
        self.base.win.requestProperties(props)
        self.captured = True
        self._center_pointer()

    def release_mouse(self) -> None:
        # TAB reaches here as well as reaching the chat composer, where it switches
        # channel. While composing it must only do the latter.
        if self.typing:
            return
        props = WindowProperties()
        props.setCursorHidden(False)
        self.base.win.requestProperties(props)
        self.captured = False

    def _on_mouse_down(self, key: str) -> None:
        self._set(key, True)
        # Only reclaim the pointer while flying. On a menu or in the shop the cursor is
        # the interface.
        if self.active and not self.captured:
            self.capture_mouse()

    def _center(self) -> tuple[int, int]:
        return self.base.win.getXSize() // 2, self.base.win.getYSize() // 2

    def _center_pointer(self) -> None:
        cx, cy = self._center()
        self.base.win.movePointer(0, cx, cy)

    # --- keys --------------------------------------------------------------

    # --- bindings ----------------------------------------------------------

    @staticmethod
    def events_for(key: str) -> list[str]:
        """The Panda3D events a bound key can arrive as.

        Modifiers come in pairs. Panda3D throws the side that was actually pressed —
        "lalt", "rcontrol" — as well as a generic name for some of them, and which you get
        is not worth depending on. Listening for all of them and folding them onto one
        action is what makes "Alt" mean either Alt.
        """
        sides = {
            "shift": ["shift", "lshift", "rshift"],
            "alt": ["alt", "lalt", "ralt"],
            "control": ["control", "lcontrol", "rcontrol"],
        }
        return sides.get(key, [key])

    def apply_bindings(self) -> None:
        """(Re)register every flight key. Safe to call again after a rebind."""
        for event in self._bound_events:
            self.base.ignore(event)
        self._bound_events = []
        # Anything held under the old binding would otherwise stay held forever: its
        # key-up is about to stop being listened for.
        self._down.clear()

        for action in self.FLIGHT_ACTIONS:
            key = self.binds.key(action)
            if not key:
                continue  # deliberately unbound
            for event in self.events_for(key):
                self.base.accept(event, self._set, [action, True])
                self.base.accept(event + "-up", self._set, [action, False])
                self._bound_events += [event, event + "-up"]

        # Releasing the pointer is a press, not a state, so it binds to the action
        # directly rather than going through the held-key table.
        release = self.binds.key("release_mouse")
        if release:
            for event in self.events_for(release):
                self.base.accept(event, self.release_mouse)
                self._bound_events.append(event)

    def _set(self, key: str, value: bool) -> None:
        if self.typing and value:
            # Releases still register: a key held when the composer opened must not be
            # left stuck down for the rest of the match.
            return
        self._down[key] = value

    def set_typing(self, typing: bool) -> None:
        """Hand the keyboard to the chat composer, or take it back."""
        if typing:
            # Released before the flag goes up, since release_mouse refuses to act once
            # it is set. Whatever was held when Enter was pressed would otherwise stay
            # held for as long as the message takes to write.
            self.release_mouse()
            self._down.clear()
        self.typing = typing

    def _held(self, key: str) -> bool:
        return self._down.get(key, False)

    def _axis(self, positive: str, negative: str) -> float:
        return (1.0 if self._held(positive) else 0.0) - (
            1.0 if self._held(negative) else 0.0
        )

    # --- per-frame ---------------------------------------------------------

    def update_mouse(self) -> None:
        """Read mouse movement and recentre. Call once per frame before sample()."""
        if not self.captured:
            self._yaw *= 1.0 - SMOOTHING
            self._pitch *= 1.0 - SMOOTHING
            return

        pointer = self.base.win.getPointer(0)
        if not pointer.getInWindow():
            return

        cx, cy = self._center()
        dx = pointer.getX() - cx
        dy = pointer.getY() - cy

        # movePointer returns False if the window is not focused; skip this frame's
        # delta rather than accumulating a huge jump when focus returns.
        if not self.base.win.movePointer(0, cx, cy):
            return

        def clamp(v: float) -> float:
            return -1.0 if v < -1.0 else (1.0 if v > 1.0 else v)

        # Mouse right -> yaw right. The server's yaw torque is applied about the ship's
        # up axis, where positive turns left, hence the negation.
        target_yaw = clamp(-dx * self.sensitivity)
        target_pitch = clamp(-dy * self.sensitivity)
        if self.invert_y:
            target_pitch = -target_pitch

        self._yaw += (target_yaw - self._yaw) * SMOOTHING
        self._pitch += (target_pitch - self._pitch) * SMOOTHING

    def sample(self) -> dict:
        """Current intent, as keyword arguments for NetClient.send_input.

        Alt and Control re-purpose the movement keys rather than adding new ones:

          - **Alt** turns A/D into a barrel roll about the nose.
          - **Control** turns W/S into a forward/backward roll — a somersault.

        Repurposing rather than binding fresh keys keeps the hand where it already is.
        You cannot strafe while rolling or thrust while flipping, which is the honest
        trade: a ship doing an aerobatic manoeuvre is committing to it.
        """
        rolling = self._held("roll_mod")
        flipping = self._held("pitch_mod")

        strafe = self._axis("strafe_right", "strafe_left")
        forward = self._axis("thrust_fwd", "thrust_back")

        # Both torques are negated for the same reason yaw is: they are applied about the
        # ship's own axes, where a positive value turns the other way to the intuition.
        # D rolls right; W tips the nose down into a forward roll.
        pitch = self._pitch
        if flipping:
            pitch = max(-1.0, min(1.0, pitch - forward))

        return {
            "thrust_fwd": 0.0 if flipping else forward,
            "thrust_right": 0.0 if rolling else strafe,
            "thrust_up": 0.0,
            "yaw": self._yaw,
            "pitch": pitch,
            "roll": -strafe if rolling else 0.0,
            "grab": self._held("mouse3"),
            "boost": self._held("boost"),
            "brake": self._held("brake"),
            "fire": self.firing,
        }

    @property
    def firing(self) -> bool:
        """Left click: the laser.

        Held rather than tapped. The server enforces both a shot cooldown and the energy
        bar, so holding the button empties the charge at the weapon's own rate — there is
        nothing to gain from click-spamming, and nothing lost by not being able to.
        """
        return self.active and self.captured and self._held("mouse1")
