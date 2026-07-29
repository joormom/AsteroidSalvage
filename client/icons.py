"""Procedurally drawn 2D icons for the upgrade shop.

Vector line art via LineSegs rather than image files — the project ships no assets, and
a frozen build cannot rely on anything outside the executable. Each icon is drawn in a
[-1, 1] box so it can be parented to a card and scaled freely.

The shapes are chosen to read at a glance without the label: a beam pulling something
in, a rocket exhaust, a widening cone, a shield.
"""

from __future__ import annotations

import math
import os
import sys

from panda3d.core import LineSegs, NodePath, Vec4

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

ICON_THICKNESS = 4.0


def _segs(color: Vec4) -> LineSegs:
    ls = LineSegs()
    ls.setThickness(ICON_THICKNESS)
    ls.setColor(color)
    return ls


def _circle(ls: LineSegs, cx: float, cy: float, r: float, steps: int = 24,
            start: float = 0.0, end: float = math.tau) -> None:
    for i in range(steps + 1):
        a = start + (end - start) * (i / steps)
        x, y = cx + math.cos(a) * r, cy + math.sin(a) * r
        if i == 0:
            ls.moveTo(x, 0, y)
        else:
            ls.drawTo(x, 0, y)


def _tractor_icon() -> NodePath:
    """A beam of widening arcs pulling a payload toward the emitter."""
    ls = _segs(Vec4(0.55, 1.0, 0.9, 1.0))
    # Emitter at the bottom.
    ls.moveTo(-0.25, 0, -0.85)
    ls.drawTo(0.25, 0, -0.85)
    ls.drawTo(0.12, 0, -0.62)
    ls.drawTo(-0.12, 0, -0.62)
    ls.drawTo(-0.25, 0, -0.85)
    # Three arcs widening upward: the beam.
    for i, (y, r) in enumerate(((-0.30, 0.30), (0.05, 0.48), (0.40, 0.66))):
        _circle(ls, 0.0, y, r, steps=16, start=math.pi * 0.15, end=math.pi * 0.85)
    # The payload being drawn in.
    _circle(ls, 0.0, 0.72, 0.18, steps=12)
    return NodePath(ls.create())


def _thrust_icon() -> NodePath:
    """A nozzle with exhaust chevrons."""
    ls = _segs(Vec4(1.0, 0.72, 0.3, 1.0))
    # Nozzle body.
    ls.moveTo(-0.30, 0, 0.75)
    ls.drawTo(0.30, 0, 0.75)
    ls.drawTo(0.42, 0, 0.05)
    ls.drawTo(-0.42, 0, 0.05)
    ls.drawTo(-0.30, 0, 0.75)
    # Exhaust: three chevrons, widening and falling away.
    for i, y in enumerate((-0.10, -0.42, -0.74)):
        w = 0.30 + i * 0.16
        ls.moveTo(-w, 0, y)
        ls.drawTo(0.0, 0, y - 0.20)
        ls.drawTo(w, 0, y)
    return NodePath(ls.create())


def _range_icon() -> NodePath:
    """A widening cone with a reach arrow — more distance."""
    ls = _segs(Vec4(0.6, 0.85, 1.0, 1.0))
    # Cone opening to the right.
    ls.moveTo(-0.70, 0, 0.0)
    ls.drawTo(0.55, 0, 0.60)
    ls.moveTo(-0.70, 0, 0.0)
    ls.drawTo(0.55, 0, -0.60)
    # Arc across the mouth.
    _circle(ls, -0.70, 0.0, 1.28, steps=18, start=-math.pi * 0.24, end=math.pi * 0.24)
    # Reach arrow down the middle.
    ls.moveTo(-0.45, 0, 0.0)
    ls.drawTo(0.42, 0, 0.0)
    ls.drawTo(0.20, 0, 0.16)
    ls.moveTo(0.42, 0, 0.0)
    ls.drawTo(0.20, 0, -0.16)
    return NodePath(ls.create())


def _hull_icon() -> NodePath:
    """A shield with a cargo box inside — protected cargo."""
    ls = _segs(Vec4(0.75, 0.9, 1.0, 1.0))
    # Shield outline.
    ls.moveTo(0.0, 0, 0.85)
    ls.drawTo(0.62, 0, 0.45)
    ls.drawTo(0.62, 0, -0.20)
    ls.drawTo(0.0, 0, -0.85)
    ls.drawTo(-0.62, 0, -0.20)
    ls.drawTo(-0.62, 0, 0.45)
    ls.drawTo(0.0, 0, 0.85)
    # Cargo box inside.
    ls.moveTo(-0.26, 0, 0.20)
    ls.drawTo(0.26, 0, 0.20)
    ls.drawTo(0.26, 0, -0.28)
    ls.drawTo(-0.26, 0, -0.28)
    ls.drawTo(-0.26, 0, 0.20)
    ls.moveTo(-0.26, 0, -0.04)
    ls.drawTo(0.26, 0, -0.04)
    return NodePath(ls.create())


def _laser_icon() -> NodePath:
    """A bolt striking a target ring — more damage per shot."""
    ls = _segs(Vec4(1.0, 0.42, 0.35, 1.0))
    # The bolt, coming in from the lower left.
    ls.moveTo(-0.80, 0, -0.55)
    ls.drawTo(0.10, 0, 0.10)
    # Doubled slightly offset so it reads as a beam rather than a line.
    ls.moveTo(-0.78, 0, -0.66)
    ls.drawTo(0.12, 0, -0.01)
    # Impact burst.
    for a in (0.35, 1.0, 1.7, 2.4):
        ls.moveTo(0.22, 0, 0.06)
        ls.drawTo(0.22 + math.cos(a) * 0.34, 0, 0.06 + math.sin(a) * 0.34)
    # Target ring it is hitting.
    _circle(ls, 0.34, 0.20, 0.46, steps=20)
    return NodePath(ls.create())


def _capacitor_icon() -> NodePath:
    """A battery cell stacked with charge bars — a bigger magazine."""
    ls = _segs(Vec4(0.55, 0.9, 1.0, 1.0))
    # Cell body.
    ls.moveTo(-0.42, 0, 0.66)
    ls.drawTo(0.42, 0, 0.66)
    ls.drawTo(0.42, 0, -0.78)
    ls.drawTo(-0.42, 0, -0.78)
    ls.drawTo(-0.42, 0, 0.66)
    # Terminal on top.
    ls.moveTo(-0.16, 0, 0.66)
    ls.drawTo(-0.16, 0, 0.84)
    ls.drawTo(0.16, 0, 0.84)
    ls.drawTo(0.16, 0, 0.66)
    # Charge bars filling it.
    for y in (0.36, 0.06, -0.24, -0.54):
        ls.moveTo(-0.26, 0, y)
        ls.drawTo(0.26, 0, y)
    return NodePath(ls.create())


def _recharger_icon() -> NodePath:
    """A lightning bolt inside a recycling arc — the charge coming back faster."""
    ls = _segs(Vec4(1.0, 0.88, 0.35, 1.0))
    # Most of a circle, so it reads as a cycle rather than a ring.
    _circle(ls, 0.0, 0.0, 0.78, steps=24, start=math.pi * 0.30, end=math.pi * 2.05)
    # Arrowhead closing the loop.
    ls.moveTo(0.44, 0, 0.62)
    ls.drawTo(0.62, 0, 0.40)
    ls.drawTo(0.74, 0, 0.66)
    # The bolt.
    ls.moveTo(0.10, 0, 0.50)
    ls.drawTo(-0.24, 0, 0.02)
    ls.drawTo(0.06, 0, 0.02)
    ls.drawTo(-0.14, 0, -0.52)
    ls.drawTo(0.24, 0, 0.08)
    ls.drawTo(-0.04, 0, 0.08)
    ls.drawTo(0.10, 0, 0.50)
    return NodePath(ls.create())


def _shields_icon() -> NodePath:
    """A station hull under a dome — the bubble, not a hand shield.

    Deliberately not the same shape as the Cargo Dampeners icon: one protects what you are
    carrying and the other protects the place you carry it to, and confusing them in a
    45-second intermission costs 900 credits.
    """
    ls = _segs(Vec4(0.55, 0.85, 1.0, 1.0))
    # The dome, an arc over the top.
    _circle(ls, 0.0, -0.15, 0.82, steps=24, start=0.0, end=math.pi)
    ls.moveTo(-0.82, 0, -0.15)
    ls.drawTo(0.82, 0, -0.15)
    # The station beneath it.
    _circle(ls, 0.0, -0.40, 0.30, steps=16)
    return NodePath(ls.create())


def _turrets_icon() -> NodePath:
    """A station with a gun on top, firing."""
    ls = _segs(Vec4(1.0, 0.62, 0.38, 1.0))
    _circle(ls, 0.0, -0.45, 0.36, steps=16)
    # Mount and barrel, angled up and to the right.
    ls.moveTo(-0.05, 0, -0.12)
    ls.drawTo(0.34, 0, 0.42)
    ls.moveTo(0.16, 0, -0.12)
    ls.drawTo(0.55, 0, 0.42)
    ls.moveTo(-0.05, 0, -0.12)
    ls.drawTo(0.16, 0, -0.12)
    # Muzzle flash.
    ls.moveTo(0.30, 0, 0.52)
    ls.drawTo(0.62, 0, 0.96)
    ls.moveTo(0.52, 0, 0.44)
    ls.drawTo(0.92, 0, 0.62)
    return NodePath(ls.create())


def _unknown_icon() -> NodePath:
    ls = _segs(Vec4(0.8, 0.8, 0.85, 1.0))
    _circle(ls, 0.0, 0.0, 0.7, steps=20)
    return NodePath(ls.create())


_BUILDERS = {
    proto.UPGRADE_TRACTOR: _tractor_icon,
    proto.UPGRADE_THRUST: _thrust_icon,
    proto.UPGRADE_RANGE: _range_icon,
    proto.UPGRADE_HULL: _hull_icon,
    proto.UPGRADE_LASER: _laser_icon,
    proto.UPGRADE_CAPACITOR: _capacitor_icon,
    proto.UPGRADE_RECHARGER: _recharger_icon,
    proto.UPGRADE_SHIELDS: _shields_icon,
    proto.UPGRADE_TURRETS: _turrets_icon,
}


def make_icon(upgrade_id: int) -> NodePath:
    """Build the icon for an upgrade. Unknown ids get a neutral placeholder rather than
    raising, so a client talking to a newer server still renders a usable shop."""
    return _BUILDERS.get(upgrade_id, _unknown_icon)()
