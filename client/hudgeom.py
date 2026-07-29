"""In-world beam geometry: the tractor beam, laser bolts and debris shards.

These are diegetic HUD — drawn in the 3D world rather than on a 2D overlay, so they sit
in the same space as the thing they refer to.

There used to be a navigation arrow here pointing home. It is gone deliberately: with
every team owning a mothership and the map twice the size, reading the belt and picking
out your own colours is the navigation, and an arrow did that work for you.
"""

from __future__ import annotations

from panda3d.core import (
    Geom,
    GeomNode,
    GeomTriangles,
    GeomVertexData,
    GeomVertexFormat,
    GeomVertexWriter,
    NodePath,
    TransparencyAttrib,
    Vec3,
    Vec4,
)

BEAM_CORE = Vec4(0.55, 1.00, 0.85, 0.85)
BEAM_EDGE = Vec4(0.20, 0.85, 1.00, 0.25)


def _mesh(name: str, tris: list[tuple[Vec3, Vec3, Vec3, Vec4]]) -> NodePath:
    """Build an unlit, vertex-coloured triangle mesh."""
    fmt = GeomVertexFormat.getV3c4()
    vdata = GeomVertexData(name, fmt, Geom.UHStatic)
    vdata.setNumRows(len(tris) * 3)

    vertex = GeomVertexWriter(vdata, "vertex")
    color = GeomVertexWriter(vdata, "color")
    prim = GeomTriangles(Geom.UHStatic)

    i = 0
    for a, b, c, col in tris:
        for v in (a, b, c):
            vertex.addData3(v)
            color.addData4(col)
        prim.addVertices(i, i + 1, i + 2)
        i += 3

    geom = Geom(vdata)
    geom.addPrimitive(prim)
    node = GeomNode(name)
    node.addGeom(geom)

    np = NodePath(node)
    np.setLightOff()
    return np


def make_tractor_beam(name: str = "tractor", style: dict | None = None) -> NodePath:
    """A beam modelled from the origin along +Y with unit length.

    The renderer stretches it with setScale(1, distance, 1) and aims it with lookAt, so
    one mesh serves every ship-to-cargo pair. Two crossed quads rather than a cylinder:
    from any viewing angle at least one face is broadside, which is what makes a beam
    read as a solid shaft of light instead of a flat ribbon.
    """
    style = style or {}
    core = Vec4(*style.get("core", (0.55, 1.00, 0.85)), 0.85)
    edge = Vec4(*style.get("edge", (0.20, 0.85, 1.00)), 0.25)

    tris: list[tuple[Vec3, Vec3, Vec3, Vec4]] = []
    w = 0.5

    for axis in ("h", "v"):
        if axis == "h":
            near_a, near_b = Vec3(-w, 0, 0), Vec3(w, 0, 0)
            far_a, far_b = Vec3(-w, 1, 0), Vec3(w, 1, 0)
        else:
            near_a, near_b = Vec3(0, 0, -w), Vec3(0, 0, w)
            far_a, far_b = Vec3(0, 1, -w), Vec3(0, 1, w)

        # Bright at the ship, fading toward the cargo, so the direction of pull reads.
        tris.append((near_a, near_b, far_b, core))
        tris.append((near_a, far_b, far_a, edge))
        tris.append((far_b, near_b, near_a, core))
        tris.append((far_a, far_b, near_a, edge))

    np = _mesh(name, tris)
    np.setTransparency(TransparencyAttrib.MAlpha)
    np.setDepthWrite(False)
    np.setTwoSided(True)
    np.setBin("transparent", 30)
    return np


def make_laser_bolt(name: str = "laser", rgb=(1.0, 0.25, 0.22)) -> NodePath:
    """A thin bolt modelled from the origin along +Y with unit length.

    Same construction as the tractor beam — crossed quads, stretched with setScale and
    aimed with lookAt — but narrow and hot-cored rather than wide and soft, so the two
    never read as the same effect. The colour is the shooter's team, which is how you
    tell incoming fire from your own crew's.
    """
    core = Vec4(min(1.0, rgb[0] * 1.6 + 0.4), min(1.0, rgb[1] * 1.6 + 0.4),
                min(1.0, rgb[2] * 1.6 + 0.4), 1.0)
    edge = Vec4(rgb[0], rgb[1], rgb[2], 0.30)

    tris: list[tuple[Vec3, Vec3, Vec3, Vec4]] = []
    w = 0.5

    for axis in ("h", "v"):
        if axis == "h":
            near_a, near_b = Vec3(-w, 0, 0), Vec3(w, 0, 0)
            far_a, far_b = Vec3(-w, 1, 0), Vec3(w, 1, 0)
        else:
            near_a, near_b = Vec3(0, 0, -w), Vec3(0, 0, w)
            far_a, far_b = Vec3(0, 1, -w), Vec3(0, 1, w)

        # Uniformly hot along its length: a laser is not a spray, and fading it out made
        # long shots look like they stopped short of what they hit.
        tris.append((near_a, near_b, far_b, core))
        tris.append((near_a, far_b, far_a, core))
        tris.append((far_b, near_b, near_a, edge))
        tris.append((far_a, far_b, near_a, edge))

    np = _mesh(name, tris)
    np.setTransparency(TransparencyAttrib.MAlpha)
    np.setDepthWrite(False)
    np.setTwoSided(True)
    np.setBin("transparent", 31)
    return np


def make_shard(name: str = "shard", rgb=(1.0, 0.72, 0.30)) -> NodePath:
    """One tetrahedral chunk of debris, used by the explosion effect.

    Angular rather than spherical: a sphere flying outward reads as a ball, while a
    tumbling shard reads as something that used to be part of a larger object.
    """
    bright = Vec4(min(1.0, rgb[0] * 1.3), min(1.0, rgb[1] * 1.3), min(1.0, rgb[2] * 1.3), 1.0)
    dim = Vec4(rgb[0] * 0.45, rgb[1] * 0.45, rgb[2] * 0.45, 1.0)

    a = Vec3(0, 0.9, 0)
    b = Vec3(-0.5, -0.4, -0.3)
    c = Vec3(0.5, -0.4, -0.3)
    d = Vec3(0, -0.3, 0.6)

    tris = [
        (a, b, c, bright),
        (a, c, d, bright),
        (a, d, b, dim),
        (b, d, c, dim),
    ]
    np = _mesh(name, tris)
    np.setTwoSided(True)
    return np
