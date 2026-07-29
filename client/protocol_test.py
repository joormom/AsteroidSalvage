"""Checks that the Python decoders agree with the Go encoders, byte for byte.

    python client/protocol_test.py

Go and Python implement shared/protocol.md independently, and drift between them is the
most likely bug in this project. smoke_test.py catches it for the messages a running
match exchanges; these are the ones it cannot reach, because a match has to *end* before
0x88 is sent and a lobby has to be *opened* before 0x89 is.

The literals below were produced by the Go encoders. They are pinned rather than
regenerated, which is the point: if either side changes shape, this fails instead of both
sides quietly agreeing on something new. The mirror of this test — Python's bytes decoded
by Go — lives in server/net/results_test.go.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

import asteroid_protocol as proto  # noqa: E402

FAILURES: list[str] = []

# EncodeMatchResults(1, [joormom: 14 delivered / 2840.5 banked / 6 kills / 3 deaths /
# 1275 cr, alex: 3 / 512.25 / 1 / 4 / 90.5 cr])
GO_MATCH_RESULTS = (
    "8801020100000000076a6f6f726d6f6d0e00008831450600030000609f4402000000"
    "0104616c6578030000100044010004000000b542"
)

# EncodeRoster(host=1, capacity=4, [joormom on team 0, alex on team 1])
GO_ROSTER = "890100000004020100000000076a6f6f726d6f6d020000000104616c6578"

# EncodeChatSay(ChatTeam, team=2, "joormom", "on it")
GO_CHAT_SAY = "8a0102076a6f6f726d6f6d056f6e206974"


def check(cond: bool, label: str, detail: str = "") -> None:
    if cond:
        print(f"  PASS  {label}")
    else:
        FAILURES.append(label)
        print(f"  FAIL  {label}{('  - ' + detail) if detail else ''}")


def check_match_results() -> None:
    print("0x88 MatchResults")
    raw = bytes.fromhex(GO_MATCH_RESULTS)
    check(raw[0] == proto.MSG_MATCH_RESULTS, "tag is 0x88",
          f"got {raw[0]:#x}")

    r = proto.decode_match_results(raw[1:])
    check(r.winner == 1, "winner decodes", f"got {r.winner}")
    check(len(r.players) == 2, "row count", f"got {len(r.players)}")
    if len(r.players) != 2:
        return

    a, b = r.players
    check(a.player_id == 1 and a.team == 0 and a.name == "joormom",
          "row 0 identity", f"got {a.player_id}/{a.team}/{a.name!r}")
    check(a.delivered == 14 and a.kills == 6 and a.deaths == 3,
          "row 0 counts", f"got {a.delivered}/{a.kills}/{a.deaths}")
    check(a.banked == 2840.5 and a.credits == 1275.0,
          "row 0 money", f"got banked {a.banked} credits {a.credits}")

    # The second row is the one that proves the variable-width name did not desynchronise
    # the reader: everything after it is read at an offset the first row decided.
    check(b.player_id == 2 and b.team == 1 and b.name == "alex",
          "row 1 identity after a variable-width name",
          f"got {b.player_id}/{b.team}/{b.name!r}")
    check(b.delivered == 3 and b.kills == 1 and b.deaths == 4,
          "row 1 counts", f"got {b.delivered}/{b.kills}/{b.deaths}")
    check(b.banked == 512.25 and b.credits == 90.5,
          "row 1 money", f"got banked {b.banked} credits {b.credits}")


def check_roster() -> None:
    print("\n0x89 Roster")
    raw = bytes.fromhex(GO_ROSTER)
    check(raw[0] == proto.MSG_ROSTER, "tag is 0x89", f"got {raw[0]:#x}")

    r = proto.decode_roster(raw[1:])
    check(r.host_id == 1, "host id", f"got {r.host_id}")
    check(r.capacity == 4, "capacity", f"got {r.capacity}")
    check(len(r.players) == 2, "row count", f"got {len(r.players)}")
    if len(r.players) != 2:
        return

    check([p.name for p in r.players] == ["joormom", "alex"], "names in order",
          f"got {[p.name for p in r.players]}")
    check([p.team for p in r.players] == [0, 1], "teams",
          f"got {[p.team for p in r.players]}")
    check([p.player_id for p in r.by_team(1)] == [2], "by_team filters",
          f"got {[p.player_id for p in r.by_team(1)]}")


def check_chat() -> None:
    print("\n0x8A ChatSay")
    raw = bytes.fromhex(GO_CHAT_SAY)
    check(raw[0] == proto.MSG_CHAT_SAY, "tag is 0x8A", f"got {raw[0]:#x}")

    s = proto.decode_chat_say(raw[1:])
    check(s.channel == proto.CHAT_TEAM, "channel", f"got {s.channel}")
    check(s.team == 2, "sender team", f"got {s.team}")
    check(s.name == "joormom", "sender name", f"got {s.name!r}")
    # Two length-prefixed strings back to back: the text proves the name did not
    # desynchronise the reader.
    check(s.text == "on it", "text after a variable-width name", f"got {s.text!r}")

    print("\n0x09 Chat / 0x08 UseItem")
    check(proto.encode_chat(proto.CHAT_TEAM, "on it").hex() == "0901056f6e206974",
          "Chat encodes as Go decodes it",
          proto.encode_chat(proto.CHAT_TEAM, "on it").hex())
    check(proto.encode_use_item(2).hex() == "0802", "UseItem encodes to tag + slot",
          proto.encode_use_item(2).hex())


def check_start_match() -> None:
    print("\n0x07 StartMatch")
    raw = proto.encode_start_match()
    check(raw == bytes([0x07]), "encodes to a single tag byte", f"got {raw.hex()}")


def main() -> int:
    print("Python decoders vs Go encoders\n")
    check_match_results()
    check_roster()
    check_chat()
    check_start_match()

    print()
    if FAILURES:
        print(f"FAILED ({len(FAILURES)}): " + ", ".join(FAILURES))
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
