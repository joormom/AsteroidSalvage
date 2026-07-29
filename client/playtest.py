"""Headless playtest: fly to a rock, grab it, haul it home, confirm it banks.

This is the loop the game is actually about, driven end to end without a human. It uses
the same orientation-aware steering a player performs with the mouse, so it exercises
the real control path — thrust and torque in the ship's own frame — rather than
teleporting anything.

    python client/playtest.py

Exits non-zero if the haul never completes.
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
from interp import Interpolator  # noqa: E402
from net import NetClient  # noqa: E402


def quat_conjugate(q):
    return proto.Quat(-q.x, -q.y, -q.z, q.w)


def quat_rotate(q, v):
    """Rotate vector v by quaternion q (same expansion as the server's Quat.Rotate)."""
    ux, uy, uz = q.x, q.y, q.z
    s = q.w
    dot_uv = ux * v[0] + uy * v[1] + uz * v[2]
    dot_uu = ux * ux + uy * uy + uz * uz
    cx = uy * v[2] - uz * v[1]
    cy = uz * v[0] - ux * v[2]
    cz = ux * v[1] - uy * v[0]
    k = s * s - dot_uu
    return (
        2 * dot_uv * ux + k * v[0] + 2 * s * cx,
        2 * dot_uv * uy + k * v[1] + 2 * s * cy,
        2 * dot_uv * uz + k * v[2] + 2 * s * cz,
    )


def steer_toward(ship_pos, ship_rot, goal):
    """Body-relative steering, exactly as the mouse produces it."""
    d = (goal.x - ship_pos.x, goal.y - ship_pos.y, goal.z - ship_pos.z)
    dist = math.sqrt(sum(c * c for c in d))
    if dist < 1e-3:
        return {}, 0.0
    d = tuple(c / dist for c in d)

    lx, ly, lz = quat_rotate(quat_conjugate(ship_rot), d)

    def clamp(v):
        return max(-1.0, min(1.0, v))

    cmd = {"yaw": clamp(-lx * 2.5), "pitch": clamp(lz * 2.5)}
    cmd["thrust_fwd"] = 1.0 if ly > 0.5 else (-0.35 if ly < -0.2 else 0.0)
    return cmd, dist


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=90.0)
    args = ap.parse_args()

    client = NetClient(args.url, "playtest", 0)
    client.start()
    if not client.wait_until_ready(timeout=5.0):
        print(f"FAIL: could not connect: {client.error or 'timeout'}")
        print("Is the server running?  cd server && go run .")
        return 1

    w = client.welcome
    print(f"connected as player {w.player_id}, ship {w.ship}")

    interp = Interpolator()
    banked = 0.0
    grabbed = False
    phase = "hunting"
    deadline = time.monotonic() + args.seconds
    last = time.monotonic()
    min_home_dist = 1e9
    min_cargo_dist = 1e9
    max_tether = 0.0

    while time.monotonic() < deadline:
        now = time.monotonic()
        dt = now - last
        last = now

        for snap in client.take_snapshots():
            interp.add(snap)

        for ev in client.take_events():
            if ev.type == proto.EVENT_GRABBED:
                if not grabbed:
                    print("  grabbed a rock")
                grabbed = True
            elif ev.type == proto.EVENT_DROPPED:
                grabbed = False
                print("  lost it")
            elif ev.type == proto.EVENT_DEPOSITED:
                banked += ev.value
                grabbed = False
                print(f"  DELIVERED for {ev.value:,.0f} credits")

        interp.advance(dt)
        bodies = interp.bodies()

        ship = None
        mothership = None
        rocks = []
        for b in bodies:
            if b.entity == w.ship:
                ship = b
            elif b.kind == proto.KIND_MOTHERSHIP and b.team == w.team:
                # Your own station, not whichever one happens to come last in the
                # snapshot. Only your team's hangar banks anything (checkDeposits in
                # server/sim), so hauling to a rival's door delivers nothing and this
                # test failed with the cargo sitting well inside a capture radius —
                # someone else's. main.py's _scan documents the same trap.
                mothership = b
            elif b.kind in (proto.KIND_ASTEROID, proto.KIND_SALVAGE) and not b.held:
                rocks.append(b)

        if ship is None or mothership is None:
            time.sleep(1 / 30)
            continue

        if grabbed:
            phase = "hauling"
            goal = mothership.pos
        else:
            phase = "hunting"
            if not rocks:
                time.sleep(1 / 30)
                continue
            goal = min(
                rocks,
                key=lambda r: math.dist(
                    (ship.pos.x, ship.pos.y, ship.pos.z), (r.pos.x, r.pos.y, r.pos.z)
                ),
            ).pos

        cmd, _ = steer_toward(ship.pos, ship.rot, goal)
        cmd["grab"] = True
        client.send_input(**cmd)

        if grabbed:
            home = math.dist(
                (ship.pos.x, ship.pos.y, ship.pos.z),
                (mothership.pos.x, mothership.pos.y, mothership.pos.z),
            )
            min_home_dist = min(min_home_dist, home)

            # Deposit tests the CARGO's distance, not the ship's. With a long-range beam
            # the rock can still be trailing on a tether well behind the ship, so these
            # two numbers can differ a lot — which is exactly the failure to look for.
            cargo = next((b for b in bodies if b.held), None)
            if cargo is not None:
                cd = math.dist(
                    (cargo.pos.x, cargo.pos.y, cargo.pos.z),
                    (mothership.pos.x, mothership.pos.y, mothership.pos.z),
                )
                min_cargo_dist = min(min_cargo_dist, cd)
                tether = math.dist(
                    (cargo.pos.x, cargo.pos.y, cargo.pos.z),
                    (ship.pos.x, ship.pos.y, ship.pos.z),
                )
                max_tether = max(max_tether, tether)

        if banked > 0:
            break

        time.sleep(1 / 30)

    client.stop()

    print()
    print(f"phase reached : {phase}")
    if min_home_dist < 1e9:
        print(f"ship  closest to mothership : {min_home_dist:,.0f} m")
        # Capture radius is sim.Config.DepositRadius; either the ship or the cargo
        # entering it is enough to bank.
        print(f"cargo closest to mothership : {min_cargo_dist:,.0f} m  (capture at 60 m)")
        print(f"longest tether ship->cargo  : {max_tether:,.0f} m")
    else:
        print("never hauled")
    print(f"banked        : {banked:,.0f} credits")

    if banked <= 0:
        print("\nFAIL: never completed a delivery")
        return 1
    print("\nPASS: grabbed an asteroid and delivered it to the mothership")
    return 0


if __name__ == "__main__":
    sys.exit(main())
