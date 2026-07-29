"""Map scenery: the big derelicts a match is flown around.

Built from the same primitives as everything else — subdivided icospheres, flat-shaded
slabs, saturated accent colours — because a photoreal wreck next to a cartoon spaceship
reads as two different games. Each model is built at unit radius so the renderer can
scale it to whatever the server says, exactly like the mothership.

Everything here is deliberately *broken*. A field of intact stations looks like a busy
spaceport; a field of split hulls and half a planet looks like somewhere a salvage crew
would be sent, which is the game.
"""

from __future__ import annotations

import math
import os
import sys

from panda3d.core import NodePath, Vec4

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

from shipmodel import make_sphere  # noqa: E402

# A cold, desaturated palette. Scenery has to sit *behind* the game: ships, rocks and
# team colours are the saturated things on screen, and a brightly coloured derelict would
# compete with the cargo you are supposed to be looking for.
HULL_DARK = Vec4(0.20, 0.22, 0.28, 1.0)
HULL_MID = Vec4(0.32, 0.34, 0.40, 1.0)
HULL_LIGHT = Vec4(0.46, 0.48, 0.55, 1.0)
RUST = Vec4(0.38, 0.26, 0.20, 1.0)
ROCK = Vec4(0.30, 0.27, 0.25, 1.0)
ROCK_CORE = Vec4(0.46, 0.30, 0.22, 1.0)
GLOW = Vec4(0.95, 0.62, 0.25, 1.0)  # still-burning innards
ICE = Vec4(0.55, 0.68, 0.78, 1.0)


def _blob(parent, scale, pos, color, name="part", hpr=None) -> NodePath:
    """A flat-shaded ellipsoid. The one primitive everything here is made of."""
    np = make_sphere(1, name).copyTo(parent)
    np.setScale(*scale)
    np.setPos(*pos)
    if hpr is not None:
        np.setHpr(*hpr)
    np.setColor(color)
    return np


def _slab(parent, scale, pos, color, name="slab", hpr=None) -> NodePath:
    """A squashed sphere standing in for plating. Cheaper than a real box and it keeps
    the faceted silhouette the rest of the game has."""
    return _blob(parent, scale, pos, color, name, hpr)


def make_battlestation(name: str = "battlestation") -> NodePath:
    """An armoured sphere with a trench and a dish crater — cheerfully derivative.

    Unit radius. The crater is a darker inset sphere rather than real geometry: from any
    distance you actually see one of these at, the read is "big sphere, round dish,
    equatorial line", and that is exactly what this is.
    """
    root = NodePath(name)

    _blob(root, (1.0, 1.0, 1.0), (0, 0, 0), HULL_MID, "core")

    # Equatorial trench: a flattened band, very slightly proud of the hull.
    _slab(root, (1.02, 1.02, 0.055), (0, 0, 0.02), HULL_DARK, "trench")
    _slab(root, (1.01, 1.01, 0.02), (0, 0, 0.06), HULL_LIGHT, "trenchlip")

    # The dish: a dark crater up and to one side, with a bright focus in the middle.
    dish_dir = (0.42, -0.60, 0.50)
    _blob(root, (0.40, 0.40, 0.40), dish_dir, HULL_DARK, "dish")
    _blob(root, (0.30, 0.30, 0.30),
          tuple(c * 1.04 for c in dish_dir), Vec4(0.14, 0.15, 0.19, 1.0), "dishinner")
    _blob(root, (0.07, 0.07, 0.07),
          tuple(c * 1.18 for c in dish_dir), GLOW, "dishfocus")

    # Battle damage: a bite out of the far side, so it reads as derelict rather than
    # operational. Dark blobs sunk into the hull make a convincing hole at this scale.
    for i, (dx, dy, dz, r) in enumerate((
        (-0.72, 0.55, -0.30, 0.26),
        (-0.55, 0.72, -0.10, 0.20),
        (-0.80, 0.35, 0.05, 0.16),
    )):
        _blob(root, (r, r, r), (dx, dy, dz), Vec4(0.10, 0.10, 0.13, 1.0), f"crater{i}")

    # Surface panelling, so the sphere is not a featureless ball.
    for i in range(9):
        a = math.tau * i / 9
        band = 0.55 + 0.25 * math.sin(a * 2.3)
        _slab(root, (0.30, 0.05, 0.05),
              (math.cos(a) * band, math.sin(a) * band, math.sin(a * 1.7) * 0.7),
              HULL_LIGHT, f"panel{i}", hpr=(math.degrees(a), 0, 0))

    root.setTwoSided(False)
    return root


def make_capital_wreck(name: str = "capitalwreck") -> NodePath:
    """The snapped spine of something enormous, broken into two drifting halves.

    Long on Y so it has an obvious axis: a wreck you can fly *along* is a landmark, while
    a lump is just an obstacle.
    """
    root = NodePath(name)

    # Forward section, tilted off the axis where it tore free.
    fore = root.attachNewNode("fore")
    fore.setHpr(0, 8, -6)
    _blob(fore, (0.26, 0.62, 0.20), (0, 0.42, 0), HULL_MID, "forehull")
    _blob(fore, (0.17, 0.26, 0.14), (0, 1.00, 0.02), HULL_LIGHT, "prow")
    _slab(fore, (0.52, 0.20, 0.04), (0, 0.30, -0.06), HULL_DARK, "forewing")
    # Torn edge, glowing where it is still burning out.
    _blob(fore, (0.20, 0.09, 0.16), (0, -0.16, 0), Vec4(0.12, 0.12, 0.15, 1.0), "foretear")
    _blob(fore, (0.10, 0.05, 0.08), (0, -0.19, 0), GLOW, "foreburn")

    # Aft section, drifted away and rolled over.
    aft = root.attachNewNode("aft")
    aft.setPos(0.10, -0.72, -0.08)
    aft.setHpr(0, -14, 22)
    _blob(aft, (0.24, 0.44, 0.19), (0, -0.10, 0), HULL_MID, "afthull")
    _slab(aft, (0.46, 0.16, 0.04), (0, -0.16, 0.05), HULL_DARK, "aftwing")
    # Engine bells, dead.
    for i, sx in enumerate((-0.13, 0.0, 0.13)):
        _blob(aft, (0.075, 0.10, 0.075), (sx, -0.52, 0), HULL_DARK, f"bell{i}")
        _blob(aft, (0.05, 0.04, 0.05), (sx, -0.58, 0), RUST, f"bellcore{i}")
    _blob(aft, (0.19, 0.08, 0.15), (0, 0.32, 0), Vec4(0.12, 0.12, 0.15, 1.0), "afttear")

    # Debris still hanging between the halves, which is what sells the break.
    for i in range(7):
        a = math.tau * i / 7
        _blob(root, (0.05, 0.05, 0.05),
              (math.cos(a) * 0.20, -0.28 + math.sin(a) * 0.10, math.sin(a) * 0.18),
              HULL_DARK, f"debris{i}")

    return root


def make_planet_chunk(name: str = "planetchunk") -> NodePath:
    """A shard of a world that did not survive: crust on the outside, hot core exposed.

    The exposed face is what makes it read as a *piece* of a planet rather than a lumpy
    asteroid — a brighter inner sphere showing through a gap in the crust.
    """
    root = NodePath(name)

    # Molten core, revealed on one side.
    _blob(root, (0.72, 0.72, 0.72), (0, 0, 0), ROCK_CORE, "core")
    _blob(root, (0.50, 0.50, 0.50), (0.16, -0.16, 0.06), GLOW, "magma")

    # Crust: overlapping caps covering everything except the broken face.
    for i, (dx, dy, dz, sx, sy, sz) in enumerate((
        (-0.30, 0.24, 0.10, 0.80, 0.80, 0.80),
        (-0.10, 0.42, -0.20, 0.66, 0.66, 0.66),
        (0.05, 0.18, 0.50, 0.60, 0.60, 0.60),
        (-0.36, -0.20, -0.30, 0.62, 0.62, 0.62),
        (0.30, 0.36, -0.28, 0.55, 0.55, 0.55),
    )):
        _blob(root, (sx, sy, sz), (dx, dy, dz), ROCK, f"crust{i}")

    # A frozen cap, for a bit of colour against all the brown.
    _blob(root, (0.34, 0.34, 0.30), (-0.22, 0.12, 0.62), ICE, "ice")

    # Rubble in orbit around the break.
    for i in range(9):
        a = math.tau * i / 9
        r = 0.95 + 0.2 * math.sin(a * 2.1)
        _blob(root, (0.06, 0.06, 0.06),
              (math.cos(a) * r, math.sin(a) * r, math.sin(a * 1.4) * 0.35),
              ROCK, f"rubble{i}")

    return root


def make_station_ruin(name: str = "stationruin") -> NodePath:
    """A ring station with a bite taken out of it, still turning around a broken hub."""
    root = NodePath(name)

    # Hub and spine.
    _blob(root, (0.22, 0.22, 0.26), (0, 0, 0), HULL_LIGHT, "hub")
    _blob(root, (0.09, 0.09, 0.52), (0, 0, 0.10), HULL_DARK, "spine")
    _blob(root, (0.14, 0.14, 0.12), (0, 0, 0.58), RUST, "mast")

    # The ring: segments around most of a circle, with a gap where it is broken open.
    # The gap is the whole design — a complete ring reads as intact.
    segments = 16
    missing = {4, 5, 6}
    for i in range(segments):
        if i in missing:
            continue
        a = math.tau * i / segments
        x, y = math.cos(a) * 0.86, math.sin(a) * 0.86
        colour = HULL_MID if i % 3 else HULL_LIGHT
        _slab(root, (0.20, 0.10, 0.09), (x, y, 0), colour, f"ring{i}",
              hpr=(math.degrees(a) + 90, 0, 0))

    # Spokes, two of them snapped off short.
    for i, a in enumerate((0.0, math.pi * 0.5, math.pi, math.pi * 1.5)):
        reach = 0.86 if i % 2 == 0 else 0.45
        _slab(root, (reach * 0.5, 0.035, 0.035),
              (math.cos(a) * reach * 0.5, math.sin(a) * reach * 0.5, 0),
              HULL_DARK, f"spoke{i}", hpr=(math.degrees(a), 0, 0))

    # Torn plating drifting out of the gap.
    for i in range(6):
        a = math.tau * (4.5 + i * 0.22) / segments
        r = 0.90 + i * 0.06
        _blob(root, (0.06, 0.06, 0.05),
              (math.cos(a) * r, math.sin(a) * r, math.sin(i) * 0.12),
              HULL_MID, f"shard{i}")

    # A couple of lit windows, so it reads as built rather than grown.
    for i, a in enumerate((0.9, 2.4, 4.8)):
        _blob(root, (0.035, 0.035, 0.035),
              (math.cos(a) * 0.86, math.sin(a) * 0.86, 0.07), GLOW, f"light{i}")

    return root


# Indexed by the PropKind the server sends in the snapshot's tier byte.
PROP_BUILDERS = (
    make_battlestation,
    make_capital_wreck,
    make_planet_chunk,
    make_station_ruin,
)


def make_prop(kind: int) -> NodePath:
    """Build the model for a prop kind, falling back to a plain rock for anything a
    newer server invents."""
    if 0 <= kind < len(PROP_BUILDERS):
        return PROP_BUILDERS[kind]()
    np = make_sphere(1, "unknownprop")
    np.setColor(ROCK)
    return np
