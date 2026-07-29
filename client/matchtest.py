"""End-to-end check of the round/shop layer against a live server.

Watches the phase machine advance across the wire, and buys an upgrade during an
intermission — verifying the whole path: server phase change -> MatchState broadcast ->
Python decode -> BuyUpgrade -> PlayerState reflecting the purchase.

Run the server with short rounds so this finishes quickly:

    go run ./server -round 12 -intermission 8 -warmup 3
    python client/matchtest.py
"""

from __future__ import annotations

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402
from net import NetClient  # noqa: E402

FAILURES: list[str] = []


def check(cond: bool, label: str, detail: str = "") -> None:
    if cond:
        print(f"  PASS  {label}")
    else:
        FAILURES.append(label)
        print(f"  FAIL  {label}" + (f"  ({detail})" if detail else ""))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=60.0)
    args = ap.parse_args()

    c = NetClient(args.url, "matchtest", 0)
    c.start()
    if not c.wait_until_ready(timeout=5.0):
        print(f"FAIL: could not connect: {c.error or 'timeout'}")
        return 1
    print(f"connected as player {c.welcome.player_id}")

    # Exercise team naming on the way in.
    c.set_team_name("Rock Hounds")

    phases_seen: list[int] = []
    rounds_seen: set[int] = set()
    bought = False
    credits_before = None
    upgrade_level = 0

    deadline = time.monotonic() + args.seconds
    while time.monotonic() < deadline:
        c.take_snapshots()
        c.take_events()

        m = c.match
        p = c.player

        if m is not None:
            if not phases_seen or phases_seen[-1] != m.phase:
                name = proto.PHASE_NAMES.get(m.phase, "?")
                print(f"  phase -> {name} (round {m.round}, {m.time_left}s left)")
                phases_seen.append(m.phase)
            if m.phase == proto.PHASE_ROUND:
                rounds_seen.add(m.round)

            # Buy the first offered slot during the first intermission we see. Buying by
            # slot is the real path now — the server only accepts what it offered.
            if m.phase == proto.PHASE_INTERMISSION and not bought and p is not None:
                offers = c.offers
                if offers:
                    credits_before = p.credits
                    bought_upgrade = offers[0].upgrade
                    name = proto.UPGRADE_INFO.get(bought_upgrade, ("?",))[0]
                    print(
                        f"  {len(offers)} offers; buying slot 1 ({name}, "
                        f"{offers[0].cost:,.0f}cr) with {p.credits:,.0f} credits"
                    )
                    c.buy_offer(0)
                    bought = True

        if bought and p is not None and credits_before is not None:
            if p.credits < credits_before and upgrade_level == 0:
                upgrade_level = 1
                print(f"  purchase confirmed: {credits_before:,.0f} -> {p.credits:,.0f} credits")

        time.sleep(1 / 30)

    c.stop()
    print()

    check(proto.PHASE_ROUND in phases_seen, "reached an active round")
    check(
        proto.PHASE_INTERMISSION in phases_seen,
        "reached an intermission",
        f"phases seen: {[proto.PHASE_NAMES.get(x) for x in phases_seen]}",
    )
    check(len(rounds_seen) >= 2, "played more than one round", f"rounds: {sorted(rounds_seen)}")
    check(c.player is not None, "received personal player state")

    names = [t.name for t in c.teams]
    check("Rock Hounds" in names, "team name applied", f"teams: {names}")

    if bought and credits_before is not None and credits_before > 0:
        check(upgrade_level > 0, "offer purchase applied")

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
