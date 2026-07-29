"""Headless check of chat routing: ALL reaches everyone, TEAM reaches only the crew.

The scoping rule is the one thing here that can be wrong in a way nobody notices until it
matters — a team message that leaks to the room is worse than no chat at all, and it would
look completely normal to whoever sent it.

Three clients, on two teams:

    go run ./server -teams 2 -teamsize 4 &
    python client/chattest.py

Exits non-zero if a message reaches the wrong people.
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
        print(f"  FAIL  {label}{('  - ' + detail) if detail else ''}")


def drain(clients, into) -> None:
    for name, c in clients.items():
        for say in c.take_chat():
            into[name].append(say)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="ws://localhost:8080/ws")
    args = ap.parse_args()

    # Team preferences are honoured while there is room, so this is a deterministic
    # two-on-one split rather than whatever the auto-assign happens to do.
    specs = [("alpha", 0), ("bravo", 0), ("rival", 1)]
    clients: dict[str, NetClient] = {}
    for name, team in specs:
        c = NetClient(args.url, name, team)
        c.start()
        if not c.wait_until_ready(timeout=5.0):
            print(f"FAIL: {name} could not connect: {c.error or 'timeout'}")
            print("Is the server running?  cd server && go run . -teams 2")
            return 1
        clients[name] = c

    teams = {n: clients[n].welcome.team for n, _ in specs}
    print("connected: " + ", ".join(f"{n} on team {teams[n]}" for n, _ in specs))
    if teams["alpha"] != teams["bravo"] or teams["rival"] == teams["alpha"]:
        print(f"FAIL: needed two crews, got {teams}")
        print("Run the server with -teams 2 -teamsize 4.")
        for c in clients.values():
            c.stop()
        return 1

    inbox: dict[str, list] = {n: [] for n in clients}
    time.sleep(0.5)
    drain(clients, inbox)  # discard anything from before the test
    for n in inbox:
        inbox[n].clear()

    print()
    clients["alpha"].say(proto.CHAT_ALL, "hello everyone")
    time.sleep(0.8)
    drain(clients, inbox)

    got = {n: [s.text for s in inbox[n]] for n in inbox}
    check(all("hello everyone" in got[n] for n in got),
          "an ALL message reaches every client", f"{got}")

    for n in inbox:
        inbox[n].clear()

    clients["alpha"].say(proto.CHAT_TEAM, "just us")
    time.sleep(0.8)
    drain(clients, inbox)

    got = {n: [s.text for s in inbox[n]] for n in inbox}
    check("just us" in got["alpha"], "a TEAM message reaches the sender", f"{got}")
    check("just us" in got["bravo"], "a TEAM message reaches a crewmate", f"{got}")
    check("just us" not in got["rival"],
          "a TEAM message does NOT reach the other crew", f"rival saw {got['rival']}")

    # The server names the sender, not the message, so a client cannot speak as someone
    # else or claim a crew it is not on.
    team_says = [s for s in inbox["bravo"] if s.text == "just us"]
    if team_says:
        s = team_says[0]
        check(s.name == "alpha", "the server attributes the message", f"name={s.name!r}")
        check(s.team == teams["alpha"], "the sender's team is on the message",
              f"team={s.team}")
        check(s.channel == proto.CHAT_TEAM, "the channel survives the trip")

    for c in clients.values():
        c.stop()

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
