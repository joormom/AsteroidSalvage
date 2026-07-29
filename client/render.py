"""Panda3D scene construction and per-frame sync from interpolated world state.

The client owns no game logic. It creates a node the first time it sees an entity,
moves nodes to wherever the interpolator says they are, and removes nodes for entities
that stop appearing. Nothing here decides anything about the game.
"""

from __future__ import annotations

import math
import os
import random
import sys

from panda3d.core import (
    AmbientLight,
    CardMaker,
    ColorBlendAttrib,
    DirectionalLight,
    Geom,
    GeomNode,
    GeomPoints,
    GeomVertexData,
    GeomVertexFormat,
    GeomVertexWriter,
    LineSegs,
    NodePath,
    Point2,
    PointLight,
    Quat,
    TransparencyAttrib,
    Vec3,
    Vec4,
)

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

from effects import Effects  # noqa: E402
from hudgeom import make_tractor_beam  # noqa: E402
from props import make_prop  # noqa: E402
from spacedust import make_dust, make_nebula, wrap_dust  # noqa: E402
from shipmodel import (  # noqa: E402
    make_boost_flame,
    make_engine_glow,
    make_mothership,
    make_ship,
    make_sphere,
)

# Colours are deliberately high-contrast and desaturated apart from the accents: in an
# empty black volume, silhouette and rim light are the only depth cues available.
# Ships carry their own vertex colours (see shipmodel.py); these tint them so you can
# tell your own hull from everyone else's at a glance.
TINT_OWN_SHIP = Vec4(1.00, 1.00, 1.00, 1.0)
TINT_OTHER_SHIP = Vec4(0.72, 0.80, 1.00, 1.0)
COLOR_ASTEROID = Vec4(0.42, 0.40, 0.38, 1.0)
COLOR_ASTEROID_DAMAGED = Vec4(0.55, 0.28, 0.20, 1.0)
COLOR_HELD = Vec4(1.0, 0.82, 0.30, 1.0)
COLOR_MOTHERSHIP = Vec4(0.20, 0.32, 0.45, 1.0)

# How strongly a team's colour washes over a hull. Full saturation would throw away the
# ship's own shading and read as a flat silhouette, so the tint is blended toward white:
# you can still see the model, but "that's a red one" lands instantly.
TEAM_TINT_STRENGTH = 0.72

# Your own ship stays bright regardless of team, because in a red-vs-red exchange the
# one thing you must never lose track of is which ship you are flying.
OWN_SHIP_LIFT = 0.35

STAR_COUNT = 1400
STAR_SHELL_RADIUS = 4000.0


# How far up the barrel the reticle is projected, in metres.
#
# The aim axis and the camera axis are not the same line, so the crosshair's screen
# position depends on how far away you resolve it. Far enough out that it reads as "down
# the barrel" for anything at fighting range, and short enough that it does not sit on the
# vanishing point where it stops responding to the ship at all.
# A bolt is drawn far longer than it is wide so it reads as a round in flight rather than
# a floating dot. Scaled off the unit sphere, in metres.
BOLT_LENGTH = 5.5
BOLT_GIRTH = 0.5

RETICLE_RANGE = 600.0
RETICLE_RGBA = (0.85, 0.93, 1.0, 0.9)
RETICLE_EMPTY_SCALE = (1.0, 0.45, 0.38, 0.75)


def _bounding_radius(model: NodePath) -> float:
    """Half the model's largest horizontal extent, in model units.

    Used to normalise a model's draw scale against its collision radius so the hull you
    see is the hull that collides. Horizontal only: wingspan is what players judge a
    near miss by, and including a tall fin would shrink the ship to compensate.
    """
    lo, hi = model.getTightBounds()
    return max(max(abs(lo[0]), abs(hi[0])), max(abs(lo[1]), abs(hi[1])), 1e-3)


class SceneRenderer:
    def __init__(self, base, loadout=None):
        self.base = base
        # Cosmetics apply to the player's own ship and beam. Other players render stock:
        # loadouts are not on the wire, so there is nothing to render them from.
        self.loadout = loadout or {}
        self.nodes: dict[int, NodePath] = {}
        # Last-seen kind, team and radius per entity. Effects need these after the body
        # has already left the snapshot — an explosion is drawn for something that no
        # longer exists, which is the whole point of it.
        self.meta: dict[int, tuple[int, int, float]] = {}
        # Floating health bars, created lazily the first time a body is damaged.
        self._bars: dict[int, tuple[NodePath, NodePath]] = {}
        # Shield bubbles, one per station that has bought any.
        self._shields: dict[int, NodePath] = {}
        # The crate inside each cargo bubble, kept so it can be spun. A child of the
        # pickup node, so it dies with it.
        self._pickup_boxes: dict[int, NodePath] = {}
        # Seconds since the renderer started, for anything that idles or tumbles.
        self._spin = 0.0
        # Last-seen tier per entity. Impact sparks colour an asteroid by its tier, and an
        # event asks after the body may already have left the snapshot.
        self._tiers: dict[int, int] = {}
        # Stations hidden because they were breached, and which must come back when the
        # server rebuilds them for the next round.
        self._breached: set[int] = set()
        self.world = base.render.attachNewNode("world")

        # Generated rather than loaded: Panda3D's bundled models are not carried into a
        # frozen build, and the packaged game crashed on the missing sphere asset.
        self._sphere = make_sphere(1)
        # Built once and copied per ship, so 16 players cost one assembly.
        # Stock meshes, used for everyone else's ship. Built once.
        self._ship_mesh = make_ship(base.loader)
        self._mothership_mesh = make_mothership(base.loader)
        self._glow_mesh = make_engine_glow()
        self._flame_mesh = make_boost_flame()

        # Yours, rebuilt whenever the ship editor applies a new loadout.
        self._own_mesh = None
        self._own_glow = None
        self._own_flame = None
        self._build_own_meshes()

        # Models must be drawn at the size they actually collide at.
        #
        # The ship mesh spans about +/-2.04 units, so scaling it by the 2 m collision
        # radius drew a 4 m half-width ship around a 2 m sphere: wings visibly passed
        # through asteroids that were never touched, which reads as "collision is
        # broken". Normalising by the mesh's own bounding radius makes what you see the
        # thing that actually hits.
        # Measured on the stock hull so a cosmetic with wider pods cannot shrink the
        # ship relative to its collider.
        self._ship_mesh_radius = _bounding_radius(self._ship_mesh)
        self._mothership_mesh_radius = _bounding_radius(self._mothership_mesh)

        self.reticle = self._make_reticle()

        self.beam = None
        self.beam = make_tractor_beam(style=self.loadout.get("beam"))
        self.beam.reparentTo(self.world)
        self.beam.hide()
        # Neutral until the server says which crew we are on.
        self._beam_rgb = (1.0, 1.0, 1.0)

        # Transient VFX: laser bolts, impacts, wrecks.
        self.effects = Effects(base, self.world, self._sphere)

        self._setup_lighting()
        self._setup_starfield()

        # Near-field dust and distant cloud, the two things that make speed legible.
        self._dust = make_dust()
        self._dust.reparentTo(self.world)
        self._nebula = make_nebula(self.world)

        base.setBackgroundColor(0.02, 0.02, 0.04, 1.0)

        # Third-person chase camera state, smoothed in update_camera.
        self._cam_pos = None
        base.camLens.setFov(75)
        base.camLens.setNear(0.35)
        base.camLens.setFar(12000)

    # --- construction ------------------------------------------------------

    def _setup_lighting(self):
        ambient = AmbientLight("ambient")
        # Generous ambient: the ships are cartoon-coloured, and deep shadow would mute
        # the saturated reds and yellows into mud. Also keeps unlit faces reading as
        # "in shadow" rather than as holes in the world.
        ambient.setColor(Vec4(0.52, 0.53, 0.58, 1.0))
        self.world.setLight(self.base.render.attachNewNode(ambient))

        key = DirectionalLight("key")
        key.setColor(Vec4(0.85, 0.82, 0.75, 1.0))
        key_np = self.base.render.attachNewNode(key)
        key_np.setHpr(35, -45, 0)
        self.world.setLight(key_np)

        # Fill from below-behind. Without it the ship goes near-black whenever it turns
        # away from the key, which in an empty volume happens constantly — cartoon
        # colour wants flat, even light far more than it wants dramatic shading.
        fill = DirectionalLight("fill")
        fill.setColor(Vec4(0.34, 0.36, 0.42, 1.0))
        fill_np = self.base.render.attachNewNode(fill)
        fill_np.setHpr(-160, 35, 0)
        self.world.setLight(fill_np)

        # Cool rim from the opposite side so silhouettes separate from the background.
        rim = DirectionalLight("rim")
        rim.setColor(Vec4(0.22, 0.38, 0.62, 1.0))
        rim_np = self.base.render.attachNewNode(rim)
        rim_np.setHpr(-140, 25, 0)
        self.world.setLight(rim_np)

        # The mothership glows, marking home from a long way out.
        home = PointLight("home")
        home.setColor(Vec4(0.35, 0.55, 0.85, 1.0))
        home.setAttenuation((1.0, 0.0, 0.000004))
        home_np = self.world.attachNewNode(home)
        home_np.setPos(0, 0, 0)
        self.world.setLight(home_np)

    def _setup_starfield(self):
        """A shell of points, parented to the camera so it never gets closer."""
        fmt = GeomVertexFormat.getV3c4()
        vdata = GeomVertexData("stars", fmt, Geom.UHStatic)
        vdata.setNumRows(STAR_COUNT)

        vertex = GeomVertexWriter(vdata, "vertex")
        color = GeomVertexWriter(vdata, "color")

        rng = random.Random(20260728)
        for _ in range(STAR_COUNT):
            # Uniform on a sphere; naive angle sampling clumps at the poles.
            z = rng.uniform(-1.0, 1.0)
            theta = rng.uniform(0.0, 2.0 * math.pi)
            r = math.sqrt(max(0.0, 1.0 - z * z))
            vertex.addData3(
                STAR_SHELL_RADIUS * r * math.cos(theta),
                STAR_SHELL_RADIUS * r * math.sin(theta),
                STAR_SHELL_RADIUS * z,
            )
            b = rng.uniform(0.25, 1.0)
            color.addData4(b, b, min(1.0, b * 1.1), 1.0)

        points = GeomPoints(Geom.UHStatic)
        points.addNextVertices(STAR_COUNT)
        geom = Geom(vdata)
        geom.addPrimitive(points)

        node = GeomNode("starfield")
        node.addGeom(geom)

        self.stars = self.base.camera.attachNewNode(node)
        self.stars.setLightOff()
        self.stars.setBin("background", 0)
        self.stars.setDepthWrite(False)
        self.stars.setCompass()  # keep orientation fixed as the camera turns

    # --- per-frame ---------------------------------------------------------

    def _build_own_meshes(self) -> None:
        booster = self.loadout.get("booster")
        hat = self.loadout.get("hat")
        exhaust = self.loadout.get("exhaust")

        if self._own_mesh is not None:
            self._own_mesh.removeNode()
        if self._own_glow is not None:
            self._own_glow.removeNode()
        if self._own_flame is not None:
            self._own_flame.removeNode()

        self._own_mesh = make_ship(self.base.loader, booster=booster, hat=hat)
        self._own_glow = make_engine_glow(booster=booster, exhaust=exhaust)
        self._own_flame = make_boost_flame(booster=booster, exhaust=exhaust)

    def set_loadout(self, loadout: dict) -> None:
        """Swap in a new cosmetic loadout and rebuild everything that depends on it.

        Existing ship nodes are dropped so sync() recreates them from the new prototype.
        Without this the loadout was only read once at startup, so applying a change in
        the editor did nothing until the game was restarted.
        """
        self.loadout = loadout or {}
        self._build_own_meshes()

        if self.beam is not None:
            self.beam.removeNode()
        self.beam = make_tractor_beam(style=self.loadout.get("beam"))
        self.beam.reparentTo(self.world)
        self.beam.hide()

        for entity, node in list(self.nodes.items()):
            node.removeNode()
            del self.nodes[entity]
        self.meta = {}

    def clear(self) -> None:
        """Remove every world node. Used when leaving a game so the next one does not
        start with the previous match's asteroids hanging in space."""
        for node in self.nodes.values():
            node.removeNode()
        self.nodes = {}
        self.meta = {}
        # The dust and the nebula are the map's sky, not its contents: they survive a
        # world teardown so leaving a match does not fly you into a black void.
        for track, _fill in self._bars.values():
            track.removeNode()
        self._bars = {}
        for bubble in self._shields.values():
            bubble.removeNode()
        self._shields = {}
        self._pickup_boxes = {}
        self._breached = set()
        self.effects.clear()
        self.beam.hide()
        self._cam_pos = None

    def sync(self, bodies, own_ship: int) -> None:
        # Advanced from the frame clock rather than a passed-in dt, so nothing that only
        # tumbles for decoration has to be threaded through every caller.
        self._spin += globalClock.getDt()
        seen = set()

        for b in bodies:
            seen.add(b.entity)
            self.meta[b.entity] = (b.kind, b.team, b.radius)
            self._tiers[b.entity] = b.tier
            node = self.nodes.get(b.entity)
            if node is None:
                node = self._create_node(b, own_ship)
                self.nodes[b.entity] = node

            node.setPos(b.pos.x, b.pos.y, b.pos.z)
            node.setQuat(Quat(b.rot.w, b.rot.x, b.rot.y, b.rot.z))

            if b.kind == proto.KIND_BOLT:
                # Pose is all a bolt needs per frame; its stretch is set once at creation.
                pass
            elif b.kind == proto.KIND_PICKUP:
                # The bubble is the collection radius, so it is drawn at exactly that.
                node.setScale(max(1.0, b.radius))
                box = self._pickup_boxes.get(b.entity)
                if box is not None:
                    # A slow tumble. Static boxes in a static field read as scenery; a
                    # turning one reads as something to go and get.
                    box.setHpr(self._spin * 40.0, self._spin * 26.0, 0)
            elif b.kind == proto.KIND_HAZARD:
                # Team colour while held, neutral white while empty or contested. The
                # server sends 0xFF for both, which is right: neither pays anyone.
                node.setScale(max(1.0, b.radius))
                if b.team == proto.NO_TEAM:
                    node.setColor(0.80, 0.84, 0.92, 0.30)
                else:
                    r, g, bl = proto.team_color(b.team)
                    node.setColor(r * 0.6 + 0.4, g * 0.6 + 0.4, bl * 0.6 + 0.4, 0.55)
            elif b.kind == proto.KIND_MOTHERSHIP:
                # A station hidden by a breach comes back when the round does.
                if b.entity in self._breached and b.health > 0.001:
                    self._breached.discard(b.entity)
                    node.show()
            elif b.kind in (proto.KIND_ASTEROID, proto.KIND_SALVAGE):
                node.setColor(self._salvage_tint(b))
            elif b.kind == proto.KIND_SHIP:
                # A destroyed ship's body is parked far off the field until it respawns.
                # Drawing it would send the wreck streaking off into the void.
                if b.dead:
                    node.hide()
                else:
                    node.show()
                    node.setColorScale(self._ship_tint(b, b.entity == own_ship))
                    self._sync_boost_flame(node, b)

            self._sync_health_bar(b, own_ship)

        for entity in [e for e in self.nodes if e not in seen]:
            self.nodes.pop(entity).removeNode()
            self.meta.pop(entity, None)
            bar = self._bars.pop(entity, None)
            if bar is not None:
                bar[0].removeNode()
            bubble = self._shields.pop(entity, None)
            if bubble is not None:
                bubble.removeNode()
            # The box is a child of the pickup node that has just gone, so it needs no
            # removeNode of its own — only the reference dropping.
            self._pickup_boxes.pop(entity, None)

    def _sync_boost_flame(self, node: NodePath, b) -> None:
        """Show or hide a ship's boost plume, and make it flicker while lit.

        Driven off the snapshot flag rather than the local keyboard, so it is correct for
        every ship on the field and never appears for a pilot holding the key on an empty
        tank — the server decides who is actually burning.
        """
        flame = node.find("**/boostflame")
        if flame.isEmpty():
            return

        if not b.boosting:
            flame.hide()
            return

        flame.show()
        # A rigid cone reads as a decal stuck to the hull. Jitter along the length only,
        # so the plume pulses rather than wobbling off-axis.
        flicker = 0.82 + random.random() * 0.36
        flame.setScale(1.0, flicker, 1.0)

    # --- floating health bars ----------------------------------------------
    #
    # Only ships and stations get one, and only once they have actually been hurt. A bar
    # over everything at all times would turn a quiet belt into a wall of UI, and the
    # question a bar answers — "is this nearly dead?" — does not exist until someone has
    # started shooting.

    def _make_pickup(self, b) -> NodePath:
        """A cargo box inside a bubble.

        The bubble is what you aim at — it is the collection radius made visible, so
        flying "through it" means what it looks like it means. The box is small and solid
        in the middle so the thing has a centre to read at distance.

        Every box is the same green whatever is inside it. What you get is a surprise
        until you have it, which makes the decision to go for one a decision about
        position rather than about shopping; the item's own colour appears on the hotbar
        slot, once it is yours.
        """
        rgb = proto.PICKUP_COLOR

        root = self.world.attachNewNode(f"pickup-{b.entity}")

        bubble = self._sphere.copyTo(root)
        bubble.setTransparency(TransparencyAttrib.MAlpha)
        bubble.setLightOff()
        bubble.setDepthWrite(False)
        bubble.setBin("transparent", 14)
        bubble.setAttrib(ColorBlendAttrib.make(
            ColorBlendAttrib.MAdd,
            ColorBlendAttrib.OIncomingAlpha,
            ColorBlendAttrib.OOne,
        ))
        # Wireframe for the same reason the hill is: a solid sphere this size is a wall
        # you cannot see the far side of, and the whole point is to fly into it.
        bubble.setRenderModeWireframe()
        bubble.setRenderModeThickness(1.3)
        bubble.setTwoSided(True)
        bubble.setColor(rgb[0], rgb[1], rgb[2], 0.40)

        box = self._sphere.copyTo(root)
        box.setLightOff()
        box.setColor(rgb[0], rgb[1], rgb[2], 1.0)
        # Squashed into a crate rather than left a ball, so it is not mistaken for a rock.
        box.setScale(0.20, 0.20, 0.20)
        self._pickup_boxes[b.entity] = box
        return root

    def _make_health_bar(self) -> tuple[NodePath, NodePath]:
        """A billboarded track with a left-anchored fill, in world space."""
        cm = CardMaker("health-track")
        cm.setFrame(-0.5, 0.5, -0.075, 0.075)
        track = NodePath(cm.generate())
        track.setColor(0.03, 0.04, 0.06, 0.80)

        # Modelled 0..1 wide so the X scale IS the fraction, the same trick the 2D bars
        # use: scaling a transform beats rebuilding geometry every frame.
        cm.setFrame(0.0, 1.0, -0.055, 0.055)
        fill = NodePath(cm.generate())
        fill.reparentTo(track)
        fill.setPos(-0.5, -0.01, 0)  # a hair in front so it never z-fights the track

        track.setTransparency(TransparencyAttrib.MAlpha)
        track.setLightOff()
        # Faces the camera wherever it is, so a bar is never edge-on and invisible.
        track.setBillboardPointEye()
        track.reparentTo(self.world)
        return track, fill

    def _sync_health_bar(self, b, own_ship: int) -> None:
        wants_bar = (
            b.kind in (proto.KIND_SHIP, proto.KIND_MOTHERSHIP)
            and b.health < 0.999
            and not (b.kind == proto.KIND_SHIP and b.dead)
            # Your own hull is the bar at the bottom of the screen. A second one floating
            # over your back would be read as somebody else's.
            and b.entity != own_ship
        )

        bar = self._bars.get(b.entity)
        if not wants_bar:
            if bar is not None:
                bar[0].hide()
            return

        if bar is None:
            bar = self._make_health_bar()
            self._bars[b.entity] = bar
        track, fill = bar
        track.show()

        # Sit the bar clear of the hull, and grow it with distance so it keeps roughly
        # constant size on screen — a station bar sized for a close pass is a smear of
        # pixels from across the map, which is exactly when you want to read it.
        pos = Vec3(b.pos.x, b.pos.y, b.pos.z)
        track.setPos(pos + Vec3(0, 0, b.radius * 1.35 + 2.0))

        dist = (pos - self.base.camera.getPos(self.base.render)).length()
        width = max(b.radius * 2.0, dist * 0.045)
        track.setScale(width)

        health = max(0.0, min(1.0, b.health))
        fill.setScale(max(0.001, health), 1, 1)
        if health <= 0.25:
            fill.setColor(1.00, 0.30, 0.26, 1.0)
        elif health <= 0.6:
            fill.setColor(1.00, 0.78, 0.25, 1.0)
        else:
            fill.setColor(0.40, 0.92, 0.48, 1.0)

    # --- combat VFX --------------------------------------------------------

    def fire_shot(self, shot) -> None:
        """Flash the barrel on the frame the trigger went down.

        This used to draw the whole beam, because shots were hitscan and the server sent
        how far the ray reached. Rounds travel now and arrive as their own bodies in the
        snapshot, so the beam draws itself — all that is left here is the muzzle, which a
        client cannot infer from a bolt that only shows up in the *next* snapshot.
        """
        node = self.nodes.get(shot.shooter)
        if node is None:
            return

        forward = node.getQuat().getForward()
        rgb = proto.team_color(shot.team)
        self.effects.add_flash(node.getPos() + forward * 2.6, 1.6, rgb)

    def update_shields(self, teams) -> None:
        """Wrap a translucent bubble around every station that has bought shields.

        Without this the upgrade is invisible: turrets announce themselves by shooting,
        but a shield only ever shows up as damage that did not happen. An attacker who
        cannot see one has no way to know why the hull bar is barely moving, and a crew
        that paid 900 credits gets nothing to look at.
        """
        levels = {t.team_id: t.shields for t in (teams or [])}

        for entity, (kind, team, radius) in self.meta.items():
            if kind != proto.KIND_MOTHERSHIP:
                continue
            level = levels.get(team, 0)
            bubble = self._shields.get(entity)

            if level <= 0:
                if bubble is not None:
                    bubble.hide()
                continue

            if bubble is None:
                bubble = self._sphere.copyTo(self.world)
                bubble.setTransparency(TransparencyAttrib.MAlpha)
                bubble.setLightOff()
                # Never writes depth, so the station stays visible through its own shield.
                bubble.setDepthWrite(False)
                bubble.setBin("transparent", 10)
                # Additive rather than ordinary alpha. Over a black sky, blending a
                # translucent surface *darkens* what is behind it — the bubble came out a
                # grey blob sitting on the hull. Adding light instead makes it read as
                # energy, and leaves the station underneath at full brightness.
                bubble.setAttrib(ColorBlendAttrib.make(
                    ColorBlendAttrib.MAdd,
                    ColorBlendAttrib.OIncomingAlpha,
                    ColorBlendAttrib.OOne,
                ))
                # A grid, not a skin. Solid faces gave a flat disc with no rim — a sphere
                # shaded uniformly is a circle — and every other piece of UI in this game
                # is line art anyway: the reticle, the shop icons, the tractor beam.
                bubble.setRenderModeWireframe()
                bubble.setRenderModeThickness(1.5)
                bubble.setTwoSided(True)
                self._shields[entity] = bubble

            node = self.nodes.get(entity)
            if node is None:
                bubble.hide()
                continue

            bubble.show()
            bubble.setPos(node.getPos())
            bubble.setScale(radius * 1.22)
            r, g, b = proto.team_color(team)
            # Brighter with each level, so "how well defended is that" is readable from
            # across the map rather than only in the scoreboard. Kept low because this is
            # added light: past about a third it stops looking like a field and starts
            # looking like the station is on fire.
            glow = 0.26 + 0.16 * min(level, 3)
            # Only lightly lifted toward white. Washing the team colour out entirely made
            # every station's shield look the same, and with four crews on the map the
            # colour is how you know whose door you are standing at.
            bubble.setColor(r * 0.75 + 0.25, g * 0.75 + 0.25, b * 0.75 + 0.25, glow)

    # --- aiming reticle -----------------------------------------------------

    def _make_reticle(self) -> NodePath:
        """A ring with crosshairs inside it, and a dot at dead centre.

        The ring is what makes the thing findable: four loose ticks on a starfield read as
        four stars until you already know where to look, while a closed circle is a shape
        nothing else in the scene has. The arms stop short of the middle on purpose — a
        solid crosshair covers the one thing you are trying to look at, which on a distant
        ship is most of it.
        """
        ls = LineSegs()
        ls.setThickness(1.6)
        ls.setColor(*RETICLE_RGBA)

        radius = 0.034
        steps = 48
        for i in range(steps + 1):
            a = math.tau * i / steps
            x, z = math.cos(a) * radius, math.sin(a) * radius
            if i == 0:
                ls.moveTo(x, 0, z)
            else:
                ls.drawTo(x, 0, z)

        # Crosshairs inside the ring: from a gap around the centre out to just short of
        # the circle, so the two shapes stay legible instead of merging into a blob.
        gap, arm = 0.009, radius - 0.006
        for dx, dz in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            ls.moveTo(dx * gap, 0, dz * gap)
            ls.drawTo(dx * arm, 0, dz * arm)

        # A dot at dead centre, so the exact aim point is still readable against a bright
        # rock where the thin lines wash out.
        ls.setThickness(2.6)
        ls.moveTo(0, 0, 0)
        ls.drawTo(0.0012, 0, 0)

        np = NodePath(ls.create())
        np.setLightOff()
        np.setTransparency(TransparencyAttrib.MAlpha)
        np.reparentTo(self.base.aspect2d)
        np.hide()
        return np

    def update_reticle(self, own_ship: int, can_fire: bool = True,
                       visible: bool = True) -> None:
        """Put the reticle where this ship's laser would actually go.

        The gun fires straight down the hull's nose, but the camera sits behind and above
        it, so the aim axis does *not* project to the middle of the screen — a fixed
        centre crosshair would be a lie that gets worse the closer the target is. This
        projects a point far up the barrel and tracks it, which is where a shot lands.
        """
        node = self.nodes.get(own_ship) if own_ship else None
        if node is None or not visible:
            self.reticle.hide()
            return

        aim = node.getPos() + node.getQuat().getForward() * RETICLE_RANGE
        film = Point2()
        if not self.base.camLens.project(
            self.base.cam.getRelativePoint(self.base.render, aim), film
        ):
            self.reticle.hide()  # behind the camera
            return

        self.reticle.setPos(film.getX() * self.base.getAspectRatio(), 0, film.getY())
        # Dimmed to a warning colour with an empty magazine: the reticle is the natural
        # place to answer "can I shoot right now", and it is already where you are looking.
        self.reticle.setColorScale(
            Vec4(1, 1, 1, 1) if can_fire else Vec4(*RETICLE_EMPTY_SCALE)
        )
        self.reticle.show()

    def explode(self, entity: int, rgb=None, scale: float = 1.0, shards: int = 14) -> None:
        """Blow up whatever is at an entity's last known position."""
        node = self.nodes.get(entity)
        if node is None:
            return
        _kind, team, radius = self.meta.get(entity, (0, proto.NO_TEAM, 2.0))
        if rgb is None:
            rgb = proto.team_color(team) if team != proto.NO_TEAM else (1.0, 0.62, 0.24)
        self.effects.add_explosion(node.getPos(), max(1.5, radius * scale), rgb,
                                   shards=shards)

    def impact(self, entity: int, damage: float = 0.0, rgb=None,
               shooter_pos=None) -> None:
        """A hit: a spark on the surface that was struck.

        Placed on the *near face* rather than at the body's centre when the shot's origin
        is known — a flash inside a 40 m asteroid is a faint glow somewhere in the middle
        of it, which reads as nothing at all. Offsetting to the surface is what makes a
        hit look like it landed on something.

        Sized by damage so a grazing laser and a missile do not look identical, and
        clamped so a big number cannot fill the screen.
        """
        node = self.nodes.get(entity)
        if node is None:
            return
        _kind, team, radius = self.meta.get(entity, (0, proto.NO_TEAM, 2.0))

        pos = node.getPos()
        if shooter_pos is not None:
            to = pos - shooter_pos
            if to.length() > 1e-3:
                pos = pos - to.normalized() * radius

        if rgb is None:
            rgb = proto.team_color(team) if team != proto.NO_TEAM else (1.0, 0.85, 0.45)

        size = max(1.2, min(6.0, 1.2 + damage * 0.22))
        self.effects.add_flash(pos, size, rgb)
        # A few sparks as well as the flash. One primitive on its own reads as a light
        # switching on; debris reads as something being hit.
        self.effects.add_debris(pos, size * 0.9, rgb, count=4, speed=18.0)

    def set_beam_team(self, team: int) -> None:
        """Colour the tractor beam for a crew.

        Lifted well toward white rather than using the raw team colour: a beam is a shaft
        of light, and a fully saturated one stops reading as emissive — the darker team
        colours in a sixteen-colour palette came out looking like a painted pole.
        """
        if team is None or team == proto.NO_TEAM:
            self._beam_rgb = (1.0, 1.0, 1.0)
            return
        r, g, b = proto.team_color(team)
        lift = 0.45
        self._beam_rgb = (
            r * (1 - lift) + lift,
            g * (1 - lift) + lift,
            b * (1 - lift) + lift,
        )

    def team_of(self, entity: int) -> int:
        """Which crew an entity belongs to, from the last snapshot that carried it.

        Read from meta rather than the live snapshot so it still answers for something
        that has just been destroyed — which is exactly when an event asks.
        """
        _kind, team, _radius = self.meta.get(entity, (0, proto.NO_TEAM, 0.0))
        return team

    def set_dust_enabled(self, on: bool) -> None:
        """Show or hide the near-field dust.

        A few hundred motes is the cheapest thing in the scene, but it is also the only
        part of it that is pure decoration — so it is the one graphics option that can be
        turned off without changing what you can see of the game.
        """
        self._dust.show() if on else self._dust.hide()

    def tier_of(self, entity: int) -> int:
        """A body's tier from the last snapshot that carried it. Asteroid colours read
        off this, and an event asks after the body may already be gone."""
        return self._tiers.get(entity, 0)

    def entity_pos(self, entity: int):
        node = self.nodes.get(entity)
        return None if node is None else node.getPos()

    def breach_station(self, entity: int) -> None:
        """Hide a station that has just been destroyed, until it is rebuilt.

        Deliberately not hide_entity: that one is for the end of a *match*, where staying
        hidden forever is correct. A breach only ends a round, and beginRound puts every
        station back on full health — so this remembers the entity and sync() shows it
        again the moment the server says it is whole.
        """
        self._breached.add(entity)
        node = self.nodes.get(entity)
        if node is not None:
            node.hide()

    def hide_entity(self, entity: int) -> None:
        """Stop drawing a body without removing it.

        Used for the end-of-match explosion: the server never destroys a mothership, so
        without this the debris flies out from a station that is visibly still sitting
        there. sync() only re-shows ships, so a hidden station stays gone until the
        world is torn down and rebuilt on the next match.
        """
        node = self.nodes.get(entity)
        if node is not None:
            node.hide()

    def mothership_of(self, team: int) -> int | None:
        """Entity id of a team's mothership, or None if it is not in view yet."""
        for entity, (kind, body_team, _radius) in self.meta.items():
            if kind == proto.KIND_MOTHERSHIP and body_team == team:
                return entity
        return None

    def _paint_mothership(self, node: NodePath, team: int) -> None:
        """Repaint a station's hull in its team's colours.

        Done by setting the colour on the named hull pieces rather than with a
        setColorScale over the whole model: the station's parts carry strong vertex
        colours of their own (gold trim, teal bay), and scaling only multiplies against
        those — a red team's station came out the same green as everyone else's. The
        glowing bay, engine pods and windows are deliberately left alone; they are the
        accents that keep it looking like a station rather than a painted ball.
        """
        rgb = proto.team_color(team)

        for name, shade in (("hull", 1.0), ("dome", 1.25), ("underdome", 0.85)):
            part = node.find(f"**/{name}")
            if not part.isEmpty():
                part.setColor(Vec4(min(1.0, rgb[0] * shade), min(1.0, rgb[1] * shade),
                                   min(1.0, rgb[2] * shade), 1.0))

        # The equator band goes pale rather than team-coloured, so the hull's colour has
        # something to read against at a distance.
        band = node.find("**/band")
        if not band.isEmpty():
            band.setColor(Vec4(0.30 + rgb[0] * 0.35, 0.30 + rgb[1] * 0.35,
                               0.30 + rgb[2] * 0.35, 1.0))

    def _ship_tint(self, b, mine: bool) -> Vec4:
        """Team colour, blended toward white so the hull still reads as a model.

        Damage darkens it: a ship one hit from dying should look like one, and the health
        bar over its head is not visible in a fast pass.
        """
        rgb = proto.team_color(b.team)
        k = TEAM_TINT_STRENGTH
        r = 1.0 - k + rgb[0] * k
        g = 1.0 - k + rgb[1] * k
        bl = 1.0 - k + rgb[2] * k

        if mine:
            r += (1.0 - r) * OWN_SHIP_LIFT
            g += (1.0 - g) * OWN_SHIP_LIFT
            bl += (1.0 - bl) * OWN_SHIP_LIFT

        # Never fully black — a scorched hull, not a hole in space.
        dim = 0.45 + 0.55 * max(0.0, min(1.0, b.health))
        return Vec4(r * dim, g * dim, bl * dim, 1.0)

    def _salvage_tint(self, b) -> Vec4:
        """Absolute colour, not a multiplier.

        Applied with setColor rather than setColorScale: scaling multiplies against the
        rock's base grey, so "gold" came out as dark mud and a held rock was harder to
        pick out than a free one.

        Tier colour is the default so value and difficulty read at a glance across the
        field; held and damaged states override it, because those are about *right now*
        and matter more than what the rock is worth.
        """
        if b.held:
            return COLOR_HELD
        if b.damaged:
            return COLOR_ASTEROID_DAMAGED
        rgb = proto.TIER_COLORS.get(b.tier)
        if rgb is None:
            return COLOR_ASTEROID
        return Vec4(rgb[0], rgb[1], rgb[2], 1.0)

    def _create_node(self, b, own_ship: int) -> NodePath:
        if b.kind == proto.KIND_SHIP:
            mine = b.entity == own_ship
            node = (self._own_mesh if mine else self._ship_mesh).copyTo(self.world)
            # Scale so the hull's widest point matches the collision sphere exactly.
            node.setScale(max(0.2, b.radius / self._ship_mesh_radius))
            node.setColorScale(self._ship_tint(b, mine))

            # The chase camera looks straight up the ship's back, so an unlit engine
            # plume is the clearest possible read on which way you are pointing.
            (self._own_glow if mine else self._glow_mesh).copyTo(node)

            # The boost plume is built once per ship and shown or hidden per frame —
            # rebuilding this geometry every time somebody taps Shift would churn a mesh
            # sixteen players wide at 30 Hz.
            flame = (self._own_flame if mine else self._flame_mesh).copyTo(node)
            flame.setName("boostflame")
            flame.hide()
            return node

        if b.kind == proto.KIND_MOTHERSHIP:
            node = self._mothership_mesh.copyTo(self.world)
            node.setScale(max(0.2, b.radius / self._mothership_mesh_radius))
            self._paint_mothership(node, b.team)
            return node

        if b.kind == proto.KIND_PICKUP:
            return self._make_pickup(b)

        if b.kind == proto.KIND_BOLT:
            # A stretched, unlit sliver in the shooter's colours. Long on +Y because the
            # server hands us a rotation that already points down the flight path.
            node = self._sphere.copyTo(self.world)
            node.setLightOff()
            node.setTransparency(TransparencyAttrib.MAlpha)
            node.setDepthWrite(False)
            node.setBin("transparent", 18)
            node.setAttrib(ColorBlendAttrib.make(
                ColorBlendAttrib.MAdd,
                ColorBlendAttrib.OIncomingAlpha,
                ColorBlendAttrib.OOne,
            ))
            r, g, bl = proto.team_color(b.team)
            # Lifted hard toward white: a bolt is a spark, and the darker team colours
            # read as a thrown pebble at full saturation.
            node.setColor(r * 0.35 + 0.65, g * 0.35 + 0.65, bl * 0.35 + 0.65, 0.95)

            if b.tier == proto.BOLT_MISSILE:
                # A missile is the same primitive at a very different size, and warmer, so
                # "that is not a laser" is readable in the fraction of a second there is
                # to decide whether to get out of the way.
                node.setColor(1.0, 0.72, 0.35, 1.0)
                node.setScale(BOLT_GIRTH * 3.2, BOLT_LENGTH * 1.6, BOLT_GIRTH * 3.2)
            else:
                node.setScale(BOLT_GIRTH, BOLT_LENGTH, BOLT_GIRTH)
            return node

        if b.kind == proto.KIND_HAZARD:
            # The King of the Hill zone: a volume you fly through, drawn as a wireframe
            # shell so you can see the far side of it and judge whether a rival inside is
            # actually inside. Same treatment as a station shield, for the same reason —
            # a solid sphere at this size is an opaque wall across the map.
            node = self._sphere.copyTo(self.world)
            node.setTransparency(TransparencyAttrib.MAlpha)
            node.setLightOff()
            node.setDepthWrite(False)
            node.setBin("transparent", 12)
            node.setAttrib(ColorBlendAttrib.make(
                ColorBlendAttrib.MAdd,
                ColorBlendAttrib.OIncomingAlpha,
                ColorBlendAttrib.OOne,
            ))
            node.setRenderModeWireframe()
            node.setRenderModeThickness(1.4)
            node.setTwoSided(True)
            node.setScale(max(1.0, b.radius))
            return node

        if b.kind == proto.KIND_PROP:
            # Scenery is built per body rather than copied from a prototype: there are
            # only a handful on a map, they never change, and building each one lets a
            # map's derelicts differ from each other by construction.
            node = make_prop(b.tier)
            node.reparentTo(self.world)
            node.setScale(max(0.2, b.radius))
            # A fixed tumble per entity, so the field does not look like a row of models
            # all sitting the same way up. Deterministic in the entity id: it must not
            # change every time a body leaves and re-enters view.
            rng = random.Random(b.entity)
            node.setHpr(rng.uniform(0, 360), rng.uniform(-40, 40), rng.uniform(0, 360))
            return node

        node = self._sphere.copyTo(self.world)
        # The bundled sphere is unit radius, so scale is the radius directly.
        node.setScale(max(0.2, b.radius))
        node.setColor(self._salvage_tint(b))
        return node

    def update_guidance(
        self, own_ship: int, held_pos, dt: float, beaming: bool = False,
        beam_range: float = 15.0,
    ) -> None:
        """Stretch the tractor beam from the ship to whatever it has hold of.

        Lives in world space rather than as a 2D overlay so it shares perspective with
        the cargo it is pulling.
        """
        ship = self.nodes.get(own_ship)
        if ship is None:
            self.beam.hide()
            return

        ship_pos = ship.getPos()
        quat = ship.getQuat()

        # --- tractor beam ---
        #
        # Two states, because "am I holding the button" and "did I catch anything" are
        # different questions and the player needs to see both. A faint beam projects
        # down the aim axis whenever the button is held so you can see the reach and
        # where it is pointing; it snaps solid and locks onto the cargo once something
        # is caught. Without the faint state, a missed grab looks identical to a broken
        # tractor beam.
        origin = ship_pos + quat.getForward() * 2.0

        if held_pos is not None:
            target = Vec3(held_pos.x, held_pos.y, held_pos.z)
            alpha, width = 1.0, 1.0
        elif beaming:
            target = origin + quat.getForward() * beam_range
            alpha, width = 0.35, 0.55
        else:
            self.beam.hide()
            return

        delta = target - origin
        length = delta.length()
        if length < 0.2:
            self.beam.hide()
            return

        self.beam.setPos(origin)
        self.beam.lookAt(target)
        # Mesh is unit length along +Y, so Y scale is the distance. Taper the width a
        # little with length so a long haul does not look like a slab.
        girth = max(0.35, 1.4 - length * 0.02) * width
        self.beam.setScale(girth, length, girth)
        # Tint the beam with the owner's team colour, so in a contested scrum you can see
        # whose beam is on a rock without tracing it back to a hull. The mesh already
        # carries per-vertex colour and alpha, so this is a scale over the top: lifted
        # toward white so the beam still reads as light rather than as coloured plastic,
        # and the alpha term keeps "searching" a faint projection rather than a lock.
        r, g, b = self._beam_rgb
        self.beam.setColorScale(r, g, b, alpha)
        self.beam.show()

    def update_dust(self, own_ship: int) -> None:
        """Keep the dust box around the player. Cheap enough to run every frame."""
        node = self.nodes.get(own_ship)
        if node is not None:
            wrap_dust(self._dust, node.getPos())

    def update_camera(self, own_ship: int, dt: float) -> None:
        """Chase camera behind and above the player's ship."""
        node = self.nodes.get(own_ship)
        if node is None:
            return

        quat = node.getQuat()
        # Far enough back that the hull reads as a ship rather than filling the frame,
        # and high enough to see over it toward whatever is being hauled.
        back = quat.getForward() * -26.0
        up = quat.getUp() * 8.0
        desired = node.getPos() + back + up

        if self._cam_pos is None:
            self._cam_pos = desired
        else:
            # Exponential smoothing, frame-rate independent. The lag is deliberate: a
            # rigidly attached camera makes thrust and rotation impossible to feel.
            alpha = 1.0 - math.exp(-dt * 9.0)
            self._cam_pos = self._cam_pos + (desired - self._cam_pos) * alpha

        self.base.camera.setPos(self._cam_pos)
        self.base.camera.lookAt(node.getPos() + quat.getForward() * 8.0, quat.getUp())
