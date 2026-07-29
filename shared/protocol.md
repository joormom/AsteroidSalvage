# Wire Protocol — v1

**This file is the single source of truth.** Go (`server/net/encode.go`) and Python
(`client/net.py`, `tools/devconsole/debugnet.py`) each implement it independently, so
protocol drift is the most likely source of bugs in this project. Change this document
*before* changing either implementation.

## Framing

- Transport: WebSocket **binary** frames (never text — JSON in the hot path would waste
  sprocket's zero-allocation design).
- Byte order: **little-endian** throughout.
- Every message: 1-byte type tag, then a type-specific payload.
- One message per frame. No length prefix needed — WebSocket already frames.

Types `0x01–0x7F` are client→server. Types `0x80–0xFF` are server→client.

## Coordinates & units

Right-handed, Z-up (Panda3D's native convention, so the client needs no axis swizzle).
Metres, kilograms, seconds. Ship-local axes: +Y forward, +X right, +Z up.

## Rates

| Channel | Rate |
|---|---|
| Simulation tick | 30 Hz (fixed, `lg_world_step`) |
| Snapshot broadcast | 15 Hz (every 2nd tick) |
| Client input | 30 Hz |
| TeamState | 2 Hz |
| DebugStats | ~4 Hz |

Snapshots are every 2nd tick rather than a nominal 20 Hz because 20 Hz off a 30 Hz loop
needs 1.5 ticks, which in practice alternates 33 ms / 67 ms. Uneven spacing is worse for
interpolation than a slightly lower but even rate, and the client buffers ~100 ms
regardless. Raise to every tick (30 Hz) if bandwidth ever proves cheaper than latency.

---

## Client → Server

### `0x01` Input

Sent at 30 Hz, unreliable-in-spirit (latest wins; `seq` lets the server drop reorders).

| Field | Type | Notes |
|---|---|---|
| `seq` | `u32` | Monotonic per client. Server ignores `seq <= last_seq`. |
| `thrust_fwd` | `f32` | −1..1 |
| `thrust_right` | `f32` | −1..1 |
| `thrust_up` | `f32` | −1..1 |
| `yaw` | `f32` | −1..1 |
| `pitch` | `f32` | −1..1 |
| `roll` | `f32` | −1..1 |
| `flags` | `u8` | bit0 = grab held, bit1 = boost, bit2 = brake, bit3 = fire |

Size: 1 + 4 + 24 + 1 = **30 bytes**.

### `0x02` Hello

First message after connect.

| Field | Type | Notes |
|---|---|---|
| `name_len` | `u8` | ≤ 32 |
| `name` | `u8[name_len]` | UTF-8 |
| `team_pref` | `u8` | `0xFF` = no preference / auto-assign |

### `0x03` BuyUpgrade

| Field | Type | Notes |
|---|---|---|
| `upgrade` | `u8` | `0`=tractor amplifier, `1`=engine boost, `2`=beam extender, `3`=cargo dampeners, `4`=laser focuser, `5`=capacitor bank, `6`=fast recharger, `7`=station shields, `8`=station turrets |

Rejected outside `PhaseIntermission`. On success the server replies with a fresh
`0x85 PlayerState`. Costs and effects live in `server/sim/upgrades.go`.

`7` and `8` are bought with personal credits but count up on the **team**, so a shield a
crewmate paid for in round two is still there for someone who joins in round three, and
two people buying it does not get the crew two shields. Their levels ride in
`0x83 TeamState` rather than `0x85 PlayerState` for that reason — see there.

### `0x04` BuyOffer

| Field | Type | Notes |
|---|---|---|
| `slot` | `u8` | Which of this intermission's offers, `0`-based |

Buying by **slot** rather than by upgrade id is the point: the server rolled the offers
and only accepts what it actually put on that player's screen. `0x03 BuyUpgrade` names an
upgrade directly and is the older path; offers are what the shop uses.

Rejected outside `PhaseIntermission`, and rejected if the slot was never offered. On
success the server replies with a fresh `0x85 PlayerState` and `0x86 ShopOffers`.

### `0x05` SetTeamName

| Field | Type | Notes |
|---|---|---|
| `name_len` | `u8` | ≤ 20; longer names are truncated by the sender |
| `name` | `u8[name_len]` | UTF-8, stripped of control characters on arrival |

Names the sender's whole crew, not the sender. Claimed after connecting rather than passed
at Hello, which is what lets it apply when joining someone else's game too.

### `0x06` SetTeamColor

| Field | Type | Notes |
|---|---|---|
| `color` | `u8` | Palette entry, `0`–`15` |

Colour is a property of the **team on the server**, not a client-side preference — so
every client draws that crew's hulls, lasers, beams and station the same way, and the
choice survives into a game somebody else is hosting.

### `0x07` StartMatch

No body — the tag alone. Leaves `PhaseLobby` and starts the warmup into round one.

Accepted only from the player the server considers the host, and only while the phase is
`4`. From anyone else, or in any other phase, it is silently ignored rather than answered
with an error: it is a button that should not have been pressable, not a protocol
violation.

The host is the **first player to connect**. A hosting client launches the server and
connects to it before it has told anyone the address, so in practice that is always the
person who pressed HOST — and it needs no shared secret between the server process and the
client that spawned it. The role does not move if the host leaves, since handing it to
whoever happened to be next would let a joiner start a match somebody else was setting up.

### `0x08` UseItem

| Field | Type | Notes |
|---|---|---|
| `slot` | `u8` | Inventory slot, `0`–`2` |

Spends whatever is in the slot. An empty slot, an out-of-range one, or a request from a
player waiting to respawn are all silently ignored — pressing a key that had nothing behind
it is not a protocol violation.

On success the server replies with a fresh `0x85 PlayerState`, which is where the client
learns the slot is now empty and the effect is running. The client must not empty the slot
itself: the server is the authority on whether the item was actually spent.

### `0x09` Chat

| Field | Type | Notes |
|---|---|---|
| `channel` | `u8` | `0`=all, `1`=team |
| `len` | `u8` | ≤ 120 |
| `text` | `u8[len]` | UTF-8 |

The sender's name and team are **not** in this message. The server fills them in from the
session (see `0x8A`), so a client cannot put words in somebody else's mouth or claim a crew
it is not on.

Control characters are stripped on arrival and the result is trimmed; a message that is
empty afterwards is dropped rather than relayed.

### `0x0A` SetTeam

| Field | Type | Notes |
|---|---|---|
| `team` | `u8` | Crew to move to |

**Lobby only** (phase `4`), and refused if that crew is already at `TeamSize`, or in co-op
where there is only one crew to be on. Silently ignored otherwise.

Switching mid-match would let somebody join whichever crew is winning, and would strand
whatever their old crew was counting on them for. On success the server moves the session
between team rooms — that is what scopes `0x09 Chat` — rebroadcasts `0x89 Roster`, and
respawns the ship at the new crew's spawn point.

### `0x10` DebugSet — dev builds only

Rejected unless the server was started with `--dev`. **This is a remote
physics-mutation endpoint**; it must never be reachable in a release build.

| Field | Type | Notes |
|---|---|---|
| `param` | `u16` | See parameter IDs below |
| `value` | `f32` | |

---

## Server → Client

### `0x80` Welcome

Sent once, immediately after `Hello`.

| Field | Type |
|---|---|
| `player_id` | `u32` |
| `ship_entity` | `u32` |
| `team_id` | `u8` |
| `tick_rate` | `u8` |
| `snapshot_rate` | `u8` |

### `0x81` Snapshot

Broadcast to the match room at 20 Hz.

| Field | Type | Notes |
|---|---|---|
| `tick` | `u32` | Server sim tick |
| `server_ms` | `u32` | Server clock, for interpolation delay |
| `count` | `u16` | Number of body records following |

Then `count` records of **27 bytes** each:

| Field | Type | Notes |
|---|---|---|
| `entity` | `u32` | |
| `kind` | `u8` | `0`=ship, `1`=asteroid, `2`=salvage, `3`=mothership, `4`=hazard, `5`=prop, `6`=bolt, `7`=pickup |
| `flags` | `u8` | bit0 = held, bit1 = sleeping, bit2 = damaged, bit3 = dead (destroyed ship awaiting respawn — do not draw), bit4 = boosting |
| `tier` | `u8` | Asteroids: `0`=rubble, `1`=iron, `2`=crystal, `3`=gold, `4`=massive, `5`=colossal, `6`=core, `7`=titan. Props: which structure — see below |
| `team` | `u8` | Owning crew for ships and motherships; `0xFF` = neutral |
| `health` | `u8` | Fraction of this body's maximum, `value / 255`. Always 255 for anything that cannot be shot |
| `radius` | `u16` | Metres in 0.05 m units (`radius_m = value * 0.05`), so 0–3276 m |
| `pos` | `f32[3]` | |
| `rot` | `u32` | Quaternion, smallest-three packed (see below) |

Radius was one byte at 0.25 m units until titan-class asteroids grew past its 63.75 m
ceiling. Two bytes at 0.05 m costs one byte per body — about 3 KB/s at full load — and
removes the ceiling as a design constraint.

`tier` drives the client's asteroid colour and the "needs a second beam" warning. The
balance table it refers to (size, density, payout, spawn weight, toughness and what it
splits into) lives in `server/sim/tiers.go` — only the tier id is on the wire.

On a **prop** the same byte selects which structure to draw instead: `0`=battlestation,
`1`=capital wreck, `2`=planet chunk, `3`=ruined ring station (`sim.PropKind`). Props are
static map scenery — solid, indestructible, and neutral. Reusing the byte rather than
adding one is why scenery needed no new message at all.

On a **bolt** the tier byte separates a laser round (`0`) from an item missile (`1`). A
missile steers toward a target locked at launch, capped at 1.6 rad/s — the client draws
where the server says it is, so guidance needs nothing on the wire beyond the pose that is
already there.

On a **pickup** it carries which item is in the box: `1`=missile, `2`=damage boost,
`3`=speed boost, `4`=shield — the same `ItemID` values `0x85 PlayerState` reports.

**Several of these kinds are not physics bodies.** Props are; the hill, bolts and pickups
are synthesised into the snapshot each time it is built:

- `4` **hazard** is the King of the Hill control point, sent only in that mode. It is a
  volume you fly *through* — a collider would make holding it a matter of bouncing off it
  — so it has no body and no entity of its own. `team` is whoever holds it, `0xFF` for
  both empty and contested.
- `6` **bolt** is a round in flight, one entry per round. See `0x87 Shots`.
- `7` **pickup** is a cargo box in a bubble. Also a volume you fly through — colliding
  with one would shove you off the course you took to reach it — and `radius` is the
  collection radius, so the bubble a client draws is exactly what the server tests
  against.

All three exist as synthetic entries so clients draw them through the machinery they
already have, with no new message and no separate cadence.

`team` travels with every body rather than in a side table because the client colours
ships and motherships by team, and a body can appear in a snapshot before any other
message has said who owns it. Each team has its own mothership, and a deposit only
counts at your own.

Radius is resent every snapshot rather than announced once at spawn. It is static per
entity, so this is redundant — but at one byte it costs ~3 KB/s at full load, and it
means a client joining mid-match needs no separate state resync to draw the world.

**Smallest-three quaternion packing:** drop the largest-magnitude component (it is
recoverable as `sqrt(1 - a² - b² - c²)`), store its index in the top 2 bits, then the
remaining three components each quantised to 10 bits over the range ±1/√2. Costs 4 bytes
instead of 16, with ~0.1° error — far below what is visible.

Budget: 200 bodies → ~4.6 KB/snapshot → ~69 KB/s per client at 15 Hz. Fine for LAN and
localhost.
Delta compression and interest management are deferred to Phase 6 and only if profiling
demands them (libspatial's octree already supports the radius query interest management
would need).

### `0x82` Event

Discrete, non-interpolated occurrences. Drives sound and HUD feedback.

| Field | Type | Notes |
|---|---|---|
| `event` | `u8` | See the table below |
| `entity` | `u32` | Subject |
| `player` | `u32` | Actor, `0` if none |
| `value` | `f32` | Deposited value / damage magnitude / see below |

| id | event | `player` | `value` |
|---|---|---|---|
| `0` | grabbed | actor | — |
| `1` | dropped | actor | — |
| `2` | deposited | actor | credits banked |
| `3` | damaged | actor | damage |
| `4` | destroyed (cargo worthless) | — | `0` |
| `5` | round start | — | round number |
| `6` | round end | **team id**, `0xFF` = draw | winning score |
| `7` | match over | **team id**, `0xFF` = draw | round wins |
| `8` | ship hit | shooter, `0` for a collision | damage |
| `9` | ship destroyed | killer | victim's team |
| `10` | ship respawn | — | team |
| `11` | asteroid hit | shooter | damage |
| `12` | asteroid destroyed | shooter | tier that broke |
| `13` | mothership hit | shooter | damage actually taken |
| `14` | mothership destroyed | shooter | **team id** whose station broke |
| `15` | shield absorbed | shooter | damage the shield ate |
| `16` | team eliminated | — | **team id** of the last crew standing |
| `17` | hill moved | — | the new hill's radius |
| `18` | item picked up | collector | the `ItemID` collected |
| `19` | item used | user | the `ItemID` spent |

For `5`–`7` and `9`–`10` the `player` field carries a **team id**, not a player id.

A shielded station emits `15` immediately before the `13` for the same hit, so a client
can flare the bubble instead of sparking the hull. A client that does not know about
shields still sees a normal hit and draws something sensible. Event `14` ends the round on
the spot — see `endRoundByBreach` — so a client will see the phase change to intermission
in the same breath.

### `0x83` TeamState

| Field | Type | Notes |
|---|---|---|
| `team_count` | `u8` | |
| per team | `u8` `team_id`, `f32` `score`, `u8` `member_count`, `u8` `name_len`, `u8[name_len]` `name`, `u8` `shields`, `u8` `turrets`, `u8` `lives`, `u8` `color` | |

Records are variable width because team names are length-prefixed (≤ 20 bytes).

In co-op mode `team_count` is 1 and every player is in it.

`shields` and `turrets` are the team's **station** upgrade levels. They travel here rather
than in `0x85 PlayerState` because they are shared by the whole crew and because a client
needs a *rival's* shield level to draw their bubble, not only its own. Every other
upgrade is personal and stays in `PlayerState`.

`color` is the palette entry the crew is wearing, `0`–`15`, set by `0x06 SetTeamColor`. It
defaults to the team id, which is the fixed mapping teams had before colour was a choice.
Everything drawn in a crew's colours — hulls, stations, lasers, the tractor beam, the
end-of-match table — reads through this one value.

`lives` is the crew's remaining respawns this round, reset when a round begins. It is
broadcast for every team on purpose: knowing a rival is down to its last ship is what
makes pressing an attack a decision rather than a guess. A team on zero lives with nobody
alive is out for the round, and when only one crew is left the round ends — see
`endRoundByElimination`.

### `0x84` MatchState

Broadcast at 2 Hz. Drives the round banner, the clock and the shop's visibility.

| Field | Type | Notes |
|---|---|---|
| `phase` | `u8` | `0`=warmup, `1`=round, `2`=intermission, `3`=match over, `4`=lobby |
| `round` | `u8` | 1-based once play starts |
| `best_of` | `u8` | |
| `wins_needed` | `u8` | majority of `best_of` |
| `time_left` | `u16` | seconds remaining in this phase |
| `winner` | `u8` | team id, `0xFF` = none or draw |
| `team_count` | `u8` | |
| per team | `u8` `team_id`, `u8` `round_wins` | |
| `mode` | `u8` | `0`=salvage, `1`=hoard, `2`=king of the hill |

`mode` is appended **after** the variable-length team block rather than inserted into the
header, so a decoder that stops at the end of the teams still parses everything it
understands. Clients that predate it should treat a missing trailing byte as salvage.

### `0x85` PlayerState

Sent **per session**, not broadcast — credits and upgrades are personal and a player
must not see what rivals can afford.

| Field | Type | Notes |
|---|---|---|
| `credits` | `f32` | |
| `upgrade_count` | `u8` | |
| per upgrade | `u8` `id`, `u8` `level` | |
| `energy` | `f32` | Current weapon charge, in shots |
| `max_energy` | `f32` | Magazine size, including the capacitor upgrade |
| `health` | `f32` | Hull, 0–1 |
| `respawn_in` | `f32` | Seconds until respawn; `0` when alive |
| `boost` | `f32` | Seconds of burn left in the tank |
| `max_boost` | `f32` | Tank size |
| `boost_cooldown` | `f32` | Seconds of forced stall after running dry; `0` normally |
| `grounded` | `u8` | `1` when the team's life pool is spent and no respawn is coming |
| `items` | `u8[3]` | Inventory. `0`=empty, `1`=missile, `2`=damage, `3`=speed, `4`=shield |
| `item_damage` | `f32` | Seconds of laser boost left |
| `item_speed` | `f32` | Seconds of thrust boost left |
| `item_shield` | `f32` | **Damage** the shield can still absorb — a pool, not a timer |

The trailing blocks are appended rather than inserted, so a decoder that stops at the end
of the length-prefixed upgrade table — or at the end of the combat block — still parses
the part it understands.

Sent at the snapshot rate, not the 2 Hz shop cadence — `energy`, `health` and `boost`
drive live HUD bars.

Boost is a tank, not a modifier: the client sends `flags` bit1 while the key is held, and
the server decides whether the engines actually run hot. A client must not assume that
holding the key means boosting — it reports `boost` for exactly this reason, and the
sound and the bar should follow the tank rather than the key.

`boost_cooldown` is non-zero only after the tank has been run all the way down. While it
counts, the tank does not refill and no burn can start at any charge level, so `boost` and
`boost_cooldown` are genuinely different states and should not be drawn the same way. A
client should also not treat a small non-zero `boost` as usable: the server requires a
floor (`sim.BoostReengageSeconds`) to *start* a burn, as opposed to continuing one.

### `0x86` ShopOffers

Sent per session at 2 Hz. This intermission's randomised choices; `0x04 BuyOffer` picks
one by slot, so a client cannot buy something it was never shown.

| Field | Type |
|---|---|
| `count` | `u8` |
| per offer | `u8` `upgrade`, `u8` `level`, `f32` `cost` |

### `0x87` Shots

Broadcast every tick a laser is fired, not on the snapshot cadence: a muzzle flash exists
for exactly one tick, and a dropped broadcast means it is never drawn at all.

| Field | Type | Notes |
|---|---|---|
| `count` | `u8` | |

Then `count` records of **5 bytes** each:

| Field | Type | Notes |
|---|---|---|
| `shooter` | `u32` | Entity id of the firing ship, or of a mothership for a turret |
| `team` | `u8` | Colours the flash |

**This message is only a muzzle flash.** It used to be the beam itself — 14 bytes carrying
a hit flag, a beam length and a target — back when lasers were hitscan and a shot's whole
life happened on the tick it was fired.

Rounds now travel — 420 m/s in a straight line, spent after 2000 m — so none of that can
be known at the trigger: whether it connects, how far it gets and what it hits are all
decided later, by `projectiles.go`. Leaving the fields in place carrying zeros was actively
harmful — a test read the always-zero hit flag and concluded that nothing in the game ever
hit anything.

The rounds themselves reach clients as **synthetic snapshot bodies** with `kind` `6`, one
entry per bolt in flight. Sending the real position each snapshot, rather than letting
each client integrate its own arc, is what keeps a near miss from being drawn as a hit.

The client draws the flash at the shooter's *current* interpolated pose, because a flash
at the ship you can see beats one at where that ship was 110 ms ago.

### `0x88` MatchResults

Broadcast **once**, on the transition to phase `3`, immediately before the `7` match over
event that sets the client's end-of-match sequence running. The match is decided by the
time this is sent and nothing in it can change afterwards, so it is sent on the transition
rather than on a cadence.

| Field | Type | Notes |
|---|---|---|
| `winner` | `u8` | team id, `0xFF` = draw. Same value as `MatchState.winner` |
| `player_count` | `u8` | |

Then `player_count` variable-width records:

| Field | Type | Notes |
|---|---|---|
| `player_id` | `u32` | |
| `team` | `u8` | |
| `name_len` | `u8` | |
| `name` | `u8[name_len]` | ≤ 20 bytes, already stripped of control characters |
| `delivered` | `u16` | Rocks banked over the whole match |
| `banked` | `f32` | Their total value |
| `kills` | `u16` | |
| `deaths` | `u16` | |
| `credits` | `f32` | Personal earnings, unspent |

Stats are **match totals**, not per round: they accumulate like credits rather than
resetting with the team scores, because a round you lost is still work you did.

Rows arrive **pre-sorted** — by team id, then by `banked` descending, then by name and id
to break ties. The server sorts because it iterates a Go map, so without it the same match
would draw a differently ordered table for every player watching it.

A delivery credits **every player with a beam on the rock**, each for the share they were
actually paid, matching how the payout itself splits. A ram emits a kill and a death for
both pilots; a station turret emits a death with no kill to credit.

This is the only message that tells you what other players have earned. `0x85 PlayerState`
is sent per session precisely so rivals cannot see what each other can afford in the shop —
but that is a mid-match concern, and once the match is over there is no shop left to open.

### `0x89` Roster

Who is connected. Broadcast **on change** — a join, a leave, or the match starting — not
on a cadence: seats change rarely, and a lobby that redraws itself fifteen times a second
for no reason is worse than one that redraws when something happens.

| Field | Type | Notes |
|---|---|---|
| `host_id` | `u32` | The only player a `0x07 StartMatch` is accepted from; `0` before anyone connects |
| `capacity` | `u8` | Seats per crew, so the lobby can draw the empty ones |
| `count` | `u8` | |

Then `count` variable-width records:

| Field | Type | Notes |
|---|---|---|
| `player_id` | `u32` | |
| `team` | `u8` | |
| `name_len` | `u8` | |
| `name` | `u8[name_len]` | ≤ 20 bytes, already stripped of control characters |

Rows are **pre-sorted** by team, then by player id — which is join order, so a pilot's row
does not jump around as other people arrive.

Names are here rather than in `0x83 TeamState` because TeamState is a per-crew record and
this is a per-player one. Before the lobby existed no message carried another player's
name at all, which is why a client could count its opponents but never name them.

`capacity` is `TeamSize`, except in co-op, where the single crew holds the whole match and
it is `TeamSize × TeamCount`. **Team capacity is enforced**: a join that would overrun it
is refused and the socket closed, since a lobby drawing a fixed number of slots must not
be able to seat more people than it has drawn.

### `0x8A` ChatSay

A message on its way out. Broadcast to the match room for channel `0`, and to the sender's
**team room only** for channel `1`.

| Field | Type | Notes |
|---|---|---|
| `channel` | `u8` | `0`=all, `1`=team |
| `team` | `u8` | The sender's crew, for colouring the name |
| `name_len` | `u8` | ≤ 20 |
| `name` | `u8[name_len]` | |
| `text_len` | `u8` | ≤ 120 |
| `text` | `u8[text_len]` | |

Name and team are attached here by the server from the sending session, never carried up
from `0x09`.

### `0x90` DebugStats — dev builds only

| Field | Type |
|---|---|
| `tick_ms` | `f32` |
| `body_count` | `u16` |
| `session_count` | `u16` |

---

## Debug parameter IDs

Used by `0x10 DebugSet`. Mirrored in `config/feel.toml` so a tuned feel is reproducible
rather than rediscovered.

| ID | Name | Default | Range |
|---|---|---|---|
| `0x0001` | `grab.spring` | 220.0 | 0–2000 |
| `0x0002` | `grab.damping` | 28.0 | 0–200 |
| `0x0003` | `grab.max_force` | 1800.0 | 0–20000 |
| `0x0004` | `grab.hold_distance` | 6.0 | 1–30 |
| `0x0005` | `grab.reaction_scale` | 1.0 | 0–2 |
| `0x0006` | `grab.range` | 15.0 | 5–200 |
| `0x0010` | `ship.thrust` | 1800.0 | 0–5000 |
| `0x0011` | `ship.torque` | 1400.0 | 0–6000 |
| `0x0012` | `ship.linear_damping` | 0.35 | 0–5 |
| `0x0013` | `ship.angular_damping` | 900.0 | 0–3000 |
| `0x0020` | `salvage.damage_threshold` | 12.0 | 0–50 |
| `0x0021` | `salvage.damage_scale` | 0.04 | 0–1 |

`grab.reaction_scale` is the single most important value in the game: it scales the
equal-and-opposite force the held mass exerts back on the ship, and is what makes a heavy
asteroid *feel* heavy. `1.0` is physically correct; the tuned value may not be.

`ship.torque` and `ship.angular_damping` are best tuned through what they produce rather
than directly: top turn rate is `torque / angular_damping`, and responsiveness is
`inertia / angular_damping` where the ship's inertia is `0.4·m·r²` = 192. Defaults give
~1.55 rad/s and a 0.21 s time constant.

`salvage.damage_threshold` is in m/s and has to move with `ship.thrust`, because what
matters is the fraction of top speed rather than the absolute number. It went 6 → 12 when
thrust was doubled to 1800, so that ordinary cruising does not shred cargo.
