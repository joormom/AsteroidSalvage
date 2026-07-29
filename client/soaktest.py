"""Hold a connection open and report exactly when and why it drops.

Written to chase a report of disconnects around the 30 second mark. Short tests never
caught it, and the Go load-test bots are immune because they answer pings explicitly —
so this exercises the Python client the way a player actually does: connected, sending
input, doing nothing clever, for minutes.

    python client/soaktest.py --seconds 180
"""

from __future__ import annotations

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

from net import NetClient  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    ap.add_argument("--seconds", type=float, default=180.0)
    args = ap.parse_args()

    c = NetClient(args.url, "soak", 0xFF)
    c.start()
    if not c.wait_until_ready(timeout=5.0):
        print(f"FAIL: could not connect: {c.error or 'timeout'}")
        return 1

    start = time.monotonic()
    print(f"connected as player {c.welcome.player_id}; holding for {args.seconds:.0f}s")

    seq = 0
    last_report = start
    snapshots = 0

    while time.monotonic() - start < args.seconds:
        snapshots += len(c.take_snapshots())
        c.take_events()

        seq += 1
        c.send_input(thrust_fwd=0.2)

        now = time.monotonic()
        if c.error is not None:
            elapsed = now - start
            print(f"\nDISCONNECTED after {elapsed:.1f}s")
            print(f"  reason    : {c.error}")
            print(f"  snapshots : {snapshots}")
            return 1

        if now - last_report >= 15:
            print(f"  {now - start:6.1f}s  still connected  ({snapshots} snapshots)")
            last_report = now

        time.sleep(1 / 30)

    c.stop()
    print(f"\nheld {args.seconds:.0f}s with no disconnect ({snapshots} snapshots)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
