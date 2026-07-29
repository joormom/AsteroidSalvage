"""Checks that flight controls never steal the cursor outside the game.

    python client/controls_test.py

Uses a stub in place of ShowBase so it runs headlessly in a second. The bug this guards
against was subtle and entirely invisible from the code: Controls binds mouse1 both to
"fire the tractor beam" and to "take the pointer back after Tab", so clicking a menu
button hid the cursor and made every subsequent menu unusable.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))

FAILURES: list[str] = []


def check(cond: bool, label: str, detail: str = "") -> None:
    if cond:
        print(f"  PASS  {label}")
    else:
        FAILURES.append(label)
        print(f"  FAIL  {label}" + (f"  ({detail})" if detail else ""))


class _StubWin:
    def __init__(self):
        self.cursor_hidden = None
        self.size = (800, 600)

    def requestProperties(self, props):
        self.cursor_hidden = props.getCursorHidden()

    def getXSize(self):
        return self.size[0]

    def getYSize(self):
        return self.size[1]

    def movePointer(self, *_a):
        return True

    def getPointer(self, _n):
        class P:
            def getInWindow(self):
                return True

            def getX(self):
                return 400

            def getY(self):
                return 300

        return P()


class _StubThrowerNode:
    """Records the modifier set Controls installs.

    Controls clears it so that holding Alt or Control does not rename every other key's
    event — "control-w" instead of "w" — which is what would silently break the roll
    bindings and Shift-to-boost-while-thrusting.
    """

    def __init__(self):
        self.modifiers = "default"

    def setModifierButtons(self, mods):
        self.modifiers = mods


class _StubThrower:
    def __init__(self):
        self._node = _StubThrowerNode()

    def node(self):
        return self._node


class _StubBase:
    """Just enough ShowBase for Controls: event registration and a window."""

    def __init__(self):
        self.win = _StubWin()
        self.handlers: dict[str, tuple] = {}
        self.buttonThrowers = [_StubThrower()]

    def accept(self, event, fn, extra=None):
        self.handlers[event] = (fn, extra or [])

    def ignore(self, event):
        self.handlers.pop(event, None)

    def fire(self, event):
        if event not in self.handlers:
            raise AssertionError(f"nothing is bound to {event!r}")
        fn, extra = self.handlers[event]
        fn(*extra)


def main() -> int:
    from controls import Controls

    base = _StubBase()
    c = Controls(base)

    print("menus (controls inactive):")
    check(not c.active, "controls start inactive")

    base.fire("mouse1")
    check(not c.captured, "clicking a menu button does not capture the mouse")
    check(base.win.cursor_hidden is not True, "cursor stays visible on a menu click")

    base.fire("mouse3")
    check(not c.captured, "right-clicking a menu does not capture the mouse")

    c.capture_mouse()
    check(not c.captured, "capture_mouse is a no-op while inactive")

    print("\nin game (controls active):")
    c.active = True
    c.capture_mouse()
    check(c.captured, "capture works once the game starts")
    check(base.win.cursor_hidden is True, "cursor is hidden while flying")

    c.release_mouse()
    check(not c.captured, "release frees the mouse")
    check(base.win.cursor_hidden is False, "cursor reappears when released")

    # The shop releases the mouse mid-game; a click there must not silently reclaim it
    # while the player is trying to click a card... but a click in open flight should.
    base.fire("mouse1")
    check(c.captured, "clicking during flight reclaims the pointer after Tab")

    print("\nrolls (Alt and Control repurpose the movement keys):")
    check(base.buttonThrowers[0].node().modifiers != "default",
          "modifier folding is disabled, so Alt+A still throws 'a'")

    base.fire("d")
    plain = c.sample()
    check(plain["thrust_right"] == 1.0 and plain["roll"] == 0.0,
          "D alone strafes and does not roll",
          f"right={plain['thrust_right']} roll={plain['roll']}")

    base.fire("lalt")
    rolled = c.sample()
    check(rolled["roll"] == -1.0, "Alt+D rolls right", f"roll={rolled['roll']}")
    check(rolled["thrust_right"] == 0.0, "Alt+D stops strafing",
          f"right={rolled['thrust_right']}")

    base.fire("lalt-up")
    base.fire("d-up")

    base.fire("w")
    thrusting = c.sample()
    check(thrusting["thrust_fwd"] == 1.0, "W alone thrusts")

    base.fire("lcontrol")
    flipping = c.sample()
    # Negative pitch is nose-down, matching an un-inverted mouse pushed forward.
    check(flipping["pitch"] == -1.0, "Ctrl+W tips into a forward roll",
          f"pitch={flipping['pitch']}")
    check(flipping["thrust_fwd"] == 0.0, "Ctrl+W stops thrusting",
          f"fwd={flipping['thrust_fwd']}")

    base.fire("w-up")
    base.fire("s")
    back = c.sample()
    check(back["pitch"] == 1.0, "Ctrl+S is a backward roll", f"pitch={back['pitch']}")

    base.fire("lcontrol-up")
    base.fire("s-up")
    rest = c.sample()
    check(rest["roll"] == 0.0 and rest["pitch"] == 0.0,
          "releasing the modifiers leaves the ship level")

    print("\nrebinding:")
    import tempfile

    import keybinds

    # Rebinding saves as it goes, and the real path is the player's own keybinds.json.
    # Point it somewhere disposable first: a test that quietly rewrote your controls
    # would be a worse bug than anything it could catch.
    tmpdir = tempfile.mkdtemp(prefix="keybinds-test-")
    keybinds.path = lambda: os.path.join(tmpdir, "keybinds.json")

    binds = keybinds.Keybinds()
    b2 = _StubBase()
    c2 = Controls(b2, binds=binds)
    c2.active = True

    b2.fire("w")
    check(c2.sample()["thrust_fwd"] == 1.0, "the default key thrusts")
    b2.fire("w-up")

    binds.binds["thrust_fwd"] = "i"
    c2.apply_bindings()

    check("w" not in b2.handlers, "the old key stops being listened for")
    b2.fire("i")
    check(c2.sample()["thrust_fwd"] == 1.0, "the new key thrusts")
    b2.fire("i-up")

    # Rebinding onto a key another action owns takes it, leaving that one unset rather
    # than silently giving one key two jobs.
    taken = binds.rebind("strafe_left", binds.key("brake"))
    check(taken == "brake", "rebinding onto a used key reports what it displaced",
          f"got {taken}")
    check(binds.key("brake") == "", "the displaced action is left unset",
          f"brake={binds.key('brake')!r}")

    # A key held when the bindings change must not be left stuck down: its key-up is
    # about to stop being listened for.
    b2.fire("i")
    binds.binds["thrust_fwd"] = "o"
    c2.apply_bindings()
    check(c2.sample()["thrust_fwd"] == 0.0, "a key held across a rebind is not stuck on",
          f"fwd={c2.sample()['thrust_fwd']}")

    check(binds.rebind("boost", "escape") is None and binds.key("boost") == "shift",
          "reserved keys are refused")

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
