"""Headless check of the cargo-box loop: fly through a box, get the item, spend it.

The whole feature end to end, with no human and no shortcuts — real flight to a real
pickup, the real 0x08 UseItem, and the effect read back out of PlayerState. It uses the
same body-relative steering a player performs with the mouse.

    go run ./server &
    python client/itemtest.py

Exits non-zero if a box is never collected or an item never takes effect.
"""

from __future__ import annotations

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
from interp import Interpolator  # noqa: E402
from net import NetClient  # noqa: E402
from playtest import steer_toward  # noqa: E402

FAILURES: list[str] = []


def check(cond: bool, label: str, detail: str = "") -> None:
    if cond:
        print(f"  PASS  {label}")
    else:
        FAILURES.append(label)
        print(f"  FAIL  {label}{('  - ' + detail) if detail else ''}")


def nearest_pickup(bodies, pos):
    best, best_d = None, 1e18
    for b in bodies:
        if b.kind != proto.KIND_PICKUP:
            continue
        d = (b.pos.x - pos.x) ** 2 + (b.pos.y - pos.y) ** 2 + (b.pos.z - pos.z) ** 2
        if d < best_d:
            best, best_d = b, d
    return best, best_d ** 0.5


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=120.0)
    args = ap.parse_args()

    client = NetClient(args.url, "itemtest", 0)
    client.start()
    if not client.wait_until_ready(timeout=5.0):
        print(f"FAIL: could not connect: {client.error or 'timeout'}")
        print("Is the server running?  cd server && go run .")
        return 1

    w = client.welcome
    print(f"connected as player {w.player_id}, ship {w.ship}\n")

    interp = Interpolator()
    deadline = time.monotonic() + args.seconds

    saw_pickup_body = False
    picked_up = None       # the ITEM_* collected
    used = None            # the ITEM_* spent
    effect_seen = False
    closest = 1e18
    used_at = None
    last = time.monotonic()

    while time.monotonic() < deadline:
        now = time.monotonic()
        dt = now - last
        last = now

        for snap in client.take_snapshots():
            interp.add(snap)

        for ev in client.take_events():
            if ev.type == proto.EVENT_ITEM_PICKED_UP and ev.player == w.player_id:
                if picked_up is None:
                    name = proto.ITEM_INFO.get(int(ev.value), ("?",))[0]
                    print(f"  collected {name}")
                picked_up = int(ev.value)
            elif ev.type == proto.EVENT_ITEM_USED and ev.player == w.player_id:
                used = int(ev.value)

        interp.advance(dt)
        bodies = interp.bodies()
        me = next((b for b in bodies if b.entity == w.ship), None)
        if me is None:
            time.sleep(1 / 60)
            continue

        box, dist = nearest_pickup(bodies, me.pos)
        if box is not None:
            saw_pickup_body = True
            closest = min(closest, dist)

        player = client.player
        held = (player is not None
                and any(i != proto.ITEM_NONE for i in player.items))

        # Checked outside the "still holding something" branch on purpose: using an item
        # empties the slot, so gating the check on a full inventory meant it only ran if
        # another box happened to be collected first.
        if used is not None and used_at and time.monotonic() - used_at > 0.5:
            client.send_input(brake=True)
            if player is not None and (player.item_damage > 0 or player.item_speed > 0
                                       or player.item_shield > 0
                                       or used == proto.ITEM_MISSILE):
                effect_seen = True
                break

        # Once something is in a slot, stop flying and spend it.
        if held and used is None:
            slot = next(i for i, it in enumerate(player.items) if it != proto.ITEM_NONE)
            client.send_input(brake=True)
            client.use_item(slot)
            used_at = time.monotonic()
        elif box is not None:
            cmd, _ = steer_toward(me.pos, me.rot, box.pos)
            # Ease off on the approach. steer_toward runs the engine flat out whenever the
            # target is ahead, which sails straight past an 8 m bubble and then has to come
            # all the way back around — the same mistake a player makes on their first one.
            if dist < 70:
                cmd["thrust_fwd"] = 0.30
            if dist < 25:
                cmd["thrust_fwd"] = 0.12
            client.send_input(**cmd)
        else:
            client.send_input()

        time.sleep(1 / 30)

    print()
    check(saw_pickup_body, "cargo boxes appear in snapshots",
          "no KIND_PICKUP body was ever received")
    check(picked_up is not None, "flew through a box and collected it",
          f"closest approach {closest:.0f} m")
    check(used is not None, "the item was spent")
    name = proto.ITEM_INFO.get(used or 0, ("nothing",))[0]
    check(effect_seen, f"using {name} had an effect the server reported back")

    print()
    print(f"closest approach : {closest:.0f} m")
    print(f"collected        : {proto.ITEM_INFO.get(picked_up or 0, ('none',))[0]}")

    client.stop()
    if FAILURES:
        print(f"\nFAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("\nALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
