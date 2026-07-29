"""Cartoon spaceship, built from primitives — with a pilot in the cockpit.

Deliberately stylised rather than realistic: fat rounded forms, exaggerated proportions
(stubby fuselage, oversized engines, a big bubble canopy), and bright saturated colours.
Cartoon shapes read as *rounded*, so this is assembled from scaled spheres rather than
the flat-shaded faceted mesh it replaced — a low-poly wedge reads as "military jet", not
"cartoon".

Ship-local axes match the rest of the game: +Y forward, +X right, +Z up. Everything is
modelled at roughly unit scale and scaled by the ship's radius at spawn.

The pilot matters for more than charm: the chase camera looks up the ship's back, so the
helmet sitting in a glass bubble gives an instant read on which way the ship is facing,
and makes the thing feel crewed rather than like a drone.
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

# Bright and saturated — cartoon colour, not military grey.
HULL = Vec4(0.91, 0.26, 0.24, 1.0)  # cherry red
HULL_TRIM = Vec4(0.97, 0.97, 0.98, 1.0)  # white
ACCENT = Vec4(1.00, 0.78, 0.16, 1.0)  # sunny yellow
ENGINE_CASE = Vec4(0.36, 0.39, 0.47, 1.0)  # gunmetal
ENGINE_HOT = Vec4(1.00, 0.62, 0.15, 1.0)
CANOPY_GLASS = Vec4(0.62, 0.90, 1.00, 0.30)  # pale cyan, mostly see-through
HELMET = Vec4(0.97, 0.97, 0.98, 1.0)
VISOR = Vec4(0.10, 0.16, 0.26, 1.0)
SKIN = Vec4(0.94, 0.76, 0.60, 1.0)


def _icosahedron():
    """Unit icosahedron: 12 vertices, 20 faces."""
    import math as _m

    t = (1.0 + _m.sqrt(5.0)) / 2.0
    verts = [
        Vec3(-1, t, 0), Vec3(1, t, 0), Vec3(-1, -t, 0), Vec3(1, -t, 0),
        Vec3(0, -1, t), Vec3(0, 1, t), Vec3(0, -1, -t), Vec3(0, 1, -t),
        Vec3(t, 0, -1), Vec3(t, 0, 1), Vec3(-t, 0, -1), Vec3(-t, 0, 1),
    ]
    for v in verts:
        v.normalize()

    faces = [
        (0, 11, 5), (0, 5, 1), (0, 1, 7), (0, 7, 10), (0, 10, 11),
        (1, 5, 9), (5, 11, 4), (11, 10, 2), (10, 7, 6), (7, 1, 8),
        (3, 9, 4), (3, 4, 2), (3, 2, 6), (3, 6, 8), (3, 8, 9),
        (4, 9, 5), (2, 4, 11), (6, 2, 10), (8, 6, 7), (9, 8, 1),
    ]
    return verts, faces


def make_sphere(subdivisions: int = 1, name: str = "sphere") -> NodePath:
    """A unit-radius icosphere, built in code.

    Panda3D ships `models/misc/sphere`, but that asset lives inside the installed
    package and is not carried into a frozen build — the packaged game died on
    "Couldn't load file models/misc/sphere.egg". Generating it here keeps the project
    genuinely asset-free, so what runs from source and what ships are the same thing.

    Flat-shaded at low subdivision on purpose: faceted rocks suit the cartoon look far
    better than a smooth ball, and it matches the ship's shading.
    """
    verts, faces = _icosahedron()

    for _ in range(subdivisions):
        midpoint: dict[tuple[int, int], int] = {}
        new_faces = []

        def mid(a: int, b: int) -> int:
            key = (min(a, b), max(a, b))
            if key not in midpoint:
                m = verts[a] + verts[b]
                m.normalize()
                verts.append(m)
                midpoint[key] = len(verts) - 1
            return midpoint[key]

        for a, b, c in faces:
            ab, bc, ca = mid(a, b), mid(b, c), mid(c, a)
            new_faces += [(a, ab, ca), (b, bc, ab), (c, ca, bc), (ab, bc, ca)]
        faces = new_faces

    fmt = GeomVertexFormat.getV3n3()
    vdata = GeomVertexData(name, fmt, Geom.UHStatic)
    vdata.setNumRows(len(faces) * 3)
    vertex = GeomVertexWriter(vdata, "vertex")
    normal = GeomVertexWriter(vdata, "normal")
    tris = GeomTriangles(Geom.UHStatic)

    index = 0
    for a, b, c in faces:
        va, vb, vc = verts[a], verts[b], verts[c]
        n = (vb - va).cross(vc - va)
        if n.lengthSquared() > 1e-12:
            n.normalize()
        else:
            n = Vec3(0, 0, 1)
        for v in (va, vb, vc):
            vertex.addData3(v)
            normal.addData3(n)
        tris.addVertices(index, index + 1, index + 2)
        index += 3

    geom = Geom(vdata)
    geom.addPrimitive(tris)
    node = GeomNode(name)
    node.addGeom(geom)
    return NodePath(node)


# Built once and copied; every blob in the game is a scaled instance of this.
_SPHERE_PROTO: NodePath | None = None


def _sphere_proto() -> NodePath:
    global _SPHERE_PROTO
    if _SPHERE_PROTO is None:
        _SPHERE_PROTO = make_sphere(1)
    return _SPHERE_PROTO


def _blob(loader, parent, scale, pos, color, name):
    """A scaled sphere. The cartoon vocabulary is basically this, repeated.

    `loader` is accepted but unused — kept so callers read the same whether the geometry
    comes from an asset or from code.
    """
    np = _sphere_proto().copyTo(parent)
    np.setName(name)
    np.setScale(*scale)
    np.setPos(*pos)
    np.setColor(color)
    return np


def booster_mounts(spec: dict) -> list[tuple[float, float]]:
    """Where this booster style puts its pods, as (x, z) offsets.

    Shared with the exhaust plume so the flames always come out of the actual nozzles
    rather than a hardcoded pair of positions.
    """
    count = spec.get("count", 2)
    if count == 1:
        return [(0.0, -0.10)]
    if count == 4:
        return [(-0.50, 0.10), (0.50, 0.10), (-0.34, -0.30), (0.34, -0.30)]
    return [(-0.46, -0.10), (0.46, -0.10)]


def make_ship(loader, name: str = "ship", booster: dict | None = None,
              hat: dict | None = None) -> NodePath:
    """Build the cartoon ship.

    booster and hat are cosmetic spec dicts (see cosmetics.py); omitting them gives the
    stock ship, which is what other players' ships and the packaged default use.
    """
    booster = booster or {"count": 2, "scale": 1.0}
    root = NodePath(name)

    # --- fuselage: a fat teardrop, wider than it is tall ---
    _blob(loader, root, (0.62, 1.15, 0.50), (0, 0.05, 0), HULL, "body")

    # Blunt rounded nose — cartoon ships are never pointy.
    _blob(loader, root, (0.40, 0.46, 0.36), (0, 1.10, -0.02), HULL_TRIM, "nose")

    # White belly stripe, sunk into the hull so only the underside shows.
    _blob(loader, root, (0.50, 0.95, 0.30), (0, 0.05, -0.28), HULL_TRIM, "belly")

    # --- wings: rounded paddles, swept slightly back ---
    for side, sx in (("l", -1.0), ("r", 1.0)):
        wing = _blob(
            loader, root, (0.95, 0.52, 0.10), (sx * 1.05, -0.25, -0.06), HULL, f"wing_{side}"
        )
        wing.setHpr(sx * -18, 0, 0)
        # Yellow tip cap, the classic toy-rocket detail.
        _blob(
            loader,
            root,
            (0.20, 0.24, 0.11),
            (sx * 1.85, -0.42, -0.06),
            ACCENT,
            f"wingtip_{side}",
        )

    # --- tail fin, set well back so it never crowds the canopy ---
    fin = _blob(loader, root, (0.09, 0.32, 0.44), (0, -1.05, 0.40), HULL, "fin")
    fin.setHpr(0, 14, 0)

    # --- engines: oversized, because cartoon ---
    _build_boosters(loader, root, booster)

    # --- cockpit: pilot first, then the glass over the top ---
    _build_pilot(loader, root, hat)

    # Roomy and only lightly tinted. A darker or tighter bubble hides the pilot, which
    # defeats the point of putting one in there.
    canopy = _blob(
        loader, root, (0.52, 0.62, 0.56), (0, 0.40, 0.40), CANOPY_GLASS, "canopy"
    )
    # Transparent glass must be drawn after the pilot inside it, and must not write
    # depth, or it would hide the head it is supposed to show.
    canopy.setTransparency(TransparencyAttrib.MAlpha)
    canopy.setDepthWrite(False)
    canopy.setBin("transparent", 10)
    # Unlit. A lit low-alpha dome against empty space composites to nearly black and
    # reads as a hood over the pilot rather than as glass over him; unlit keeps the
    # tint bright and consistent from every angle.
    canopy.setLightOff()

    return root


def _build_boosters(loader, root: NodePath, spec: dict) -> None:
    """Engine pods, laid out and sized by the equipped booster cosmetic."""
    scale = spec.get("scale", 1.0)
    long_pods = spec.get("long", False)
    length = 0.46 * scale * (1.6 if long_pods else 1.0)

    for i, (sx, sz) in enumerate(booster_mounts(spec)):
        _blob(
            loader, root,
            (0.28 * scale, length, 0.28 * scale),
            (sx, -1.05, sz),
            ENGINE_CASE,
            f"engine{i}",
        )
        # Hot nozzle mouth, visible straight down the chase camera.
        _blob(
            loader, root,
            (0.20 * scale, 0.12 * scale, 0.20 * scale),
            (sx, -1.05 - length * 0.9, sz),
            ENGINE_HOT,
            f"nozzle{i}",
        )


def _build_hat(loader, pilot: NodePath, spec: dict) -> None:
    """Headwear, sitting on top of the helmet.

    Positioned relative to the helmet rather than the hull so it stays put whatever the
    rest of the ship is wearing.
    """
    shape = spec.get("shape")
    if not shape:
        return
    color = Vec4(*spec.get("color", (1, 1, 1)), 1.0)
    top = 0.68  # just above the helmet crown

    if shape == "cap":
        _blob(loader, pilot, (0.24, 0.24, 0.10), (0, 0.40, top), color, "cap")
        _blob(loader, pilot, (0.20, 0.16, 0.04), (0, 0.62, top - 0.03), color, "peak")

    elif shape == "cone":
        # A stack of shrinking blobs reads as a cone at this scale.
        for i in range(4):
            t = i / 3.0
            _blob(
                loader, pilot,
                (0.20 * (1 - t * 0.8), 0.20 * (1 - t * 0.8), 0.10),
                (0, 0.40, top + i * 0.11),
                color, f"cone{i}",
            )

    elif shape == "top":
        _blob(loader, pilot, (0.30, 0.30, 0.04), (0, 0.40, top), color, "brim")
        _blob(loader, pilot, (0.19, 0.19, 0.22), (0, 0.40, top + 0.18), color, "crown")

    elif shape == "crown":
        _blob(loader, pilot, (0.24, 0.24, 0.09), (0, 0.40, top), color, "band")
        for i, a in enumerate((-0.18, 0.0, 0.18)):
            _blob(loader, pilot, (0.05, 0.05, 0.13), (a, 0.40, top + 0.14), color, f"spike{i}")

    elif shape == "halo":
        ring = _blob(loader, pilot, (0.26, 0.26, 0.035), (0, 0.40, top + 0.22), color, "halo")
        ring.setLightOff()  # it should glow, not be lit


def _build_pilot(loader, parent: NodePath, hat: dict | None = None) -> NodePath:
    """A helmeted head, sized with cartoon proportions — deliberately oversized.

    Everything here is scaled and positioned for one specific viewpoint: the chase
    camera, sitting behind and above the ship looking down at the canopy. The head sits
    high and the crest runs front-to-back so the pilot is unmistakable from that angle
    rather than only from a side-on view nobody ever sees.
    """
    pilot = parent.attachNewNode("pilot")

    # Shoulders, just enough to read as a body under the canopy rim.
    _blob(loader, pilot, (0.34, 0.22, 0.17), (0, 0.30, 0.16), ACCENT, "torso")

    # Head/helmet, and it is meant to be too big. The pilot is the only part of the ship
    # with a face, and at the distance the chase camera sits a realistically-proportioned
    # head is about four pixels. Everything below is sized off the helmet so the face,
    # visor and crest keep their proportions if it is resized again.
    helmet = (0.40, 0.41, 0.41)
    _blob(loader, pilot, helmet, (0, 0.40, 0.50), HELMET, "helmet")

    # Face opening with a dark visor across it, facing forward (+Y). Pushed further out
    # in front of the helmet centre than the old head needed, or the bigger dome swallows
    # it and the pilot has no face from behind.
    _blob(loader, pilot, (0.27, 0.17, 0.23), (0, 0.66, 0.48), SKIN, "face")
    _blob(loader, pilot, (0.26, 0.13, 0.14), (0, 0.715, 0.525), VISOR, "visor")

    # Helmet crest, running fore-and-aft along the top — the clearest possible read on
    # facing from directly behind.
    _blob(loader, pilot, (0.07, 0.33, 0.11), (0, 0.38, 0.80), ACCENT, "crest")

    if hat:
        _build_hat(loader, pilot, hat)

    return pilot


# --- mothership palette -----------------------------------------------------
MS_HULL = Vec4(0.30, 0.46, 0.72, 1.0)  # deep blue
MS_HULL_LIGHT = Vec4(0.55, 0.70, 0.90, 1.0)
MS_TRIM = Vec4(0.97, 0.85, 0.35, 1.0)  # gold banding
MS_BAY = Vec4(0.35, 1.00, 0.70, 1.0)  # glowing docking bay
MS_ENGINE = Vec4(0.45, 0.80, 1.00, 1.0)


def make_mothership(loader, name: str = "mothership") -> NodePath:
    """Big friendly cartoon station: saucer hull, gold banding, glowing bay.

    Modelled at unit radius so the renderer can scale it by whatever radius the server
    reports (30 m today). Same visual language as the player ship — rounded primitives,
    saturated colours — so it reads as the same universe rather than a grey planet.

    The docking bay glows and faces +Y, and the whole thing is deliberately readable from
    a long way out, because it is the thing every haul is aimed at.
    """
    root = NodePath(name)

    # Main saucer: wide and flattened.
    _blob(loader, root, (1.00, 1.00, 0.42), (0, 0, 0), MS_HULL, "hull")

    # Gold equator band, slightly proud of the hull.
    _blob(loader, root, (1.04, 1.04, 0.13), (0, 0, 0), MS_TRIM, "band")

    # Command dome on top and a smaller blister underneath.
    _blob(loader, root, (0.46, 0.46, 0.40), (0, 0, 0.34), MS_HULL_LIGHT, "dome")
    _blob(loader, root, (0.30, 0.30, 0.22), (0, 0, -0.34), MS_HULL_LIGHT, "underdome")

    # Glowing docking bay on the front face — the thing you actually aim your haul at.
    bay = _blob(loader, root, (0.34, 0.30, 0.22), (0, 0.92, 0), MS_BAY, "bay")
    bay.setLightOff()
    bay_ring = _blob(loader, root, (0.44, 0.16, 0.30), (0, 0.88, 0), MS_TRIM, "bay_ring")
    bay_ring.setLightOff()

    # Engine pods around the back, glowing softly.
    for i, (sx, sz) in enumerate(((-0.62, 0.0), (0.62, 0.0), (0.0, 0.46), (0.0, -0.46))):
        pod = _blob(
            loader, root, (0.20, 0.26, 0.20), (sx, -0.92, sz), MS_ENGINE, f"pod{i}"
        )
        pod.setLightOff()

    # A ring of small windows so the scale reads as "big ship", not "big rock".
    import math as _math

    for i in range(12):
        a = (i / 12.0) * 2.0 * _math.pi
        _blob(
            loader,
            root,
            (0.07, 0.07, 0.05),
            (_math.cos(a) * 0.86, _math.sin(a) * 0.86, 0.16),
            MS_TRIM,
            f"win{i}",
        ).setLightOff()

    return root


def make_boost_flame(name: str = "boostflame", booster: dict | None = None,
                     exhaust: dict | None = None) -> NodePath:
    """The big plume that fires out the back while boosting.

    Shares the idle glow's construction but is deliberately a different object rather than
    a scaled-up copy of it: boosting has to be unmistakable from across the map and from
    behind another player's ship, so this runs far longer, wider and hotter, with a
    white-hot throat that the idle plume never has. The renderer shows and hides it — the
    server decides who is actually boosting, so this is never drawn for someone merely
    holding the key on an empty tank.
    """
    booster = booster or {"count": 2, "scale": 1.0}
    exhaust = exhaust or {}

    fmt = GeomVertexFormat.getV3c4()
    vdata = GeomVertexData(name, fmt, Geom.UHStatic)
    vertex = GeomVertexWriter(vdata, "vertex")
    color = GeomVertexWriter(vdata, "color")
    tris = GeomTriangles(Geom.UHStatic)

    # The throat is nearly white whatever the cosmetic colours are, which is what sells it
    # as hot rather than merely large.
    hot = Vec4(*exhaust.get("hot", (1.0, 0.80, 0.35)), 0.95)
    throat = Vec4(1.0, 0.97, 0.88, 0.95)
    cool = Vec4(*exhaust.get("cool", (1.0, 0.35, 0.10)), 0.0)

    scale = booster.get("scale", 1.0)
    start = -1.50 * (1.4 if booster.get("long") else 1.0)
    # Five times the idle plume's reach. At the chase camera's distance anything shorter
    # is a flicker behind the hull rather than a jet.
    end = start - 0.85 * scale * 8.0

    index = 0
    for sx, sz in booster_mounts(booster):
        w = 0.42 * scale  # and about two and a half times as wide
        # Two crossed sheets per mount, so the plume has volume from any viewing angle
        # instead of vanishing to a line when seen edge-on.
        for axis in ("h", "v"):
            if axis == "h":
                near = [Vec3(sx - w, start, sz), Vec3(sx + w, start, sz)]
                far = [Vec3(sx + w * 0.28, end, sz), Vec3(sx - w * 0.28, end, sz)]
            else:
                near = [Vec3(sx, start, sz - w), Vec3(sx, start, sz + w)]
                far = [Vec3(sx, end, sz + w * 0.28), Vec3(sx, end, sz - w * 0.28)]

            for p, c in zip(near + far, (throat, hot, cool, cool)):
                vertex.addData3(p)
                color.addData4(c)

            tris.addVertices(index, index + 1, index + 2)
            tris.addVertices(index, index + 2, index + 3)
            # Both windings, so the sheet is visible from either side.
            tris.addVertices(index + 2, index + 1, index)
            tris.addVertices(index + 3, index + 2, index)
            index += 4

    geom = Geom(vdata)
    geom.addPrimitive(tris)
    node = GeomNode(name)
    node.addGeom(geom)

    np = NodePath(node)
    np.setLightOff()
    np.setTransparency(TransparencyAttrib.MAlpha)
    np.setDepthWrite(False)
    np.setBin("transparent", 21)
    return np


def make_engine_glow(name: str = "glow", booster: dict | None = None,
                     exhaust: dict | None = None) -> NodePath:
    """Unlit exhaust plume trailing behind the engines.

    Raw geometry rather than a sphere so it can fade to transparent along its length,
    and unlit so it reads as emissive against the black background. Plumes are placed
    from the booster's own mount points, so a quad cluster gets four flames and a single
    core gets one.
    """
    booster = booster or {"count": 2, "scale": 1.0}
    exhaust = exhaust or {}

    fmt = GeomVertexFormat.getV3c4()
    vdata = GeomVertexData(name, fmt, Geom.UHStatic)
    vertex = GeomVertexWriter(vdata, "vertex")
    color = GeomVertexWriter(vdata, "color")

    tris = GeomTriangles(Geom.UHStatic)

    hot = Vec4(*exhaust.get("hot", (1.0, 0.80, 0.35)), 0.85)
    cool = Vec4(*exhaust.get("cool", (1.0, 0.35, 0.10)), 0.0)

    scale = booster.get("scale", 1.0)
    start = -1.50 * (1.4 if booster.get("long") else 1.0)
    end = start - 0.85 * scale * 1.6

    index = 0
    for sx, sz in booster_mounts(booster):
        w = 0.17 * scale
        pts = [
            Vec3(sx - w, start, sz),
            Vec3(sx + w, start, sz),
            Vec3(sx + w * 0.5, end, sz + 0.03),
            Vec3(sx - w * 0.5, end, sz + 0.03),
        ]
        cols = [hot, hot, cool, cool]
        for p, c in zip(pts, cols):
            vertex.addData3(p)
            color.addData4(c)

        tris.addVertices(index, index + 1, index + 2)
        tris.addVertices(index, index + 2, index + 3)
        index += 4

    geom = Geom(vdata)
    geom.addPrimitive(tris)
    node = GeomNode(name)
    node.addGeom(geom)

    np = NodePath(node)
    np.setLightOff()
    np.setTransparency(TransparencyAttrib.MAlpha)
    np.setDepthWrite(False)
    np.setBin("transparent", 20)
    return np
