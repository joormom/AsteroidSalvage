"""Headless end-to-end check of the Python client against the real Go server.

Protocol drift between server/net/protocol.go and shared/asteroid_protocol.py is the
most likely bug in this project, and it is invisible until something renders wrong. This
exercises the whole loop with no window: connect, receive snapshots, fly, grab, and
confirm the world responds.

    go run ./server &          # or: go run . from the server directory
    python client/smoke_test.py

Exits non-zero on failure, so it is usable as a gate.
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

FAILURES: list[str] = []


def check(condition: bool, label: str, detail: str = "") -> bool:
    if condition:
        print(f"  PASS  {label}")
        return True
    FAILURES.append(label)
    print(f"  FAIL  {label}" + (f"  ({detail})" if detail else ""))
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=6.0)
    args = ap.parse_args()

    print(f"connecting to {args.url}")
    client = NetClient(args.url, "smoke", 0)
    client.start()

    if not client.wait_until_ready(timeout=5.0):
        print(f"  FAIL  could not connect: {client.error or 'timeout'}")
        print("\nIs the server running?  cd server && go run .")
        return 1

    welcome = client.welcome
    print(f"  connected: player {welcome.player_id}, team {welcome.team}, ship {welcome.ship}")

    check(welcome.ship != 0, "welcome carries a ship entity")
    check(welcome.tick_rate == 30, "tick rate is 30 Hz", f"got {welcome.tick_rate}")

    interp = Interpolator()
    snapshots = 0
    kinds_seen: set[int] = set()
    events: list[proto.Event] = []
    first_pos = None
    last_pos = None
    max_radius = 0.0
    grabbed_any = False

    deadline = time.monotonic() + args.seconds
    last = time.monotonic()

    while time.monotonic() < deadline:
        now = time.monotonic()
        dt = now - last
        last = now

        for snap in client.take_snapshots():
            interp.add(snap)
            snapshots += 1

        for ev in client.take_events():
            events.append(ev)
            if ev.type == proto.EVENT_GRABBED:
                grabbed_any = True

        interp.advance(dt)
        for b in interp.bodies():
            kinds_seen.add(b.kind)
            max_radius = max(max_radius, b.radius)
            if b.entity == welcome.ship:
                if first_pos is None:
                    first_pos = b.pos
                last_pos = b.pos

        # Fly forward with grab held: the belt is dense enough that this eventually
        # runs into something worth picking up.
        client.send_input(thrust_fwd=1.0, grab=True)
        time.sleep(1.0 / 30.0)

    print(f"\n  {snapshots} snapshots, {len(events)} events, kinds seen: {sorted(kinds_seen)}")

    check(snapshots > 20, "snapshots arriving", f"got {snapshots}")
    check(proto.KIND_SHIP in kinds_seen, "own ship present in snapshots")
    check(proto.KIND_MOTHERSHIP in kinds_seen, "mothership present in snapshots")
    check(proto.KIND_ASTEROID in kinds_seen, "asteroid field present in snapshots")

    # Radius decoding is the thing under test here, not the balance table: a misaligned
    # field shows up as a nonsense number immediately.
    #
    # The upper bound used to be 64 m, which was the old one-byte wire cap and stopped
    # meaning anything once the field started carrying 60 m titans and 190 m derelicts.
    # It is now sized off the largest structure the server can generate, with headroom —
    # tight enough that a decode error is still obvious, loose enough that growing the
    # scenery again is not a test failure.
    check(
        20.0 < max_radius < 400.0,
        "radius decodes to a sane range",
        f"max radius {max_radius:.2f} m",
    )

    if first_pos and last_pos:
        moved = math.dist(
            (first_pos.x, first_pos.y, first_pos.z), (last_pos.x, last_pos.y, last_pos.z)
        )
        check(moved > 1.0, "ship moved under thrust", f"moved {moved:.2f} m")
    else:
        check(False, "ship visible in snapshots")

    check(client.error is None, "no connection errors", str(client.error))

    if grabbed_any:
        print("  note: also grabbed an asteroid during the run")

    client.stop()

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
