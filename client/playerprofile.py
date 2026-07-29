"""Player profile: chosen loadout, unlocked achievements and lifetime stats.

Saved next to the game's log in the user's app data rather than beside the executable —
a shared zip may land in Program Files or a read-only folder, and losing someone's
unlocks because the install directory was not writable would be miserable.

Stats are tracked client-side from game events. That is trust-on-the-client, which for
cosmetic unlocks in a game played with friends is the right trade: it needs no protocol
changes and nothing here affects anyone else's match.
"""

from __future__ import annotations

import json
import os
import sys

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

import cosmetics  # noqa: E402

APP_NAME = "AsteroidSalvage"

# Stat keys the achievements read. Kept flat and additive so a profile written by an
# older build simply misses newer keys rather than failing to load.
STAT_KEYS = [
    "deliveries",
    "crystals",
    "golds",
    "massives",
    "credits",
    "pristine",
    "round_wins",
    "match_wins",
]


def profile_dir() -> str:
    base = os.environ.get("LOCALAPPDATA") or os.path.expanduser("~")
    return os.path.join(base, APP_NAME)


def profile_path() -> str:
    return os.path.join(profile_dir(), "profile.json")


class Profile:
    def __init__(self) -> None:
        self.loadout: dict[str, str] = cosmetics.default_loadout()
        self.unlocked: set[str] = set()
        self.stats: dict[str, float] = {k: 0 for k in STAT_KEYS}
        self.newly_unlocked: list[cosmetics.Achievement] = []

    # --- persistence -------------------------------------------------------

    @classmethod
    def load(cls) -> "Profile":
        p = cls()
        try:
            with open(profile_path(), "r", encoding="utf-8") as f:
                data = json.load(f)
        except (OSError, ValueError):
            # No profile yet, or a corrupt one. Starting fresh beats refusing to run.
            return p

        loadout = data.get("loadout", {})
        for slot in cosmetics.SLOTS:
            if slot in loadout:
                # Validate through cosmetics.get so an unknown id degrades to the
                # slot default instead of rendering nothing.
                p.loadout[slot] = cosmetics.get(slot, loadout[slot]).id

        p.unlocked = {a for a in data.get("unlocked", []) if a in cosmetics.ACHIEVEMENTS_BY_ID}
        for k in STAT_KEYS:
            p.stats[k] = data.get("stats", {}).get(k, 0)
        return p

    def save(self) -> bool:
        try:
            os.makedirs(profile_dir(), exist_ok=True)
            tmp = profile_path() + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump(
                    {
                        "loadout": self.loadout,
                        "unlocked": sorted(self.unlocked),
                        "stats": self.stats,
                    },
                    f,
                    indent=2,
                )
            # Replace atomically so a crash mid-write cannot leave a truncated profile.
            os.replace(tmp, profile_path())
            return True
        except OSError:
            return False

    # --- stats and unlocking ----------------------------------------------

    def add_stat(self, key: str, amount: float = 1) -> None:
        if key in self.stats:
            self.stats[key] += amount
            self._check_unlocks()

    def _check_unlocks(self) -> None:
        for ach in cosmetics.ACHIEVEMENTS:
            if ach.id in self.unlocked:
                continue
            if self.stats.get(ach.stat, 0) >= ach.target:
                self.unlocked.add(ach.id)
                self.newly_unlocked.append(ach)

    def take_new_unlocks(self) -> list[cosmetics.Achievement]:
        """Drain achievements unlocked since the last call, for on-screen toasts."""
        out = self.newly_unlocked
        self.newly_unlocked = []
        return out

    def progress(self, ach: cosmetics.Achievement) -> tuple[float, int]:
        return self.stats.get(ach.stat, 0), ach.target

    # --- loadout -----------------------------------------------------------

    def equipped(self, slot: str) -> cosmetics.Cosmetic:
        return cosmetics.get(slot, self.loadout.get(slot, ""))

    def equip(self, slot: str, cosmetic_id: str) -> bool:
        """Equip an item if it is unlocked. Returns whether it took."""
        item = cosmetics.get(slot, cosmetic_id)
        if not cosmetics.is_unlocked(item, self.unlocked):
            return False
        self.loadout[slot] = item.id
        self.save()
        return True

    def is_unlocked(self, item: cosmetics.Cosmetic) -> bool:
        return cosmetics.is_unlocked(item, self.unlocked)
