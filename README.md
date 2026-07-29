# Asteroid Salvage

A 3D multiplayer space hauling game. Fly out from a mothership, grab asteroids, and haul
them home without smashing them. Built for 16 players in teams of 4, scalable down to
two, with a co-op mode where everyone shares one score pool.

The design reference is **R.E.P.O.** — its *feel*, not its content. Mass, momentum and
fragility are the whole game: you grab a thing, it fights you, and getting it home intact
is the challenge.

## Download and play

[![Download](https://img.shields.io/github/v/release/joormom/AsteroidSalvage?label=Download%20for%20Windows&style=for-the-badge)](https://github.com/joormom/AsteroidSalvage/releases/latest)

**[Download the latest build](https://github.com/joormom/AsteroidSalvage/releases/latest/download/AsteroidSalvage-win64.zip)** — Windows 10 or later, ~36 MB.

Unzip it anywhere and run **`AsteroidSalvage.exe`**. **Nothing to install**: no Python, no
Go, no toolchain, no launcher account. It keeps itself up to date — see
[Updates](#updates).

That link always points at the newest release, so it is the one worth sharing.

**To play together:** one player clicks HOST A GAME, sets the rules and presses START,
which launches the bundled server and **opens a lobby**. Everyone else types the host's LAN
address — shown along the bottom of that screen, with a COPY button — and clicks JOIN. The
lobby fills in on the right as people arrive, and the host presses BEGIN MATCH when the
crew is there.

Everything is on your own network; there is no account, no server to rent and nothing
phoning home except the update check.

## Architecture

| Layer | Language | Why |
|---|---|---|
| Physics | C11 ([lagrange](https://github.com/kevinfling/lagrange) + [libspatial](https://github.com/kevinfling/libspatial)) | Rigid bodies, forces, collision response |
| Simulation + netcode | Go, cgo to lagrange, [sprocket](https://github.com/kevinfling/sprocket) WebSockets | Authoritative rules, rooms map onto matches and teams |
| Client | Python + Panda3D | Renders and sends input. No physics, no prediction |
| Dev console | Python + [Dear ImGui](https://github.com/ocornut/imgui) via `imgui-bundle` | Live tuning of how hauling feels |

The server owns everything. The client draws server state ~110 ms in the past and
interpolates between snapshots; on localhost that delay is imperceptible and it keeps the
client honest.

```
client/ (Panda3D) ──input 30 Hz──►  server/ (Go)  ──cgo──►  vendor/lagrange (C)
                  ◄─snapshots 15 Hz─┘   │
tools/devconsole/ ◄──DebugStats─────────┘
                  ──DebugSet──────────►
```

## Setup

Requires a **GCC-compatible** compiler — cgo cannot use MSVC.

```bash
winget install GoLang.Go
winget install BrechtSanders.WinLibs.POSIX.UCRT   # MinGW-w64, NOT Visual Studio
pip install panda3d numpy imgui-bundle websockets

git submodule update --init --recursive           # pulls lagrange + libspatial
```

Verify: `gcc --version`, `go version`, `python -c "import panda3d"`.

## Updates

A packaged copy **checks for a newer release on startup and installs it when you quit**,
so a fix does not mean re-sending 36 MB and asking six people to unzip it again.

The design is mostly about failure modes. The check runs on a background thread with a
short timeout and every failure path is silent — offline, rate-limited, no releases
published yet, a malformed reply: none of those may stop somebody playing. Nothing is
swapped until a complete download has been extracted, so a connection dropped halfway
leaves the installed copy untouched. A running `.exe` cannot overwrite itself on Windows,
so the swap is done by a detached script that waits for the game to exit, copies over the
install and relaunches.

**Source checkouts are inert.** `is_frozen()` trusts `sys.frozen` and nothing else: an
earlier version guessed from the layout around `sys.argv[0]` and got it backwards under
`python -c`, which would have let the updater overwrite a working tree.

## Publishing a release

```bash
python build_dist.py              # just build: dist/AsteroidSalvage-win64.zip
python build_dist.py --release    # stamp a version, build, and publish to GitHub
```

`--release` bumps the latest tag, generates `client/buildinfo.py` from it, and uploads via
the `gh` CLI — so authentication is `gh auth login` rather than a token this repo stores.
The version lives in the release tag alone.

Publishing is what makes the [download link](#download-and-play) point at the new build,
and it is what every installed copy's updater notices.

The build needs Go, MinGW-w64 and Python on *your* machine — not on the players'.

Override toolchain locations with the `GO_BIN` and `MINGW_BIN` environment variables.

Note the game is asset-free by design: every model is generated in code. Panda3D's
bundled models are not carried into a frozen build, and depending on them meant the
packaged game died on a missing `models/misc/sphere`.

## Running

**Double-click `play.bat`.** It builds the server, then opens the game and the tuning
console. Click HOST A GAME to start a match with your chosen settings; closing the game
shuts the server down again, and the console reconnects on its own.

Or by hand:

```bash
cd server && go run .                 # server + dev console endpoint on :8080
python client/main.py                 # the game
python tools/devconsole/main.py       # tuning sliders
```

Server flags: `-addr :8080`, `-teams 4`, `-teamsize 4`, `-coop 1`, `-seed N`, `-dev=false`.

### Controls

| Input | Action |
|---|---|
| `W` / `S` | thrust forward / back |
| `A` / `D` | strafe left / right |
| **Mouse** | aim (there is no roll — point where you want to go and burn) |
| **Right-click** | use the selected item — or, on `BEAM`, the tractor beam: lock on and reel in |
| **Left-click** | fire the laser — the ringed crosshair shows where it goes |
| `Shift` / `Space` | boost / brake |
| `Alt` + `A` / `D` | barrel roll left / right |
| `Ctrl` + `W` / `S` | forward / backward roll |
| `1` / `2` / `3` | select an item slot |
| **Mouse wheel** | select `BEAM` or an item slot |
| `Enter` | chat |
| `Tab` | while chatting: switch `ALL` / `TEAM`. Otherwise release the mouse |
| `Esc` | pause (Resume / Settings / Leave Game / Exit Game) |

**This table is no longer printed across the bottom of the screen.** It was reference
material — read once, then permanent clutter under the crosshair, sharing that corner with
the status bars and the item hotbar. It lives in the settings screen, which is where you
go when you want to look something up.

**Every key in it can be changed**, in Settings -> Controls: click a key, press the one you
want. See below.

## Settings

Three doors rather than one page: **Sound**, **Graphics** and **Controls**. One screen with
everything on it had grown to four unrelated concerns stacked down the page, and the
control reference at the bottom had become a table you scrolled past to reach the volume.

Choices are saved to `settings.json` next to the player profile, and a command-line flag
still wins over a saved one for that run — a flag is an explicit instruction, a saved
setting is a preference.

| Screen | What is on it |
|---|---|
| **Sound** | Sound and music volume. Applied as you click, not on the next match |
| **Graphics** | Fullscreen, space dust, antialiasing, vertical sync |
| **Controls** | Mouse sensitivity, invert Y, and every key |

Fullscreen and dust apply immediately. **Antialiasing and vertical sync apply on the next
launch** and the screen says so — both are chosen when the window is created and cannot be
changed on a live one. Dust is the only part of the scene that is pure decoration, which
is what makes it the one graphics option that can be turned off without changing what you
can see of the game.

### Rebinding

Click a key, press the one you want. Escape cancels rather than binding Escape.

Bindings live in one registry (`client/keybinds.py`) that both the flight controls and the
game's own keys read from. They used to be hard-coded in two places, which meant "the
controls" were a list in three files and rebinding one would have quietly desynchronised
the others.

A key can only do one thing, so **binding a key that is already taken unbinds the other
action** rather than refusing. Refusing would mean anyone wanting to swap two keys has to
find a spare one to park the first on; this way the displaced action shows as `- unset -`
and is obviously the next thing to fix.

Escape and the mouse buttons are **reserved**. Escape is the way out of every screen
including that one — bound to something else, a player who made a mess of their controls
would have no way back to fix them. The mouse buttons are wired into pointer capture and
into the item/beam split, and binding "fire" onto "use item" would be a mess with no way
back.

Saved to `keybinds.json` the moment one changes: a rebind is fiddly enough that losing it
to a crash would be maddening.

## Ship editor and achievements

The main menu's **Ship Editor** customises your ship: boosters, hats, booster effects and
tractor-beam colours. Most are locked behind achievements ("Win a match", "Deliver a
MASSIVE asteroid"), shown greyed out with the requirement rather than hidden — an item
you can't see gives you no reason to chase it.

**A crew's colour comes with the crew.** There is no separate colour picker: picking a team
already picks a colour, and two settings for one decision meant you could choose Team B and
then paint it red, which is what Team A looks like. Everything that draws in team colours —
hulls, stations, lasers, the tractor beam, the lobby, the end-of-match table — follows from
one shared mapping. The sixteen-colour palette is still on the wire (`0x06 SetTeamColor`);
nothing in the front end sends it.

**Boosting is visible from outside the ship.** The plume rides on the snapshot as a body
flag, so every client draws every ship's burn, and it never appears for a pilot holding
the key on an empty tank — the server decides who is actually running hot.

Cosmetics are purely visual and apply to **your own ship only**; loadouts aren't on the
wire, so other players see a stock hull. Progress lives in
`%LOCALAPPDATA%\AsteroidSalvage\profile.json`, tracked client-side from game events —
unlocks are personal and cosmetic, so this needs no protocol changes and can't affect
anyone else's match.

The tractor beam is short range — **15 m** (`grab.range`) — so you fly up to a rock and
take it rather than vacuuming the field from a standoff. It locks onto whatever is
closest to your crosshair, selecting by *aim angle* rather than proximity, so a nearer
rock off to the side can't steal the lock from the one you lined up on.

`grab.range` and `grab.hold_distance` interact: cargo is held at
`hold_distance + both radii`, which is already ~13.5 m for the biggest asteroids. Raising
one usually means raising the other.

The **green arrow** floating above your ship always points at the mothership. Grab a rock,
follow the arrow home, and it banks automatically once you or your cargo is inside the
60 m capture radius — the HUD counts you down and switches to `DELIVERING...`.

### Asteroid tiers

| Tier | Colour | Per rock | Per kg | Notes |
|---|---|---|---|---|
| rubble | grey | 16 | 8.2 | clutter |
| iron | rust | 211 | 7.9 | bread and butter |
| crystal | cyan | 325 | **135** | light and lucrative — the efficiency play |
| gold | yellow | 879 | 19 | dense for its size |
| massive | dark red | **2891** | 4.7 | **needs two beams** |

A massive asteroid will not move for a lone stock beam *at all* — bring a teammate, or
buy the Tractor Amplifier. Payout splits between everyone with a beam on it, so helping
is never charity.

### Bolts travel, and they run out

Lasers are no longer hitscan. Rounds leave the barrel at **420 m/s** and fly **straight**,
which makes aiming a question of leading a moving target and nothing else. A ship crossing
at 40 m/s moves most of its own length in the 0.7 s a bolt takes to cross 300 m, so range
costs accuracy in a way a damage falloff never conveys.

Rounds used to arc downward as well, 22 m/s² along world -Z. That is gone: a shot goes
exactly where it is pointed. There is no gravity out here, and a reticle that is wrong
about elevation turns a long shot into a guess at how much to hold over rather than a read
of where somebody is going.

What bounds range instead is a hard limit — a round is **spent after 2000 m** and simply
stops existing. That is a little under a fifth of the map and well past any range a fight
happens at, so in practice it only catches shots fired into open space. It is measured as
distance actually flown, not as a lifetime, since muzzle velocity is added to the hull's
and a round fired from a fast ship would otherwise quietly reach further.

Bolts are not physics bodies: they are swept segments tested against the same objects the
old raycast used, and they reach clients as synthetic snapshot entries. Sending them
rather than letting clients integrate their own flight is what keeps a near-miss from
being drawn as a hit.

`0x87 Shots` is now only a muzzle flash — shooter and team, five bytes. It used to carry
length, a hit flag and a target; none of that can be known at the trigger any more, and
leaving the fields carrying zeros immediately misled a test into asserting that nothing
ever hit anything.

### Combat and the siege

Everything that can be shot is measured against a **30-point hull** and a **4-damage**
stock laser: eight hits to kill, which is more than the six-shot magazine holds, so a kill
costs a reload and disengaging is always an option. Rocks are exempt from that rescaling —
`toughnessScale` in `tiers.go` keeps every asteroid at the time-to-break it was tuned for,
so moving the guns cannot silently retune the whole belt.

A **mothership has 500 health and can be destroyed**, which is the other way to win a
round — and the fastest one. At 4 damage a shot that is 125 hits: about a hundred seconds
for a single stock pilot, and a handful of seconds for a four-player crew with maxed
lasers. **Breaching a station knocks that crew out of the round** — and only that crew. Their
ships go with the base, they cannot respawn, and everyone else carries on; the round ends
when one crew is left, exactly as a wipe-out does. It used to end the round outright,
which with four teams on a map meant one siege ended two other crews' round for reasons
that had nothing to do with them. Stations are rebuilt between rounds, so a successful
siege does not hand you every round after it.

**Stations cannot be damaged at all in King of the Hill.** That round is decided by
holding ground and nothing else — a breakable base would be a second, unrelated way to end
it. For the same reason, respawns are free in that mode and being wiped out does not end a
round: points are the only win condition.

At this health a station is genuinely vulnerable: leaving home undefended while the whole
crew works the belt is a real risk, and the two station upgrades are the answer rather
than a luxury.

Your own station is never a target — a stray shot lining up a delivery cannot chip the
thing you are delivering to — but an enemy station is solid, and will stop a beam that
would otherwise have crossed the map.

The two station upgrades are the answer to being sieged. Shields show up in the world as a
team-coloured wireframe bubble, so you can see what you are flying into; turrets announce
themselves by shooting. Turrets reach 220 m, which is outside the deposit radius but well
short of the belt, so they punish someone who came for the station and never plink at
somebody working.

## The lobby

Hosting holds the server in a lobby instead of running a warmup clock down into round
one, so a crew can assemble before anything is being scored. The host screen stays up
while it fills: settings on the left, the lobby on the right, and the address you have to
read out along the bottom where it is not competing with anything.

**Your team** is picked on the same screen — `Auto` by default, since in a four-player game
nobody wants to negotiate crews. It rides in the Hello, so it applies when joining someone
else's game too: the server honours it while that crew has a seat and falls back to the
emptiest one when it does not. Whichever crew you end up on is banded and marked `YOU` in
the lobby, because everyone's name looks the same on that panel, including yours.

And you can **change crew from the lobby** — click any other crew's header to move, host
included. That is where the decision actually belongs: you can see the crews filling, so
you can even them out. Full crews say `FULL` rather than being silently refused.

Switching is **lobby-only**. Mid-match it would let somebody join whichever crew is
winning, and would strand everything their old crew was counting on them for — their
cargo, their share of the life pool, the station upgrades they helped pay for. The lobby is
the one moment a crew has no state to abandon.

Every crew shows **its seats, numbered and filled or empty**, so the shape of the match is
visible before it starts rather than inferred from a headcount — and a seat has an address
you can say out loud: "Team C, slot 2".

Auto-assign fills **breadth-first**: the first player takes Team A slot 1, the second Team
B slot 1, the third Team C, the fourth Team D, and the fifth comes back around to Team A
slot 2. Four crews filling evenly is what makes an early game playable rather than
three-on-one. **Team size is now enforced** —
`Players per team` used to be a preference that the "put them on the smallest team"
fallback quietly overran, which meant a lobby drawing four slots could seat five. A join
with no seat left anywhere is refused.

The host wears a **crown**, and is simply the first player to connect: a hosting client
launches the server and connects before anyone else has the address, so no secret has to
be shared between a process and the client that spawned it. Only they can start the match,
and the crown does not move if they leave — handing it to whoever was next would let a
joiner start a match somebody else was still setting up.

Joiners see the same lobby, without the settings column: those belong to whoever launched
the server, and a row of cyclers that changed nothing would be a worse lie than not
showing them.

## Cargo boxes

Green bubbles drifting around the map with a crate in the middle. **Fly through one** to
take what is inside — it is the only reward in the game you get by flying rather than by
shooting or hauling, and it is the only one a fight cannot be won without leaving to
collect.

Every box looks the same. You do not know what is in one until you have it, so going for a
box is a decision about position rather than about shopping. Once it is yours it takes its
own colour and its own icon in one of three slots along the bottom of the screen.

| Item | Effect |
|---|---|
| **Missile** | one shot, **25 damage**, and it **chases** — five laser hits in one, but not a kill on a full hull |
| **Overcharge** | **+50% laser damage** for 30 seconds |
| **Afterburner** | **+50% thrust** for 30 seconds |
| **Shield** | absorbs the next **30 damage**, and then it is gone |

Each answers a different question rather than being a bigger number than the last:
Overcharge rewards already being in a fight, Afterburner rewards not being in one yet, and
the Shield is the only one worth holding rather than spending.

**Selecting and firing are separate.** `1`-`3` and the wheel *select* a slot;
**right-click commits**. Firing on the key press meant a missile left the rail the instant
it was chosen, with the ship pointing wherever it happened to be pointing — there was no
moment in which to aim it. One rule for every item, and the missile gets a moment.

The fourth selection is `BEAM`, and it is the default — with nothing selected, right-click
is the tractor beam it has always been. Without that state, picking up an item would
silently take your beam away and the first you would know is a rock you failed to grab.
The wheel cycles `BEAM → 1 → 2 → 3`, so getting back to the beam is never more than three
clicks in one direction.

### The missile chases

It **locks on at launch** — the enemy ship closest to where you were pointing, by aim angle
rather than by distance, so a nearer ship off to the side cannot steal the lock. Nothing in
the cone means it flies straight rather than refusing to launch. It never re-acquires: a
missile that kept shopping for a better target would be impossible to bait, and baiting one
is the interesting half of being shot at.

It is also **dodgeable**, and one number decides that: it can bend its course by **1.6
rad/s** and no faster. At nearly three times a boosting ship's top speed it can always
catch up, so dodging is never about outrunning it — it is about making it turn. Break
across its path late and it overshoots, then has to come back around, and its range cap
eventually spends it.

**Shields do not stack.** Using one while another is up replaces what was left rather than
adding to it, so the pool is always 30 and never a number that climbs — spending a second
shield early is a choice to waste some of the first. It draws as its own bar **above the
hull**, since that is the layer damage reaches first, and only while one is actually up.

Three slots, and a fourth box is refused rather than overwriting one — a box you flew
through and did not get is annoying, but a missile that vanished because you clipped an
Afterburner on the way to a fight is worse. **Dying spends everything**, carried and
running: an item is a window, and one that survives a respawn is a permanent upgrade with
extra steps.

A missile's 25 damage is fixed. It does not scale with the laser upgrades or with
Overcharge, so what it is worth never depends on what else you happen to be holding.

## Chat

`Enter` opens the composer, `Esc` cancels, and **`Tab` switches between `ALL` and `TEAM`
while you are typing** — you start typing and then decide who hears it, rather than
picking a channel first and discovering afterwards that you sent it to the room.

Team messages go to the team room on the server and nowhere else. The sender's name and
crew are attached by the server from the session, never carried up from the message, so no
client can put words in somebody else's mouth or claim a crew it is not on.

The composer takes the keyboard while it is open; without that, typing "was" would thrust,
strafe and brake on the way past. Anything held when it opened is released, so a key does
not stay down for as long as the message takes to write.

## Game modes

Chosen on the host screen, which swaps in that mode's own settings when you pick it.
Everything else — flight, the map, combat, lives, the match structure — is shared, so a
mode feels like the same game rather than a different one.

| Mode | How you score | Its own settings |
|---|---|---|
| **Salvage** | Tractor rocks home and bank them at your station | — |
| **Hoard** | No hauling: **fly into rocks to eat them** | points to win a round, rock value |
| **King of the Hill** | **Hold a marked zone**, a point a second | points to win, points per second, credits per second, how often the hill moves |

**Hoard** is the scramble. No trip home and no cargo to protect, so the round is a race to
be where the good rocks are. Rock integrity still counts, so one somebody has been
bouncing off the scenery is worth less. `-mode hoard -hoard-target 4000 -hoard-bonus 1.6`

**King of the Hill** is the territory game: a 130 m zone somewhere on the map, and a crew
scores for every second they are the only crew inside it. Three rules carry the mode —
presence rather than kills, so a crew losing the shooting can still win by being
somewhere; **contested means nobody scores**, because if the leader still scored while
contested the correct play against them would be to leave; and the hill relocates on a
timer, since a fixed one becomes an entrenched position and a queue of people flying into
it. Holding also pays **20 credits a second to every member of the crew** — not only the ship
parked in the volume, since escorting and chasing people off it is the other half of the
job. Round points win the round and reset with it; credits are personal and last the
match, so a crew that has already lost a round on the clock still has a reason to contest
the hill. `-mode koth -koth-target 500 -koth-rate 7 -koth-credits 20 -koth-shift 45`

The zone is a volume you fly *through* — giving it a collider would make holding it a
matter of bouncing off it — so it exists only as a synthetic body in the snapshot, drawn
as a wireframe shell in the holder's colour and neutral white while empty or contested.

### Nebula and dust

Speed is only legible as parallax, and parallax needs something close. The starfield is a
shell parented to the camera so it never shifts, and asteroids are hundreds of metres out —
at 40 m/s the screen was almost completely still.

Two layers fix it: **dust**, a few hundred motes in a small box that follows the ship and
snaps to a lattice of its own size, so the motes hold still in *world* space and stream
past the canopy instead of travelling with you; and **nebula**, seven enormous additive
clouds ringing the play volume that slide against the starfield as you cross the map.

### The map

**A new map every match.** The seed defaults to a fresh one each run and is logged, so a
map worth keeping can be replayed with `-seed`.

Every map is built around **one enormous centrepiece** — 260–400 m, roughly ten times a
mothership — and which of the four structures it is changes from match to match. That
single choice is what makes a map a place rather than a scattering: the belt is worked
around it, and it is visible from every station on the ring. Nine smaller structures are
scattered outside the mothership ring.

The four structures: **battlestations** (a cracked armoured
sphere with a dish, cheerfully derivative), **capital wrecks** snapped in half, **planet
chunks** with the core still glowing through the crust, and **ruined ring stations**. Nine
laid out by rejection sampling so nothing overlaps a hangar, another structure, or the
centrepiece — and asteroids will not generate inside the landmark, where they would be
unreachable.

They are static, indestructible and **solid — a laser stops at one**. That is the point:
they are cover to break line of sight behind and landmarks to navigate by, in a volume
that was otherwise uniform in every direction. Generation is fully deterministic in the
seed, so a host can share a map and a generation bug can be reproduced rather than hunted.

### Aerobatics

`Alt` turns `A`/`D` into a barrel roll about the nose; `Ctrl` turns `W`/`S` into a
forward or backward roll. Both **repurpose the movement keys** rather than adding new ones,
so your hand stays where it is — and you cannot strafe while rolling or thrust while
flipping, which is the honest trade. A ship doing an aerobatic manoeuvre is committing to
it.

There is a subtlety worth knowing if you rebind anything: Panda3D normally folds held
modifiers into event names, so `Ctrl`+`W` arrives as `control-w` and a plain `w` binding
never fires. That is switched off, which is also what makes `Shift`-to-boost work while
already thrusting.

### Flying into things

Above **12 m/s** — the same speed cargo starts taking impact damage at — a collision hurts
the hull, and it scales: **every metre per second over twelve costs a hull point**. A
30 m/s scrape is 18 of your 30 points and survivable; a ship tops out near 43 m/s, so
**flat out into anything solid is death**.

Rocks, cargo, scenery and your own station all count. Your own station especially: it is
the thing you fly at fastest and most often, and being the one solid object you could
belly-flop into for free made docking at a hundred metres a second the correct way to
deliver.

Damage lands **once per collision, not once per tick of contact** — half a second of
immunity afterwards. Without that, holding thrust against a rock dealt a third of a hull
thirty times a second, and a pilot who bumped something and did not instantly reverse
simply died. Item shields absorb it like any other damage.

Every hit — laser, missile, collision, or a station shield eating a round — now throws a
**spark on the surface that was struck**, sized by the damage. Where the shot's origin is
known the flash is offset onto the near face rather than the body's centre: a flash inside
a 40 m asteroid is a faint glow somewhere in the middle of it, which reads as nothing at
all.

### Ramming

Flying your hull into something at speed is a weapon, above a 10 m/s closing speed so a
nudge while you both reach for the same rock is still just a nudge.

| You hit | Result |
|---|---|
| An enemy ship | **both** ships are destroyed |
| An enemy station | **15 damage** to it, and your ship is gone |
| A teammate | nothing — you bounce |

It always costs you the ship, which is what stops it replacing the laser. A station has
500 health, so ramming one down would take 34 ships and nobody has that many; what it buys
a crew already shooting a station is a few seconds off the clock.

### Lives

Each team shares a pool of respawns per round — **6 to 20, twelve by default**, set on the
host screen or with `-lives`. Every death spends one, whoever it was and however it
happened, so the pilot who keeps ramming is spending everybody's budget.

Spend the last one and you are **out for the round** — no countdown, because none is
coming. When only one crew still has anyone able to fly, the round ends and goes to them,
exactly like a station breach. Lives reset every round, so a round lost on lives costs you
the round rather than the match. Everyone can see everyone's remaining count on the
scoreboard: a rival down to their last ship is information worth acting on.

### Boost is a tank with a stall

Holding boost drains a four-second tank that refills over seven. Running it **all the way
down stalls it for three seconds** — nothing refills, and no burn can start — and starting
a burn afterwards needs a real second of charge in it rather than a drop.

Both rules exist for the same reason. With only a lockout-until-release, a pilot who
tapped the key kept re-engaging on whatever had trickled in since the last tick, so boost
was never actually unavailable; it just stuttered. The HUD draws four states, because
pressing the key does something different in each: burning, ready, charging-but-not-yet-
usable, and stalled with a countdown.

### Match structure

Best of 5 rounds, 3 minutes each, majority takes the match. Team scores reset every
round; **personal credits and upgrades persist**, which is what makes a round you lost
still worth playing. Between rounds a 45-second intermission opens the shop — press
`1`–`4` to buy:

| Upgrade | Effect | From |
|---|---|---|
| Tractor Amplifier | Beam counts as two — solo a massive | 1200 cr |
| Engine Boost | +25% thrust per level (×3) | 400 cr |
| Beam Extender | +8 m reach per level (×3) | 350 cr |
| Cargo Dampeners | Cargo takes 35% less damage per level (×2) | 500 cr |
| Laser Focuser | +40% damage per level (×3) | 450 cr |
| Capacitor Bank | +2 shots per charge (×3) | 400 cr |
| Fast Recharger | Charge refills 20% quicker per level (×3) | 380 cr |
| **Station Shields** | **TEAM**: your mothership takes 20% less per level (×3) | 900 cr |
| **Station Turrets** | **TEAM**: your mothership shoots back, one gun per level (×3) | 1000 cr |

The last two are the only things in the shop you buy for the crew rather than for
yourself. They count up on the *team*, so a shield a crewmate paid for in round two is
still there for someone who joins in round three, and two people buying it does not get
you two shields.

When the match ends, the losing stations blow up and then a **results table** takes the
screen: round wins per crew, and a row per pilot — delivered, banked, kills/deaths and
credits, with your own row picked out in your crew's colour. Stats are match totals rather
than per round, because a round you lost is still work you did. A rock two people hauled
counts as a delivery for both, each worth the share they were actually paid, which is how
the payout already splits.

This is the one message that shows everyone's earnings. During a match they are sent per
session precisely so rivals cannot see what each other can afford in the shop — but that
is a mid-match concern, and by the time this table appears there is no shop left to open.

Server flags for pacing: `-bestof 5 -round 180 -intermission 45 -warmup 15 -lives 12`, or
`-sandbox` for one endless round with no clock or shop. `-lobby` holds the server in a
lobby until the host starts it, which is what HOST A GAME passes.

**HOST A GAME owns the server.** It launches one with the settings on that screen, so
those settings only apply to a server it started. If something is already listening on the
port it now says so and refuses, rather than quietly connecting to the other server and
playing its match — which is what made "round length does nothing, it is always three
minutes" happen: `play.bat` used to pre-start a server, and the menu's copy could never
bind.

Client flags: `--name`, `--team`, `--sensitivity`, `--invert-y`.

## Testing

```bash
cd server && go test ./...                      # physics, sim, netcode
go run ./bots -n 16 -seconds 60                 # 16-player load test
python client/smoke_test.py                     # Python client vs the real Go server
python client/playtest.py                       # full loop: grab a rock, deliver it
python client/soaktest.py --seconds 300         # connection stability over time
python client/protocol_test.py                  # Python decoders vs Go's literal bytes
python client/itemtest.py                       # fly through a cargo box, spend the item
python client/chattest.py                       # three clients: ALL vs TEAM routing
python tools/devconsole/main.py --frames 120    # console renders without error

# See what the game actually looks like, without a human at the keyboard:
python client/main.py --demo --shot out.png --shot-frames 300 --shot-hauling
python client/main.py --host --shot bin/lobby.png --shot-lobby --shot-frames 600
python client/main.py --join 192.168.1.42 --shot bin/joined.png --shot-lobby
python client/main.py --url ... --demo --shot bin/results.png --shot-results
python client/main.py --host --demo --demo-items --shot bin/hotbar.png  # items in hand
python client/main.py --host --shot bin/chat.png --shot-chat            # log + composer
python client/preview.py --model mothership --out bin/ms.png   # a model on its own
```

`--demo` autopilots using the same body-relative steering the mouse produces, and
`--shot-hauling` waits until cargo is on the beam before capturing — so a screenshot
shows the mechanic rather than a ship parked at spawn. Capturing the desktop from
outside is unreliable; it photographs whichever window is on top.

`--shot-lobby` opens a real lobby and photographs it rather than starting the match, so
the roster can be checked with players actually in it — point `go run ./bots -n 6` at the
same server to fill the seats. `--join` is the mirror of `--host`: it drives the menu's
JOIN button rather than connecting behind the menu's back, which is the only way to
exercise what a joiner actually sees. `--url` skips the front end entirely and cannot.

The smoke test matters more than it looks: Go and Python implement the wire protocol
independently, and drift between them is the most likely bug in this project.
`TestPythonEncodersDecodeInGo` pins the other direction with literal bytes.
`protocol_test.py` covers the messages a running match never exchanges — a match has to
*end* before `0x88` is sent and a lobby has to be *opened* before `0x89` is, so neither
would ever be reached by the smoke test.

## Tuning the feel

Grab feel cannot be unit-tested. Run the server with `-dev`, open the console, fly, and
drag sliders while playing — changes apply on the next tick with no restart. The server
writes `config/feel.toml` on Ctrl-C so a good session is kept.

The parameter that matters most is **`grab.reaction_scale`**: how much of the cargo's
reaction force your ship feels. Physically it should be `1.0`. Whether that is what
*feels* right is the entire question.

Ships run at **`ship.thrust` 1800**, which is twice what they used to: about 43 m/s
unladen, 86 m/s on boost. Doubling thrust rather than halving damping was deliberate —
it lifts top speed *and* acceleration while leaving the response time unchanged, where
cutting drag would have doubled the top speed and made the ship floaty to start and slow
to stop.

Two thresholds are expressed in m/s and had to move with it, because what matters is the
fraction of top speed rather than the absolute number: `salvage.damage_threshold` 6 → 12,
so ordinary cruising does not shred cargo, and `RamSpeedThreshold` 10 → 20, so an
incidental bump between rivals is not a double kill.

Current balance: the heaviest asteroid leaves ~49% of unladen mobility — heavy, but still
flyable. `TestCarryingHeaviestRockSlowsButDoesNotStopTheShip` guards that. It was ~34%
before the engines were doubled: more thrust makes the biggest rocks noticeably lighter
work, which is the one balance consequence of the speed change worth watching.

## Notes on the vendored libraries

Three upstream issues were worked around rather than patched, since these are submodules:

- **`lagrange/math_simd.h` does not compile on plain x86-64.** It only includes the Intel
  intrinsics headers under `__AVX2__` but selects the `__m128` path under `__SSE__`
  (always defined on x86-64), and calls `_mm_rsqrt_ss` with two arguments when it takes
  one. `server/physics/bridge.c` includes `lagrange/sim.h` directly instead of the
  umbrella header, which avoids that file entirely. Orbital mechanics and libspatial
  broadphase would need it fixed.
- **`lagrange`'s broadphase is O(n²)** (`lg_broad_phase`, sim.h:711) and its entity lookup
  is a linear scan. Measured anyway: 1000 bodies costs 1.97 ms against a 33 ms budget, so
  libspatial broadphase is not needed until roughly 2000 bodies.
- **`sprocket`'s `Room.BroadcastBinary` does not deliver.** It enqueues via
  `writeSharedNoSignal` and then wakes workers with nil sentinels, which `writeWorker`
  treats as no-ops; nothing ever flushes those queues. `server/net/server.go` broadcasts
  by iterating `Room.Members()` and calling `Session.WriteBinary`, which works and also
  copies the payload (letting the snapshot buffer be reused).
- **`sprocket` drops every connection after ~60s.** Its gws transport has empty `OnPing`
  and `OnPong` stubs (`transports/gws/gws.go:73,75`), so the server never answers a
  client's ping *and* never records a pong. A session's read deadline is only extended
  by the pong handler (`session.go:241-246`), so it expires at `PongWait` no matter how
  much traffic flows, and `pingWorker` separately reaps the session once `lastPong` ages
  out. The server sets `PongWait` beyond any real session to disable both, and
  `runKeepalive` refreshes read deadlines from actually-received messages instead —
  better liveness for a game where clients send input 30×/s. The client also passes
  `ping_interval=None`, since its pings would otherwise go unanswered.
- **`lagrange`'s contact normals are inverted relative to its resolver.** The narrow
  phase builds the normal A→B while `lg_resolve_contact` assumes B→A, so approaching
  bodies are treated as separating and ignored, then attracted once they overlap. Every
  collider is flagged as a trigger to disable lagrange's pass, and `bridge.c` resolves
  sphere contacts with a consistent convention.

## Layout

```
vendor/lagrange/      C physics submodule (contains libspatial)
server/
  physics/            cgo bridge — bulk API over lagrange's per-entity linear scans
  sim/                game rules: ship control, grab, fragility, scoring
  net/                wire protocol + sprocket server + Go client
  bots/               headless load-test clients
client/               Panda3D client
tools/devconsole/     ImGui tuning console
shared/               protocol.md (authoritative) + its Python implementation
config/feel.toml      tuned values
```

## Status

Playable end to end: assemble in a lobby, fly, grab, haul, bank, shoot, break rocks open
for their cores, collect cargo boxes and spend what is in them, talk to your crew, siege a
rival's station, waves escalate, teams score, co-op works, and the match finishes on a
results table. 16 concurrent bots sustain 15 Hz with zero errors.

Not built yet: ready-up (the lobby shows who is here, but nobody marks themselves ready —
the host just starts it), client-side prediction, and interest management (deferred until
profiling demands it).
