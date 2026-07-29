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


class _StubBase:
    """Just enough ShowBase for Controls: event registration and a window."""

    def __init__(self):
        self.win = _StubWin()
        self.handlers: dict[str, tuple] = {}

    def accept(self, event, fn, extra=None):
        self.handlers[event] = (fn, extra or [])

    def fire(self, event):
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

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
