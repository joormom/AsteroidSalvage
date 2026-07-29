"""Mouse-look flight controls.

WASD moves, the mouse aims, and holding the left button grabs and pulls. That is the
whole control scheme — no arrow keys, no roll, no separate up/down thrust. If you want
to go up, point up and burn, exactly like flying.

Mouse capture uses absolute pointer reads with a manual recentre each frame rather than
Panda3D's M_relative mode, which is inconsistent across platforms and drivers. Reading
the pointer and warping it back to the middle works the same everywhere.
"""

from __future__ import annotations

from panda3d.core import WindowProperties

HELP_TEXT = (
    "W/S thrust   A/D strafe   MOUSE aim   LEFT-CLICK fire   "
    "HOLD RIGHT-CLICK tractor beam   HOLD SHIFT boost   TAB release mouse   ESC menu"
)

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

    def __init__(self, base, sensitivity: float = DEFAULT_SENSITIVITY):
        self.base = base
        self.sensitivity = sensitivity

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

        for key in ("w", "a", "s", "d", "shift", "space"):
            base.accept(key, self._set, [key, True])
            base.accept(key + "-up", self._set, [key, False])

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

        base.accept("tab", self.release_mouse)

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

    def _set(self, key: str, value: bool) -> None:
        self._down[key] = value

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
        """Current intent, as keyword arguments for NetClient.send_input."""
        return {
            "thrust_fwd": self._axis("w", "s"),
            "thrust_right": self._axis("d", "a"),
            "thrust_up": 0.0,
            "yaw": self._yaw,
            "pitch": self._pitch,
            "roll": 0.0,
            "grab": self._held("mouse3"),
            "boost": self._held("shift"),
            "brake": self._held("space"),
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
