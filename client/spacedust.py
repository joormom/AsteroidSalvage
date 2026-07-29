"""Nebula clouds and near-field dust: the two things that make the ship feel like it is
moving.

Space has a specific problem. The starfield is a shell parented to the camera, so it never
shifts; asteroids are hundreds of metres away and barely change angle; and the result is
that at 40 m/s the screen is almost completely still. Speed is only legible as *parallax*,
and parallax needs something close.

Two layers, doing different jobs:

  - **Dust** is the speed cue. A few hundred motes in a small box that follows the ship,
    wrapping around it as it flies, so there is always something streaking past the
    canopy. This is what makes the throttle feel connected to anything.
  - **Nebula** is the depth cue. A handful of enormous translucent clouds at mid distance
    that slide against the starfield as you cross the map, so the volume has somewhere in
    it rather than being uniformly black.
"""

from __future__ import annotations

import math
import os
import random
import sys

from panda3d.core import (
    ColorBlendAttrib,
    Geom,
    GeomNode,
    GeomPoints,
    GeomVertexData,
    GeomVertexFormat,
    GeomVertexWriter,
    NodePath,
    TransparencyAttrib,
    Vec3,
    Vec4,
)

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

from shipmodel import make_sphere  # noqa: E402

# --- dust -------------------------------------------------------------------

# Motes in the box. Enough that something is always in view without turning the canopy
# into static.
DUST_COUNT = 700

# Half-width of the box the dust lives in, in metres. Small on purpose: parallax falls
# off with distance, so the cue comes almost entirely from the closest motes. Much bigger
# and they stop moving relative to the ship; much smaller and they flicker through.
DUST_BOX = 110.0

DUST_COLOR = (0.62, 0.68, 0.82)

# --- nebula -----------------------------------------------------------------

NEBULA_COUNT = 7
NEBULA_MIN_RADIUS = 900.0
NEBULA_MAX_RADIUS = 1800.0

# Placed out past the play volume so they read as distant sky rather than as obstacles a
# player might try to fly around.
NEBULA_RING_MIN = 2200.0
NEBULA_RING_MAX = 3600.0

# Deep, desaturated colours. These are added light over a black sky, and anything
# saturated competes with the ships and the cargo for attention.
NEBULA_COLORS = (
    (0.26, 0.16, 0.42),  # violet
    (0.13, 0.22, 0.44),  # deep blue
    (0.32, 0.15, 0.26),  # plum
    (0.12, 0.30, 0.36),  # teal
    (0.30, 0.22, 0.14),  # dim amber
)


def make_dust(name: str = "spacedust") -> NodePath:
    """A cloud of unlit points, modelled in a box centred on the origin.

    The node is moved to follow the ship and the points are wrapped in place, so this
    geometry is built once and never rebuilt.
    """
    fmt = GeomVertexFormat.getV3c4()
    vdata = GeomVertexData(name, fmt, Geom.UHStatic)
    vdata.setNumRows(DUST_COUNT)
    vertex = GeomVertexWriter(vdata, "vertex")
    color = GeomVertexWriter(vdata, "color")

    points = GeomPoints(Geom.UHStatic)
    rng = random.Random(20260728)

    for i in range(DUST_COUNT):
        vertex.addData3(
            rng.uniform(-DUST_BOX, DUST_BOX),
            rng.uniform(-DUST_BOX, DUST_BOX),
            rng.uniform(-DUST_BOX, DUST_BOX),
        )
        # Vary the brightness so the field has depth instead of reading as a flat grid.
        b = rng.uniform(0.35, 1.0)
        color.addData4(Vec4(DUST_COLOR[0] * b, DUST_COLOR[1] * b, DUST_COLOR[2] * b, b))
        points.addVertex(i)

    geom = Geom(vdata)
    geom.addPrimitive(points)
    node = GeomNode(name)
    node.addGeom(geom)

    np = NodePath(node)
    np.setLightOff()
    np.setTransparency(TransparencyAttrib.MAlpha)
    np.setRenderModeThickness(2.0)
    np.setDepthWrite(False)
    np.setBin("fixed", 5)
    return np


def wrap_dust(dust: NodePath, ship_pos: Vec3) -> None:
    """Keep the dust box centred on the ship, snapped to a grid.

    Snapping is the whole trick. If the box simply followed the ship exactly, every mote
    would move with it and nothing would ever stream past — the field would be perfectly
    static relative to the canopy, which is the problem this exists to solve. Snapping the
    box to a lattice of its own size means the motes hold still in *world* space and only
    jump when the ship has travelled far enough that the jump is invisible.
    """
    step = DUST_BOX * 2
    dust.setPos(
        math.floor(ship_pos.x / step + 0.5) * step,
        math.floor(ship_pos.y / step + 0.5) * step,
        math.floor(ship_pos.z / step + 0.5) * step,
    )


def make_nebula(parent: NodePath, seed: int = 1) -> list[NodePath]:
    """Enormous, faint, additive clouds ringing the play volume."""
    rng = random.Random(seed)
    out: list[NodePath] = []

    for i in range(NEBULA_COUNT):
        radius = rng.uniform(NEBULA_MIN_RADIUS, NEBULA_MAX_RADIUS)
        theta = rng.uniform(0, math.tau)
        dist = rng.uniform(NEBULA_RING_MIN, NEBULA_RING_MAX)

        cloud = make_sphere(1, f"nebula{i}").copyTo(parent)
        cloud.setPos(
            math.cos(theta) * dist,
            math.sin(theta) * dist,
            rng.uniform(-1, 1) * dist * 0.35,
        )
        # Squashed and tumbled, so seven spheres do not read as seven spheres.
        cloud.setScale(radius, radius * rng.uniform(0.5, 1.0), radius * rng.uniform(0.35, 0.8))
        cloud.setHpr(rng.uniform(0, 360), rng.uniform(0, 360), rng.uniform(0, 360))

        r, g, b = NEBULA_COLORS[i % len(NEBULA_COLORS)]
        cloud.setColor(r, g, b, rng.uniform(0.10, 0.20))

        cloud.setLightOff()
        cloud.setTransparency(TransparencyAttrib.MAlpha)
        cloud.setDepthWrite(False)
        # Behind everything else, and adding light rather than blending: a translucent
        # surface over a black sky darkens what is behind it, which would turn a nebula
        # into a hole punched in the starfield.
        cloud.setBin("background", 10 + i)
        cloud.setAttrib(ColorBlendAttrib.make(
            ColorBlendAttrib.MAdd,
            ColorBlendAttrib.OIncomingAlpha,
            ColorBlendAttrib.OOne,
        ))
        cloud.setTwoSided(True)
        out.append(cloud)

    return out
