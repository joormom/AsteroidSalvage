"""Snapshot buffering and interpolation.

The server sends world state 15 times a second. Rendering those positions directly would
look like a slideshow, so the client deliberately renders ~100 ms in the past and
interpolates between the two snapshots bracketing that moment. The cost is a small,
constant input-to-photon delay; the benefit is smooth motion that survives jitter and
the occasional dropped packet.

This is the single most important piece of the client. Without it the game looks broken
no matter how correct the server is.
"""

from __future__ import annotations

import os
import sys
from collections import deque

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

# How far behind the newest snapshot to render. Must exceed the snapshot interval
# (~67 ms at 15 Hz) with room to spare, or a single late packet leaves nothing to
# interpolate toward and the world stutters.
DEFAULT_DELAY_MS = 110.0

# Above this the buffer is stale enough that catching up smoothly is hopeless; snap
# instead. Happens after an alt-tab or a network stall.
RESYNC_THRESHOLD_MS = 750.0


class InterpolatedBody:
    __slots__ = ("entity", "kind", "flags", "tier", "radius", "team", "health",
                 "pos", "rot")

    def __init__(self, entity: int, kind: int, flags: int, tier: int, radius: float,
                 team: int, health: float, pos, rot):
        self.entity = entity
        self.kind = kind
        self.flags = flags
        self.tier = tier
        self.radius = radius
        self.team = team
        self.health = health
        self.pos = pos
        self.rot = rot

    @property
    def held(self) -> bool:
        return bool(self.flags & proto.BODY_HELD)

    @property
    def damaged(self) -> bool:
        return bool(self.flags & proto.BODY_DAMAGED)

    @property
    def dead(self) -> bool:
        return bool(self.flags & proto.BODY_DEAD)

    @property
    def boosting(self) -> bool:
        return bool(self.flags & proto.BODY_BOOSTING)


def _from_body(b, pos=None, rot=None) -> InterpolatedBody:
    """Copy a decoded body, optionally with a blended pose.

    Team never changes and health is a bar rather than a position, so neither is
    interpolated — both are taken as-is from whichever snapshot supplied them.
    """
    return InterpolatedBody(
        b.entity, b.kind, b.flags, b.tier, b.radius, b.team, b.health,
        b.pos if pos is None else pos,
        b.rot if rot is None else rot,
    )


def _lerp(a: float, b: float, t: float) -> float:
    return a + (b - a) * t


def _nlerp(qa: proto.Quat, qb: proto.Quat, t: float) -> proto.Quat:
    """Normalised lerp between two quaternions.

    Cheaper than slerp and visually indistinguishable at the small angular deltas
    between adjacent snapshots. Sign-corrects first so rotation takes the short way
    round — without that, a body can spin the long way between two frames.
    """
    dot = qa.x * qb.x + qa.y * qb.y + qa.z * qb.z + qa.w * qb.w
    if dot < 0.0:
        qb = proto.Quat(-qb.x, -qb.y, -qb.z, -qb.w)

    x = _lerp(qa.x, qb.x, t)
    y = _lerp(qa.y, qb.y, t)
    z = _lerp(qa.z, qb.z, t)
    w = _lerp(qa.w, qb.w, t)

    n = (x * x + y * y + z * z + w * w) ** 0.5
    if n < 1e-8:
        return proto.Quat(0.0, 0.0, 0.0, 1.0)
    return proto.Quat(x / n, y / n, z / n, w / n)


class Interpolator:
    def __init__(self, delay_ms: float = DEFAULT_DELAY_MS, buffer_size: int = 32):
        self.delay_ms = delay_ms
        self._buf: deque[proto.Snapshot] = deque(maxlen=buffer_size)
        self._render_ms: float | None = None
        self.resyncs = 0

    def add(self, snap: proto.Snapshot) -> None:
        # Out-of-order or duplicate snapshots are discarded; the newest wins.
        if self._buf and snap.server_ms <= self._buf[-1].server_ms:
            return
        self._buf.append(snap)

    def advance(self, dt: float) -> None:
        """Move the render clock forward by dt seconds."""
        if not self._buf:
            return

        newest = self._buf[-1].server_ms
        target = newest - self.delay_ms

        if self._render_ms is None:
            self._render_ms = target
            return

        self._render_ms += dt * 1000.0

        drift = target - self._render_ms
        if abs(drift) > RESYNC_THRESHOLD_MS:
            # Too far gone to hide; jump.
            self._render_ms = target
            self.resyncs += 1
        else:
            # Nudge toward the target so clock drift between server and client never
            # accumulates. 5% per frame is invisible but converges quickly.
            self._render_ms += drift * 0.05

    def bodies(self) -> list[InterpolatedBody]:
        """World state at the current render time."""
        if not self._buf or self._render_ms is None:
            return []

        older, newer = self._bracket(self._render_ms)
        if older is None:
            return []

        if newer is None or newer.server_ms <= older.server_ms:
            return [_from_body(b) for b in older.bodies]

        span = newer.server_ms - older.server_ms
        t = (self._render_ms - older.server_ms) / span
        t = 0.0 if t < 0.0 else (1.0 if t > 1.0 else t)

        newer_by_id = {b.entity: b for b in newer.bodies}

        out: list[InterpolatedBody] = []
        for a in older.bodies:
            b = newer_by_id.get(a.entity)
            if b is None:
                # Despawned between the two snapshots — hold its last known pose rather
                # than popping it out early.
                out.append(_from_body(a))
                continue

            pos = proto.Vec3(
                _lerp(a.pos.x, b.pos.x, t),
                _lerp(a.pos.y, b.pos.y, t),
                _lerp(a.pos.z, b.pos.z, t),
            )
            # Everything but the pose comes from the newer snapshot: "held", "damaged"
            # and health are states, not something to blend.
            out.append(_from_body(b, pos=pos, rot=_nlerp(a.rot, b.rot, t)))

        # Bodies that appeared in the newer snapshot only.
        older_ids = {a.entity for a in older.bodies}
        for b in newer.bodies:
            if b.entity not in older_ids:
                out.append(_from_body(b))

        return out

    def _bracket(self, t_ms: float):
        """Return the snapshots immediately before and after t_ms."""
        older = None
        for snap in self._buf:
            if snap.server_ms <= t_ms:
                older = snap
            else:
                return older, snap
        # Render clock has run past every snapshot; extrapolating would invent motion,
        # so hold the newest pose until fresh data arrives.
        return (older, None) if older is not None else (self._buf[0], None)

    @property
    def buffered(self) -> int:
        return len(self._buf)
