"""Python implementation of the Asteroid Salvage wire protocol.

shared/protocol.md is authoritative. This module and server/net/protocol.go implement it
independently, so any change belongs in that document first.

Both the game client and the dev console import this module, so the protocol has exactly
one Python implementation.
"""

from __future__ import annotations

import math
import struct
from dataclasses import dataclass, field
from typing import NamedTuple

# --- message tags -----------------------------------------------------------

MSG_INPUT = 0x01
MSG_HELLO = 0x02
MSG_BUY_UPGRADE = 0x03
MSG_BUY_OFFER = 0x04
MSG_SET_TEAM_NAME = 0x05
MSG_SET_TEAM_COLOR = 0x06
MSG_START_MATCH = 0x07
MSG_USE_ITEM = 0x08
MSG_CHAT = 0x09
MSG_SET_TEAM = 0x0A
MSG_DEBUG_SET = 0x10

MSG_WELCOME = 0x80
MSG_SNAPSHOT = 0x81
MSG_EVENT = 0x82
MSG_TEAM_STATE = 0x83
MSG_MATCH_STATE = 0x84
MSG_PLAYER_STATE = 0x85
MSG_SHOP_OFFERS = 0x86
MSG_SHOTS = 0x87
MSG_MATCH_RESULTS = 0x88
MSG_ROSTER = 0x89
MSG_CHAT_SAY = 0x8A
MSG_DEBUG_STATS = 0x90

# Chat channels.
CHAT_ALL = 0
CHAT_TEAM = 1

# Longest message the server will relay. Enforced there too — this is so the composer can
# stop accepting characters rather than silently truncating on send.
CHAT_MAX_BYTES = 120

# Match phases.
PHASE_WARMUP = 0
PHASE_ROUND = 1
PHASE_INTERMISSION = 2
PHASE_MATCH_OVER = 3
# Held open for players to join. Only reached when the server is run with -lobby, and it
# has no clock: it ends when the host starts the match and not otherwise.
PHASE_LOBBY = 4

# Game modes. Rules live in server/sim/modes.go; these are for display and for the host
# screen, which offers a different options panel per mode.
MODE_SALVAGE = 0
MODE_HOARD = 1
MODE_KOTH = 2

MODE_NAMES = {
    MODE_SALVAGE: "SALVAGE",
    MODE_HOARD: "HOARD",
    MODE_KOTH: "KING OF THE HILL",
}
# Short name for a command line, where "KING OF THE HILL" is not a word.
MODE_FLAGS = {MODE_SALVAGE: "salvage", MODE_HOARD: "hoard", MODE_KOTH: "koth"}
MODE_BLURBS = {
    MODE_SALVAGE: "Tractor rocks home and bank them at your station.",
    MODE_HOARD: "No hauling - fly into rocks to eat them. Most points wins.",
    MODE_KOTH: "Hold the marked zone. Contested by anyone means nobody scores.",
}

# The synthetic entity the King of the Hill zone is broadcast under. It is a volume you
# fly through rather than an object, so it has no body in the physics world.
HILL_ENTITY = 0xFFFFFF01

PHASE_NAMES = {
    PHASE_WARMUP: "WARMUP",
    PHASE_ROUND: "ROUND",
    PHASE_INTERMISSION: "INTERMISSION",
    PHASE_MATCH_OVER: "MATCH OVER",
}

# Upgrades. Costs and effects live in server/sim/upgrades.go; these are for display.
UPGRADE_TRACTOR = 0
UPGRADE_THRUST = 1
UPGRADE_RANGE = 2
UPGRADE_HULL = 3
UPGRADE_LASER = 4
UPGRADE_CAPACITOR = 5
UPGRADE_RECHARGER = 6

# Station lines. These count up on the team rather than the buyer — see Team.Station in
# server/sim/sim.go — so their level arrives in TeamState, not PlayerState.
UPGRADE_SHIELDS = 7
UPGRADE_TURRETS = 8

STATION_UPGRADES = (UPGRADE_SHIELDS, UPGRADE_TURRETS)

# Absolute health pools, for display only. Health travels as a 0-1 fraction, which is all
# the bars and damage tints need; these turn it back into the number the player is
# actually being told about ("18 / 30"). They mirror sim.ShipMaxHealth and
# sim.MothershipMaxHealth — if those move and these do not, the bars stay correct and only
# the labels lie.
# Bolt flight, mirroring sim.BoltSpeed and sim.BoltMaxRange. Rounds fly straight, so
# aiming is purely a matter of leading a moving target — there is no hold-over.
#
# There was a BOLT_DROP here, and it read 36.0 against the server's 22.0: nothing aimed
# with it well enough for the disagreement to show, which is exactly how a mirrored
# constant goes bad. Dropping the arc removes the constant that had to be kept in step.
BOLT_SPEED = 420.0
BOLT_MAX_RANGE = 2000.0

SHIP_MAX_HEALTH = 30.0
MOTHERSHIP_MAX_HEALTH = 500.0

UPGRADE_INFO = {
    UPGRADE_TRACTOR: ("Tractor Amplifier", "Beam counts as two - solo a MASSIVE", 1, 1200.0, 1.0),
    UPGRADE_THRUST: ("Engine Boost", "+25% thrust per level", 3, 400.0, 1.6),
    UPGRADE_RANGE: ("Beam Extender", "+8 m tractor reach per level", 3, 350.0, 1.5),
    UPGRADE_HULL: ("Cargo Dampeners", "Cargo takes 35% less damage per level", 2, 500.0, 1.8),
    UPGRADE_LASER: ("Laser Focuser", "+40% laser damage per level", 3, 450.0, 1.7),
    UPGRADE_CAPACITOR: ("Capacitor Bank", "+2 shots per charge", 3, 400.0, 1.6),
    UPGRADE_RECHARGER: ("Fast Recharger", "Charge refills 20% quicker per level", 3, 380.0, 1.6),
    UPGRADE_SHIELDS: ("Station Shields", "TEAM: mothership takes 20% less damage", 3, 900.0, 1.7),
    UPGRADE_TURRETS: ("Station Turrets", "TEAM: mothership shoots back", 3, 1000.0, 1.6),
}


def upgrade_cost(upgrade_id: int, have: int) -> float | None:
    """Price to go from `have` to `have+1`, or None if maxed. Mirrors CostFor in Go."""
    info = UPGRADE_INFO.get(upgrade_id)
    if info is None:
        return None
    _name, _desc, max_level, base, growth = info
    if have >= max_level:
        return None
    cost = base
    for _ in range(have):
        cost *= growth
    return cost

# --- input flag bits --------------------------------------------------------

INPUT_GRAB = 1 << 0
INPUT_BOOST = 1 << 1
INPUT_BRAKE = 1 << 2
INPUT_FIRE = 1 << 3

# --- body record flag bits --------------------------------------------------

BODY_HELD = 1 << 0
BODY_SLEEPING = 1 << 1
BODY_DAMAGED = 1 << 2
BODY_DEAD = 1 << 3  # a destroyed ship awaiting respawn: do not draw it
# Engines actually running hot. On the body rather than in PlayerState because every
# client draws every ship's plume, and PlayerState only reaches the ship's own session.
BODY_BOOSTING = 1 << 4

# --- teams ------------------------------------------------------------------

NO_TEAM = 0xFF

# Team colours. Chosen to stay distinguishable against the dark blue skybox and against
# the tier colours, so "that's an enemy" and "that's a gold rock" never read the same.
TEAM_COLORS = {
    0: (0.90, 0.22, 0.20),  # red
    1: (0.24, 0.50, 0.95),  # blue
    2: (0.30, 0.82, 0.38),  # green
    3: (0.95, 0.72, 0.18),  # amber
    # A full palette to pick from. The first four keep their old ids so an existing
    # match, a saved profile or a screenshot still means the same thing.
    #
    # Every entry is kept bright and reasonably saturated: these are read as a tint over a
    # shaded hull against a near-black sky, and anything dark or muddy turns into "some
    # ship" at fifty metres. Neighbouring hues are separated enough to survive that tint —
    # the test is whether two of them are still tellable apart on a moving ship, not
    # whether they differ on a colour wheel.
    4: (0.72, 0.35, 0.95),  # violet
    5: (0.20, 0.85, 0.85),  # cyan
    6: (0.98, 0.48, 0.16),  # orange
    7: (0.98, 0.45, 0.72),  # pink
    8: (0.60, 0.90, 0.25),  # lime
    9: (0.55, 0.42, 0.90),  # indigo
    10: (0.95, 0.90, 0.40),  # butter
    11: (0.15, 0.70, 0.55),  # teal
    12: (0.85, 0.35, 0.45),  # rose
    13: (0.45, 0.68, 0.95),  # sky
    14: (0.80, 0.62, 0.35),  # bronze
    15: (0.92, 0.92, 0.96),  # white
}

TEAM_COLOR_NAMES = {
    0: "RED", 1: "BLUE", 2: "GREEN", 3: "AMBER",
    4: "VIOLET", 5: "CYAN", 6: "ORANGE", 7: "PINK",
    8: "LIME", 9: "INDIGO", 10: "BUTTER", 11: "TEAL",
    12: "ROSE", 13: "SKY", 14: "BRONZE", 15: "WHITE",
}

# How many colours a host can choose between.
TEAM_COLOR_COUNT = 16

NEUTRAL_COLOR = (0.70, 0.72, 0.78)


# Which palette entry each team is currently wearing, keyed by team id.
#
# Teams used to *be* their colour — team 1 was always blue — so every call site passed a
# team id straight into the palette. Now that a crew can pick from sixteen, the mapping
# lives here and is refreshed from TeamState, which means hulls, beams, lasers, station
# paint and the end-of-match clip all follow a colour change without any of them knowing
# a choice was ever made.
_TEAM_PALETTE: dict[int, int] = {}


def set_team_palette(teams) -> None:
    """Refresh the team-to-colour mapping from a decoded TeamState list."""
    _TEAM_PALETTE.clear()
    for t in teams or ():
        _TEAM_PALETTE[t.team_id] = t.color


def team_color(team: int) -> tuple[float, float, float]:
    """The RGB a team is wearing. Falls back to the old team-id-is-the-colour rule, which
    is still right before any TeamState has arrived."""
    if team == NO_TEAM:
        return NEUTRAL_COLOR
    return TEAM_COLORS.get(_TEAM_PALETTE.get(team, team), NEUTRAL_COLOR)


def team_color_name(team: int) -> str:
    return TEAM_COLOR_NAMES.get(_TEAM_PALETTE.get(team, team), "?")

# --- entity kinds -----------------------------------------------------------

KIND_SHIP = 0
KIND_ASTEROID = 1
KIND_SALVAGE = 2
KIND_MOTHERSHIP = 3
KIND_HAZARD = 4
# Static map scenery: derelicts, wrecks, broken worlds. Solid and indestructible; the
# tier byte carries which structure to draw. See server/sim/props.go and client/props.py.
KIND_PROP = 5

# A round in flight. Not a physics body on the server — rounds travel, and they arrive as
# synthetic snapshot entries so what you see is what the server resolves against. The tier
# byte separates a laser bolt (0) from an item missile (1).
# See server/sim/projectiles.go.
KIND_BOLT = 6
BOLT_LASER = 0
BOLT_MISSILE = 1

# A cargo box drifting in a bubble. Not a physics body either: you fly through one to
# collect it rather than bouncing off it. The tier byte carries which ITEM_* is inside.
# See server/sim/items.go.
KIND_PICKUP = 7

PROP_BATTLESTATION = 0
PROP_CAPITAL_WRECK = 1
PROP_PLANET_CHUNK = 2
PROP_STATION_RUIN = 3

# --- items ------------------------------------------------------------------
#
# Carried in three slots, used by slot with 0x08 UseItem. Mirrors sim.ItemID.

ITEM_NONE = 0
ITEM_MISSILE = 1
ITEM_DAMAGE = 2
ITEM_SPEED = 3
ITEM_SHIELD = 4

ITEM_SLOTS = 3

# What a crew is called until somebody renames it. Mirrors sim.DefaultTeamName, and used
# by the host screen's team picker before any TeamState has arrived to say otherwise.
DEFAULT_TEAM_NAMES = ["Team A", "Team B", "Team C", "Team D"]

# name, short label for the hotbar, and what it does — for the HUD and the tooltip.
ITEM_INFO = {
    ITEM_MISSILE: ("Missile", "MSL", "one shot, 25 damage"),
    ITEM_DAMAGE: ("Overcharge", "DMG", "+50% laser damage for 30s"),
    ITEM_SPEED: ("Afterburner", "SPD", "+50% thrust for 30s"),
    ITEM_SHIELD: ("Shield", "SHD", "absorbs the next 30 damage"),
}

# Every cargo box in the world is this colour, whatever is inside it. A box is a surprise
# until you have it, so going for one is a decision about position rather than shopping.
PICKUP_COLOR = (0.45, 1.00, 0.55)

# Per-item colours, used on the hotbar only — once an item is yours, telling the three
# slots apart at a glance matters more than the mystery does.
ITEM_COLORS = {
    ITEM_MISSILE: (1.00, 0.55, 0.30),
    ITEM_DAMAGE: (1.00, 0.35, 0.35),
    ITEM_SPEED: (0.40, 0.85, 1.00),
    ITEM_SHIELD: (0.55, 1.00, 0.65),
}

# --- event types ------------------------------------------------------------

EVENT_GRABBED = 0
EVENT_DROPPED = 1
EVENT_DEPOSITED = 2
EVENT_DAMAGED = 3
EVENT_DESTROYED = 4
# For these the `player` field carries a TEAM id (0xFF = nobody / draw) and `value`
# carries the round number or winning score.
EVENT_ROUND_START = 5
EVENT_ROUND_END = 6
EVENT_MATCH_OVER = 7
# Combat.
EVENT_SHIP_HIT = 8
EVENT_SHIP_DESTROYED = 9  # value carries the victim's team
EVENT_SHIP_RESPAWN = 10  # value carries the team
EVENT_ASTEROID_HIT = 11
EVENT_ASTEROID_DESTROYED = 12  # value carries the tier that broke

# Sieging a station. `player` is the shooter, so a client can tell being hit from hitting.
EVENT_MOTHERSHIP_HIT = 13
EVENT_MOTHERSHIP_DESTROYED = 14  # value carries the destroyed team
EVENT_SHIELD_ABSORBED = 15  # value carries the damage the shield ate
EVENT_TEAM_ELIMINATED = 16  # value carries the team id of the last crew standing
EVENT_HILL_MOVED = 17  # the King of the Hill zone relocated; value carries its radius

# Cargo boxes. Value carries the ITEM_* id in both cases.
EVENT_ITEM_PICKED_UP = 18
EVENT_ITEM_USED = 19

BODY_RECORD_SIZE = 27

# Asteroid tiers. Wire values; the balance table lives in server/sim/tiers.go.
TIER_RUBBLE = 0
TIER_IRON = 1
TIER_CRYSTAL = 2
TIER_GOLD = 3
TIER_MASSIVE = 4
TIER_COLOSSAL = 5
TIER_CORE = 6  # never spawns loose; only ever cut out of something bigger
TIER_TITAN = 7  # later rounds only, and only a good gun gets through one

# (r, g, b) per tier, so a player can read value and difficulty at a glance.
TIER_COLORS = {
    TIER_RUBBLE: (0.42, 0.41, 0.39),
    TIER_IRON: (0.62, 0.36, 0.22),
    TIER_CRYSTAL: (0.35, 0.85, 0.95),
    TIER_GOLD: (0.98, 0.78, 0.20),
    TIER_MASSIVE: (0.62, 0.18, 0.20),
    TIER_COLOSSAL: (0.55, 0.50, 0.66),
    # Near-white with a violet cast: nothing else in the belt is this bright, which is
    # the point — a core should be visible across the map the moment it is cut free.
    TIER_CORE: (0.96, 0.86, 1.00),
    # Almost black, so the biggest rock in the game reads as a hole in the starfield
    # rather than as another boulder.
    TIER_TITAN: (0.20, 0.19, 0.26),
}

TIER_NAMES = {
    TIER_RUBBLE: "rubble",
    TIER_IRON: "iron",
    TIER_CRYSTAL: "crystal",
    TIER_GOLD: "gold",
    TIER_MASSIVE: "MASSIVE",
    TIER_COLOSSAL: "COLOSSAL",
    TIER_CORE: "CORE",
    TIER_TITAN: "TITAN",
}

# Tiers that one pilot cannot move alone.
TIER_NEEDS_CREW = {TIER_MASSIVE}

# Tiers no beam can tow at all — shoot them apart and haul the pieces.
TIER_UNTOWABLE = {TIER_COLOSSAL, TIER_TITAN}

# Tiers worth shouting about when one is on your beam.
TIER_PRECIOUS = {TIER_CORE}

# Radius travels as two bytes in 0.05 m units, covering 0-3276 m. It used to be one
# byte at 0.25 m, which capped out at 63.75 m — fine until asteroids grew past the
# mothership.
RADIUS_QUANTUM = 0.05

_INV_SQRT2 = 0.7071067811865476

# Pre-built struct objects; parsing runs every frame and this avoids re-compiling
# the format string each time.
_U16 = struct.Struct("<H")
_U32 = struct.Struct("<I")
_F32 = struct.Struct("<f")
_SNAP_HEADER = struct.Struct("<IIH")
_BODY = struct.Struct("<IBBBBBHfffI")
_SHOT = struct.Struct("<IB")
_INPUT = struct.Struct("<BIffffffB")
_WELCOME = struct.Struct("<IIBBB")
_EVENT = struct.Struct("<BIIf")
_TEAM = struct.Struct("<BfB")
_RESULT_HEAD = struct.Struct("<IBB")  # player_id, team, name_len
_RESULT_TAIL = struct.Struct("<HfHHf")  # delivered, banked, kills, deaths, credits
_DEBUG_STATS = struct.Struct("<fHH")
_DEBUG_SET = struct.Struct("<BHf")


class Vec3(NamedTuple):
    x: float
    y: float
    z: float


class Quat(NamedTuple):
    x: float
    y: float
    z: float
    w: float


def unpack_quat(v: int) -> Quat:
    """Invert the server's smallest-three packing.

    The largest-magnitude component was dropped and forced positive, so it comes back as
    a plain sqrt. The other three were quantised to 10 bits each over +/-1/sqrt(2).
    """
    largest = (v >> 30) & 0x3
    comps = [0.0, 0.0, 0.0, 0.0]

    shift = 20
    sum_sq = 0.0
    for i in range(4):
        if i == largest:
            continue
        q10 = (v >> shift) & 0x3FF
        c = ((q10 / 1023.0) * 2.0 - 1.0) * _INV_SQRT2
        comps[i] = c
        sum_sq += c * c
        shift -= 10

    comps[largest] = math.sqrt(max(0.0, 1.0 - sum_sq))
    return Quat(comps[0], comps[1], comps[2], comps[3])


# --- client -> server -------------------------------------------------------


def encode_hello(name: str, team_pref: int = 0xFF) -> bytes:
    raw = name.encode("utf-8")[:32]
    return bytes([MSG_HELLO, len(raw)]) + raw + bytes([team_pref & 0xFF])


def encode_input(
    seq: int,
    thrust_fwd: float = 0.0,
    thrust_right: float = 0.0,
    thrust_up: float = 0.0,
    yaw: float = 0.0,
    pitch: float = 0.0,
    roll: float = 0.0,
    grab: bool = False,
    boost: bool = False,
    brake: bool = False,
    fire: bool = False,
) -> bytes:
    flags = 0
    if grab:
        flags |= INPUT_GRAB
    if boost:
        flags |= INPUT_BOOST
    if brake:
        flags |= INPUT_BRAKE
    if fire:
        flags |= INPUT_FIRE
    return _INPUT.pack(
        MSG_INPUT,
        seq & 0xFFFFFFFF,
        thrust_fwd,
        thrust_right,
        thrust_up,
        yaw,
        pitch,
        roll,
        flags,
    )


def encode_debug_set(param: int, value: float) -> bytes:
    return _DEBUG_SET.pack(MSG_DEBUG_SET, param, value)


# --- server -> client -------------------------------------------------------


@dataclass(slots=True)
class Welcome:
    player_id: int
    ship: int
    team: int
    tick_rate: int
    snapshot_rate: int


@dataclass(slots=True)
class Body:
    entity: int
    kind: int
    flags: int
    tier: int
    radius: float
    team: int
    health: float  # 0-1 of this body's maximum; 1 for anything that cannot be shot
    pos: Vec3
    rot: Quat

    @property
    def held(self) -> bool:
        return bool(self.flags & BODY_HELD)

    @property
    def damaged(self) -> bool:
        return bool(self.flags & BODY_DAMAGED)

    @property
    def dead(self) -> bool:
        return bool(self.flags & BODY_DEAD)

    @property
    def boosting(self) -> bool:
        return bool(self.flags & BODY_BOOSTING)


@dataclass(slots=True)
class Snapshot:
    tick: int
    server_ms: int
    bodies: list[Body] = field(default_factory=list)


@dataclass(slots=True)
class Event:
    type: int
    entity: int
    player: int
    value: float


@dataclass(slots=True)
class TeamState:
    team_id: int
    score: float
    members: int
    name: str = ""

    # Station upgrade levels, shared by the whole crew. They ride with the team rather
    # than in PlayerState because every client needs a *rival's* shield level to draw
    # their bubble, not just its own.
    shields: int = 0
    turrets: int = 0

    # Respawns this crew has left in the current round. Everyone sees everyone's — a
    # rival down to its last two ships is information worth acting on.
    lives: int = 0

    # Palette index this crew is wearing, 0-15. Defaults to the team id, which is the
    # old fixed mapping.
    color: int = 0


@dataclass(slots=True)
class DebugStats:
    tick_ms: float
    bodies: int
    sessions: int


def decode_welcome(body: bytes) -> Welcome:
    pid, ship, team, tick, snap = _WELCOME.unpack_from(body, 0)
    return Welcome(pid, ship, team, tick, snap)


def decode_snapshot(body: bytes) -> Snapshot:
    tick, server_ms, count = _SNAP_HEADER.unpack_from(body, 0)

    expected = _SNAP_HEADER.size + count * BODY_RECORD_SIZE
    if len(body) < expected:
        raise ValueError(f"snapshot claims {count} bodies but is {len(body)} bytes")

    bodies: list[Body] = []
    off = _SNAP_HEADER.size
    unpack = _BODY.unpack_from
    for _ in range(count):
        entity, kind, flags, tier, team, health, radius, px, py, pz, rot = unpack(body, off)
        bodies.append(
            Body(
                entity,
                kind,
                flags,
                tier,
                radius * RADIUS_QUANTUM,
                team,
                health / 255.0,
                Vec3(px, py, pz),
                unpack_quat(rot),
            )
        )
        off += BODY_RECORD_SIZE

    return Snapshot(tick, server_ms, bodies)


def decode_event(body: bytes) -> Event:
    etype, entity, player, value = _EVENT.unpack_from(body, 0)
    return Event(etype, entity, player, value)


def decode_team_state(body: bytes) -> list[TeamState]:
    n = body[0]
    out = []
    off = 1
    for _ in range(n):
        tid, score, members = _TEAM.unpack_from(body, off)
        off += _TEAM.size
        # Names are length-prefixed, so records are variable width.
        name_len = body[off]
        off += 1
        name = body[off : off + name_len].decode("utf-8", "replace")
        off += name_len
        shields, turrets, lives, color = (
            body[off], body[off + 1], body[off + 2], body[off + 3]
        )
        off += 4
        out.append(
            TeamState(tid, score, members, name, shields, turrets, lives, color)
        )
    return out


@dataclass(slots=True)
class ShopOffer:
    upgrade: int
    level: int
    cost: float


def decode_shop_offers(body: bytes) -> list[ShopOffer]:
    n = body[0]
    out = []
    off = 1
    for _ in range(n):
        upgrade, level = body[off], body[off + 1]
        cost = _F32.unpack_from(body, off + 2)[0]
        out.append(ShopOffer(upgrade, level, cost))
        off += 6
    return out


def encode_buy_offer(slot: int) -> bytes:
    return bytes([MSG_BUY_OFFER, slot & 0xFF])


def encode_set_team_name(name: str) -> bytes:
    raw = name.encode("utf-8")[:20]
    return bytes([MSG_SET_TEAM_NAME, len(raw)]) + raw


def decode_debug_stats(body: bytes) -> DebugStats:
    tick_ms, bodies, sessions = _DEBUG_STATS.unpack_from(body, 0)
    return DebugStats(tick_ms, bodies, sessions)


@dataclass(slots=True)
class MatchState:
    phase: int
    round: int
    best_of: int
    wins_needed: int
    time_left: int
    winner: int  # 0xFF = none / draw
    round_wins: dict[int, int] = field(default_factory=dict)

    # Which game is being played. Appended after the team block, so an older decoder
    # that stops there still parses everything it understands.
    mode: int = MODE_SALVAGE


def decode_match_state(body: bytes) -> MatchState:
    phase, rnd, best_of, wins_needed = body[0], body[1], body[2], body[3]
    time_left = _U16.unpack_from(body, 4)[0]
    winner = body[6]
    n = body[7]

    wins: dict[int, int] = {}
    off = 8
    for _ in range(n):
        wins[body[off]] = body[off + 1]
        off += 2

    mode = body[off] if len(body) > off else MODE_SALVAGE
    return MatchState(phase, rnd, best_of, wins_needed, time_left, winner, wins, mode)


@dataclass(slots=True)
class RosterEntry:
    """One seat in the lobby."""

    player_id: int
    team: int
    name: str


@dataclass(slots=True)
class Roster:
    host_id: int  # the only player the server accepts a start from; 0 = nobody yet
    capacity: int  # seats per crew, so the lobby can draw empty ones
    players: list[RosterEntry] = field(default_factory=list)

    def by_team(self, team: int) -> list[RosterEntry]:
        return [r for r in self.players if r.team == team]


def decode_roster(body: bytes) -> Roster:
    host_id = _U32.unpack_from(body, 0)[0]
    capacity, n = body[4], body[5]

    out: list[RosterEntry] = []
    off = 6
    for _ in range(n):
        pid = _U32.unpack_from(body, off)[0]
        team, name_len = body[off + 4], body[off + 5]
        off += 6
        name = body[off : off + name_len].decode("utf-8", "replace")
        off += name_len
        out.append(RosterEntry(pid, team, name))
    return Roster(host_id, capacity, out)


def encode_start_match() -> bytes:
    """Ask the server to leave the lobby. Ignored from anyone but the host."""
    return bytes([MSG_START_MATCH])


def encode_set_team(team: int) -> bytes:
    """Move to another crew. Accepted in the lobby only, and only if it has a seat."""
    return bytes([MSG_SET_TEAM, team & 0xFF])


def encode_use_item(slot: int) -> bytes:
    """Spend whatever is in an inventory slot. A no-op on the server if it is empty."""
    return bytes([MSG_USE_ITEM, slot & 0xFF])


def encode_chat(channel: int, text: str) -> bytes:
    raw = text.encode("utf-8")[:CHAT_MAX_BYTES]
    return bytes([MSG_CHAT, channel & 0xFF, len(raw)]) + raw


@dataclass(slots=True)
class ChatSay:
    """A message on its way in. Name and team come from the server, not the sender."""

    channel: int
    team: int
    name: str
    text: str


def decode_chat_say(body: bytes) -> ChatSay:
    channel, team = body[0], body[1]
    n = body[2]
    name = body[3 : 3 + n].decode("utf-8", "replace")

    off = 3 + n
    t = body[off]
    off += 1
    text = body[off : off + t].decode("utf-8", "replace")
    return ChatSay(channel, team, name, text)


@dataclass(slots=True)
class MatchResult:
    """One pilot's row on the end-of-match screen."""

    player_id: int
    team: int
    name: str
    delivered: int  # rocks banked over the whole match, not this round
    banked: float  # their total value
    kills: int
    deaths: int
    credits: float


@dataclass(slots=True)
class MatchResults:
    winner: int  # 0xFF = draw
    players: list[MatchResult] = field(default_factory=list)


def decode_match_results(body: bytes) -> MatchResults:
    """Parse a 0x88. Rows arrive pre-sorted; keep the order the server sent.

    Re-sorting here would defeat the point of sorting there — every client is meant to
    draw the same table.
    """
    winner = body[0]
    n = body[1]

    out: list[MatchResult] = []
    off = 2
    for _ in range(n):
        pid, team, name_len = _RESULT_HEAD.unpack_from(body, off)
        off += _RESULT_HEAD.size
        name = body[off : off + name_len].decode("utf-8", "replace")
        off += name_len

        delivered, banked, kills, deaths, credits = _RESULT_TAIL.unpack_from(body, off)
        off += _RESULT_TAIL.size

        out.append(
            MatchResult(pid, team, name, delivered, banked, kills, deaths, credits)
        )
    return MatchResults(winner, out)


@dataclass(slots=True)
class PlayerState:
    credits: float
    upgrades: dict[int, int] = field(default_factory=dict)

    # Combat. These arrive at the snapshot rate rather than the slower shop cadence,
    # because they drive live HUD bars.
    energy: float = 0.0
    max_energy: float = 1.0
    health: float = 1.0  # 0-1
    respawn_in: float = 0.0  # seconds; 0 when alive
    boost: float = 0.0  # seconds of burn left in the tank
    max_boost: float = 1.0

    # Seconds of forced stall left after running the tank dry. Non-zero means the bar is
    # not filling and boost cannot engage at all — a different state from merely low.
    boost_cooldown: float = 0.0

    # The team's life pool ran out on this player's death, so no respawn is coming. Not
    # inferable from respawn_in: a grounded player's countdown is zero, same as alive.
    grounded: bool = False

    # Three inventory slots and the effects the used ones are running. items holds
    # ITEM_* ids, ITEM_NONE for empty; the boosts are seconds remaining and item_shield
    # is damage the shield can still absorb rather than a timer.
    items: list[int] = field(default_factory=lambda: [ITEM_NONE] * ITEM_SLOTS)
    item_damage: float = 0.0
    item_speed: float = 0.0
    item_shield: float = 0.0

    @property
    def boost_stalled(self) -> bool:
        return self.boost_cooldown > 0.0

    @property
    def dead(self) -> bool:
        return self.respawn_in > 0.0 or self.grounded

    @property
    def shots_ready(self) -> int:
        return int(self.energy)


def decode_player_state(body: bytes) -> PlayerState:
    credits = _F32.unpack_from(body, 0)[0]
    n = body[4]
    ups: dict[int, int] = {}
    off = 5
    for _ in range(n):
        ups[body[off]] = body[off + 1]
        off += 2

    ps = PlayerState(credits, ups)
    # The combat block trails the upgrade table so an older message still parses.
    if len(body) >= off + 16:
        ps.energy, ps.max_energy, ps.health, ps.respawn_in = struct.unpack_from(
            "<ffff", body, off
        )
    if len(body) >= off + 24:
        ps.boost, ps.max_boost = struct.unpack_from("<ff", body, off + 16)
    if len(body) >= off + 28:
        ps.boost_cooldown = _F32.unpack_from(body, off + 24)[0]
    if len(body) >= off + 29:
        ps.grounded = body[off + 28] != 0
    # Inventory and item effects, appended after the combat block for the same reason it
    # was appended after the upgrade table: a decoder that stops earlier still works.
    if len(body) >= off + 29 + ITEM_SLOTS + 12:
        o = off + 29
        ps.items = list(body[o : o + ITEM_SLOTS])
        o += ITEM_SLOTS
        ps.item_damage, ps.item_speed, ps.item_shield = struct.unpack_from("<fff", body, o)
    return ps


@dataclass(slots=True)
class Shot:
    """One muzzle discharge: a flash and a bang at the barrel.

    Rounds travel and fall now, and arrive as their own bodies in the snapshot, so a shot
    no longer describes where anything went — only that a gun fired.
    """

    shooter: int  # entity id of the firing ship, or of a mothership for a turret
    team: int


def decode_shots(body: bytes) -> list[Shot]:
    n = body[0]
    out: list[Shot] = []
    off = 1
    for _ in range(n):
        shooter, team = _SHOT.unpack_from(body, off)
        out.append(Shot(shooter, team))
        off += _SHOT.size
    return out


def encode_set_team_color(color: int) -> bytes:
    """Ask the server to put this crew in a palette entry. Clamped server-side."""
    return bytes([MSG_SET_TEAM_COLOR, color & 0xFF])


def encode_buy_upgrade(upgrade_id: int) -> bytes:
    return bytes([MSG_BUY_UPGRADE, upgrade_id & 0xFF])


# --- tunable parameter table ------------------------------------------------
#
# Mirrors the table in shared/protocol.md and the registry in server/sim/tuning.go.
# The dev console builds its sliders from this.

PARAMS: list[tuple[int, str, float, float]] = [
    (0x0001, "grab.spring", 0.0, 2000.0),
    (0x0002, "grab.damping", 0.0, 200.0),
    (0x0003, "grab.max_force", 0.0, 20000.0),
    (0x0004, "grab.hold_distance", 1.0, 30.0),
    (0x0005, "grab.reaction_scale", 0.0, 2.0),
    (0x0006, "grab.range", 5.0, 200.0),
    (0x0010, "ship.thrust", 0.0, 5000.0),
    (0x0011, "ship.torque", 0.0, 6000.0),
    (0x0012, "ship.linear_damping", 0.0, 5.0),
    (0x0013, "ship.angular_damping", 0.0, 3000.0),
    (0x0020, "salvage.damage_threshold", 0.0, 50.0),
    (0x0021, "salvage.damage_scale", 0.0, 1.0),
]

PARAM_DEFAULTS: dict[str, float] = {
    "grab.spring": 220.0,
    "grab.damping": 28.0,
    "grab.max_force": 1800.0,
    "grab.hold_distance": 6.0,
    "grab.reaction_scale": 1.0,
    "grab.range": 15.0,
    "ship.thrust": 900.0,
    "ship.torque": 1400.0,
    "ship.linear_damping": 0.35,
    "ship.angular_damping": 900.0,
    "salvage.damage_threshold": 6.0,
    "salvage.damage_scale": 0.04,
}
