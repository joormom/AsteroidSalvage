"""End-to-end check of the laser layer against a live server.

Two clients on opposing teams. One flies at the other and holds the trigger; this
verifies the whole path: Fire input -> server hitscan -> Shots broadcast -> Python
decode, plus the energy bar draining and refilling in PlayerState.

    go run ./server -sandbox
    python client/combattest.py

Sandbox mode keeps one endless round running, so the test never has to wait out a
warmup or lose its shooting window to an intermission.
"""

from __future__ import annotations

import argparse
import math
import os
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
from net import NetClient  # noqa: E402
from playtest import quat_conjugate, quat_rotate, steer_toward  # noqa: E402


def aim_dot(ship_pos, ship_rot, goal) -> float:
    """How closely the ship's nose points at a target: 1 is dead on.

    Mirrors the server's own forward axis so "am I lined up" means the same thing on
    both sides, which is what makes `fire` meaningful rather than hopeful.
    """
    d = (goal.x - ship_pos.x, goal.y - ship_pos.y, goal.z - ship_pos.z)
    dist = math.sqrt(sum(c * c for c in d))
    if dist < 1e-3:
        return 0.0
    d = tuple(c / dist for c in d)
    # +Y is forward; rotating the direction into ship space makes the y component the
    # cosine of the angle off the nose.
    _lx, ly, _lz = quat_rotate(quat_conjugate(ship_rot), d)
    return ly

FAILURES: list[str] = []


def check(cond: bool, label: str, detail: str = "") -> None:
    if cond:
        print(f"  PASS  {label}")
    else:
        FAILURES.append(label)
        print(f"  FAIL  {label}" + (f"  ({detail})" if detail else ""))


def body(bodies, entity):
    for b in bodies:
        if b.entity == entity:
            return b
    return None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=100.0)
    args = ap.parse_args()

    shooter = NetClient(args.url, "gunner", 0)
    target = NetClient(args.url, "victim", 1)
    for c in (shooter, target):
        c.start()
        if not c.wait_until_ready(timeout=5.0):
            print(f"FAIL: could not connect: {c.error or 'timeout'}")
            return 1

    print(f"shooter: player {shooter.welcome.player_id} team {shooter.welcome.team}")
    print(f"target:  player {target.welcome.player_id} team {target.welcome.team}")
    if shooter.welcome.team == target.welcome.team:
        print("FAIL: both clients landed on the same team; cannot test hostile fire")
        return 1

    shots_seen = 0
    bolts_seen = 0
    kills_seen = 0
    respawns_seen = 0
    teams_on_bodies: set[int] = set()
    min_energy = 1e9
    max_energy = 0.0
    saw_target_health_drop = False

    latest = []
    deadline = time.monotonic() + args.seconds

    while time.monotonic() < deadline:
        for snap in shooter.take_snapshots():
            latest = snap.bodies
        target.take_snapshots()

        for _shot in shooter.take_shots():
            shots_seen += 1
        target.take_shots()

        # Rounds travel now, so "did it connect" is not something a shot record can say
        # any more — it is answered by the damage check below. What this watches instead
        # is that bolts actually appear *in flight*, which is the new mechanic and the
        # thing that would silently break if the projectile layer stopped broadcasting.
        bolts_seen += sum(1 for b in latest if b.kind == proto.KIND_BOLT)

        for ev in shooter.take_events():
            if ev.type == proto.EVENT_SHIP_DESTROYED:
                kills_seen += 1
            elif ev.type == proto.EVENT_SHIP_RESPAWN:
                respawns_seen += 1
        target.take_events()

        ps = shooter.player
        if ps is not None:
            min_energy = min(min_energy, ps.energy)
            max_energy = max(max_energy, ps.energy)

        me = body(latest, shooter.welcome.ship)
        him = body(latest, target.welcome.ship)
        for b in latest:
            if b.kind in (proto.KIND_SHIP, proto.KIND_MOTHERSHIP):
                teams_on_bodies.add(b.team)
        if him is not None and him.health < 0.999:
            saw_target_health_drop = True

        # The target sits still and takes it; the shooter closes and fires.
        target.send_input(brake=True)

        if me is None or him is None:
            time.sleep(1 / 30)
            continue

        # Aim high by however far the round will fall on the way. Rounds travel and drop
        # now, so pointing straight at a target puts the shot under it — this is the same
        # correction a player makes, and testing without it would only prove that a flat
        # shot misses.
        flat = math.dist((me.pos.x, me.pos.y, me.pos.z), (him.pos.x, him.pos.y, him.pos.z))
        flight = flat / proto.BOLT_SPEED
        lifted = type(him.pos)(
            him.pos.x, him.pos.y, him.pos.z + 0.5 * proto.BOLT_DROP * flight * flight
        )

        cmd, dist = steer_toward(me.pos, me.rot, lifted)
        aim = aim_dot(me.pos, me.rot, lifted)

        # Hold well off the target. Ships are twice as fast as they were and a collision
        # above 20 m/s now destroys both of them — closing to 80 m meant the shooter rammed
        # its way to a kill and the burst was never fired at all.
        if dist < 130:
            cmd["thrust_fwd"] = 0.0
            cmd["brake"] = True
        elif dist > 300:
            cmd["boost"] = True  # the motherships are a kilometre apart; close the gap

        # Fire whenever lined up, at any distance. There is no range gate to respect any
        # more — a laser runs until it leaves the map — so this doubles as a check that
        # long shots actually connect.
        cmd["fire"] = aim > 0.99
        shooter.send_input(**cmd)

        time.sleep(1 / 30)

    for c in (shooter, target):
        c.stop()
    print()

    check(shots_seen > 0, "laser shots reached the client", f"{shots_seen} seen")
    check(bolts_seen > 0, "bolts seen travelling in flight", f"{bolts_seen} sightings")
    check(saw_target_health_drop, "target hull damage appeared in snapshots")
    check(kills_seen > 0, "a ship was destroyed", f"{kills_seen} kills")
    check(respawns_seen > 0, "the destroyed ship respawned", f"{respawns_seen} respawns")
    check(
        min_energy < 1.0,
        "the energy bar drained under sustained fire",
        f"low water mark {min_energy:.2f}",
    )
    check(max_energy > 1.0, "the energy bar carried a real charge",
          f"high water mark {max_energy:.2f}")
    check(
        len(teams_on_bodies - {proto.NO_TEAM}) >= 2,
        "bodies carry distinct team ids",
        f"teams seen: {sorted(teams_on_bodies)}",
    )

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
