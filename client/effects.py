"""Transient visual effects: laser bolts, impact flashes and explosions.

Everything here is fire-and-forget. Callers add an effect and never hold a reference;
`update` ages them and removes the nodes when their lifetime runs out. Nothing in this
module affects the simulation — the server has already decided what happened, and these
are only the pictures of it.

Effects are pooled by reusing one prototype mesh per kind and copying it, because a
firefight can spawn dozens of shards in a frame and rebuilding geometry that often is
the one thing that would actually cost frames here.
"""

from __future__ import annotations

import math
import os
import random
import sys

from panda3d.core import NodePath, Vec3, Vec4

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

from hudgeom import make_laser_bolt, make_shard  # noqa: E402

# How long a hitscan beam stays on screen. Long enough to register at 60 fps, short
# enough that rapid fire reads as separate shots rather than a continuous beam.
BOLT_LIFETIME = 0.12

# Impact flash: a bright shell that expands and fades.
FLASH_LIFETIME = 0.28

SHARD_LIFETIME = (0.9, 1.8)


class Effects:
    def __init__(self, base, parent: NodePath, sphere: NodePath):
        self.base = base
        self.parent = parent
        # The renderer already builds an icosphere; reuse it rather than making another.
        self._sphere = sphere

        # One bolt prototype per team colour, built lazily.
        self._bolt_protos: dict[tuple, NodePath] = {}
        self._shard_protos: dict[tuple, NodePath] = {}

        # Each entry: (node, age, lifetime, velocity, spin, start_scale, end_scale, fade)
        self._live: list[list] = []

    # --- construction ------------------------------------------------------

    def _bolt(self, rgb) -> NodePath:
        key = tuple(round(c, 3) for c in rgb)
        proto = self._bolt_protos.get(key)
        if proto is None:
            proto = make_laser_bolt(rgb=rgb)
            self._bolt_protos[key] = proto
        return proto

    def _shard_proto(self, rgb) -> NodePath:
        key = tuple(round(c, 3) for c in rgb)
        proto = self._shard_protos.get(key)
        if proto is None:
            proto = make_shard(rgb=rgb)
            self._shard_protos[key] = proto
        return proto

    def _track(self, node, lifetime, vel=None, spin=None, start=1.0, end=1.0, fade=True):
        self._live.append([node, 0.0, lifetime, vel or Vec3(0, 0, 0),
                           spin or Vec3(0, 0, 0), start, end, fade])

    @property
    def bolts_live(self) -> int:
        """How many laser beams are currently on screen. Used by the capture modes to
        wait for a frame that actually shows one — a bolt lasts ~7 frames out of every
        12, so a fixed frame number lands on an empty sky about half the time."""
        return sum(1 for e in self._live if e[2] == BOLT_LIFETIME)

    # --- effects -----------------------------------------------------------

    def add_bolt(self, origin: Vec3, direction: Vec3, length: float, rgb) -> None:
        """A laser beam from origin, `length` metres along `direction`."""
        if length < 0.5:
            return
        node = self._bolt(rgb).copyTo(self.parent)
        node.setPos(origin)
        node.lookAt(origin + direction * length)
        # Mesh is unit length along +Y; a fixed narrow girth keeps even a map-crossing
        # shot looking like a beam rather than a wedge. Deliberately not scaled with
        # length — a beam that got fatter the further it went would read as a cone.
        node.setScale(0.30, length, 0.30)
        self._track(node, BOLT_LIFETIME)

    def add_flash(self, pos: Vec3, size: float, rgb) -> None:
        """A bright shell that swells and fades — an impact, or a rock breaking."""
        node = self._sphere.copyTo(self.parent)
        node.setPos(pos)
        node.setColor(Vec4(rgb[0], rgb[1], rgb[2], 1.0))
        node.setLightOff()
        node.setDepthWrite(False)
        node.setTransparency(True)
        node.setBin("transparent", 32)
        self._track(node, FLASH_LIFETIME, start=size * 0.3, end=size * 1.9)

    def add_debris(self, pos: Vec3, size: float, rgb, count: int = 10,
                   speed: float = 14.0) -> None:
        """A burst of tumbling shards thrown out from a point."""
        proto = self._shard_proto(rgb)
        for _ in range(count):
            # Uniform on a sphere; naive angle sampling clumps at the poles.
            z = random.uniform(-1.0, 1.0)
            theta = random.uniform(0.0, 2.0 * math.pi)
            r = math.sqrt(max(0.0, 1.0 - z * z))
            direction = Vec3(r * math.cos(theta), r * math.sin(theta), z)

            node = proto.copyTo(self.parent)
            node.setPos(pos + direction * (size * 0.4))
            node.setHpr(random.uniform(0, 360), random.uniform(0, 360), 0)
            scale = size * random.uniform(0.18, 0.42)
            node.setScale(scale)

            vel = direction * (speed * random.uniform(0.5, 1.5))
            spin = Vec3(random.uniform(-260, 260), random.uniform(-260, 260),
                        random.uniform(-260, 260))
            node.setTransparency(True)
            self._track(node, random.uniform(*SHARD_LIFETIME), vel=vel, spin=spin,
                        start=scale, end=scale * 0.6)

    def add_explosion(self, pos: Vec3, size: float, rgb, shards: int = 14) -> None:
        """Flash plus debris — the standard 'that thing is gone' effect."""
        self.add_flash(pos, size, rgb)
        self.add_debris(pos, size, rgb, count=shards, speed=size * 2.2)

    # --- per-frame ---------------------------------------------------------

    def update(self, dt: float) -> None:
        survivors = []
        for entry in self._live:
            node, age, lifetime, vel, spin, start, end, fade = entry
            age += dt
            if age >= lifetime:
                node.removeNode()
                continue

            t = age / lifetime
            if vel.lengthSquared() > 0:
                node.setPos(node.getPos() + vel * dt)
            if spin.lengthSquared() > 0:
                node.setHpr(node.getHpr() + spin * dt)
            if start != end:
                node.setScale(start + (end - start) * t)
            if fade:
                # Ease out: effects should be at full strength when they register and
                # vanish quickly, not linger at half brightness.
                node.setColorScale(1.0, 1.0, 1.0, max(0.0, 1.0 - t * t))

            entry[1] = age
            survivors.append(entry)
        self._live = survivors

    def clear(self) -> None:
        for entry in self._live:
            entry[0].removeNode()
        self._live = []
