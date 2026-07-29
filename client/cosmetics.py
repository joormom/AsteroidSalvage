"""Ship cosmetics and the achievements that unlock them.

Purely visual — nothing here touches physics or scoring, so a fully-decked ship has
exactly the same handling as a stock one. Cosmetics are a reward for playing, not an
advantage.

Each slot has one always-available option so a new player is never staring at a locked
screen, and every locked item names the achievement that opens it.

Currently cosmetics are local to your own ship: other players see a stock hull. Putting
them on the wire would mean a loadout field in the snapshot and is deliberately left for
later — it changes the protocol, and the value is mostly in seeing your own ship.
"""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Achievement:
    id: str
    name: str
    description: str
    # stat key and threshold that unlocks it; see profile.Stats.
    stat: str
    target: int


ACHIEVEMENTS: list[Achievement] = [
    Achievement("first_haul", "First Haul", "Deliver your first asteroid", "deliveries", 1),
    Achievement("hauler_10", "Salvager", "Deliver 10 asteroids", "deliveries", 10),
    Achievement("hauler_50", "Veteran Hauler", "Deliver 50 asteroids", "deliveries", 50),
    Achievement("crystal_5", "Prospector", "Deliver 5 crystal asteroids", "crystals", 5),
    Achievement("gold_5", "Bank Job", "Deliver 5 gold asteroids", "golds", 5),
    Achievement("massive_1", "Heavy Lifting", "Deliver a MASSIVE asteroid", "massives", 1),
    Achievement("rich_10k", "Ten Grand", "Bank 10,000 credits in total", "credits", 10000),
    Achievement("round_win", "Round Winner", "Win a round", "round_wins", 1),
    Achievement("match_win", "Champion", "Win a match", "match_wins", 1),
    Achievement("careful", "Careful Hands", "Deliver 10 asteroids at full value", "pristine", 10),
]

ACHIEVEMENTS_BY_ID = {a.id: a for a in ACHIEVEMENTS}


@dataclass(frozen=True)
class Cosmetic:
    id: str
    name: str
    unlock: str | None = None  # achievement id, or None if always available
    # Slot-specific styling data, read by shipmodel / hudgeom.
    data: dict = field(default_factory=dict)


# --- boosters: the engine pods at the back ---------------------------------
BOOSTERS: list[Cosmetic] = [
    Cosmetic("twin", "Twin Pods", None, {"count": 2, "scale": 1.0}),
    Cosmetic("single", "Single Core", None, {"count": 1, "scale": 1.35}),
    Cosmetic("quad", "Quad Cluster", "hauler_10", {"count": 4, "scale": 0.72}),
    Cosmetic("heavy", "Heavy Thrusters", "massive_1", {"count": 2, "scale": 1.45}),
    Cosmetic("ion", "Ion Spires", "hauler_50", {"count": 2, "scale": 0.8, "long": True}),
]

# --- hats: sit on the pilot's helmet ---------------------------------------
HATS: list[Cosmetic] = [
    Cosmetic("none", "None", None, {}),
    Cosmetic("cap", "Flight Cap", None, {"shape": "cap", "color": (0.85, 0.25, 0.25)}),
    Cosmetic("cone", "Party Cone", "first_haul", {"shape": "cone", "color": (1.0, 0.45, 0.75)}),
    Cosmetic("top", "Top Hat", "rich_10k", {"shape": "top", "color": (0.12, 0.12, 0.16)}),
    Cosmetic("crown", "Crown", "match_win", {"shape": "crown", "color": (1.0, 0.82, 0.2)}),
    Cosmetic("halo", "Halo", "careful", {"shape": "halo", "color": (1.0, 0.95, 0.55)}),
]

# --- exhaust: the engine plume ---------------------------------------------
EXHAUSTS: list[Cosmetic] = [
    Cosmetic("amber", "Amber", None, {"hot": (1.0, 0.80, 0.35), "cool": (1.0, 0.35, 0.10)}),
    Cosmetic("blue", "Blue Flame", None, {"hot": (0.6, 0.85, 1.0), "cool": (0.15, 0.35, 1.0)}),
    Cosmetic("green", "Toxic", "crystal_5", {"hot": (0.7, 1.0, 0.5), "cool": (0.1, 0.7, 0.2)}),
    Cosmetic("violet", "Void", "gold_5", {"hot": (0.85, 0.6, 1.0), "cool": (0.45, 0.1, 0.8)}),
    Cosmetic("white", "Plasma", "hauler_50", {"hot": (1.0, 1.0, 1.0), "cool": (0.6, 0.8, 1.0)}),
]

# --- tractor beam ----------------------------------------------------------
BEAMS: list[Cosmetic] = [
    Cosmetic("cyan", "Cyan", None, {"core": (0.55, 1.00, 0.85), "edge": (0.20, 0.85, 1.00)}),
    Cosmetic("amber", "Amber", None, {"core": (1.0, 0.85, 0.5), "edge": (1.0, 0.5, 0.15)}),
    Cosmetic("green", "Emerald", "round_win", {"core": (0.6, 1.0, 0.6), "edge": (0.15, 0.8, 0.35)}),
    Cosmetic("violet", "Amethyst", "rich_10k", {"core": (0.85, 0.65, 1.0), "edge": (0.5, 0.2, 0.9)}),
    Cosmetic("gold", "Midas", "match_win", {"core": (1.0, 0.95, 0.6), "edge": (0.9, 0.65, 0.1)}),
]

SLOTS = {
    "booster": BOOSTERS,
    "hat": HATS,
    "exhaust": EXHAUSTS,
    "beam": BEAMS,
}

SLOT_LABELS = {
    "booster": "Boosters",
    "hat": "Hat",
    "exhaust": "Booster effect",
    "beam": "Tractor beam",
}

SLOT_ORDER = ["booster", "hat", "exhaust", "beam"]


def default_loadout() -> dict[str, str]:
    return {slot: options[0].id for slot, options in SLOTS.items()}


def get(slot: str, cosmetic_id: str) -> Cosmetic:
    """Look up a cosmetic, falling back to the slot's default.

    Falling back rather than raising matters: a profile written by a later build may name
    something this build has never heard of, and that should not stop the game starting.
    """
    options = SLOTS[slot]
    for c in options:
        if c.id == cosmetic_id:
            return c
    return options[0]


def is_unlocked(cosmetic: Cosmetic, unlocked: set[str]) -> bool:
    return cosmetic.unlock is None or cosmetic.unlock in unlocked


def unlock_hint(cosmetic: Cosmetic) -> str:
    if cosmetic.unlock is None:
        return ""
    ach = ACHIEVEMENTS_BY_ID.get(cosmetic.unlock)
    return ach.description if ach else "locked"
