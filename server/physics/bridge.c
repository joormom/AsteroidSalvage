/*
 * bridge.c — the single translation unit that instantiates lagrange.
 *
 * lagrange is entirely `static inline`, so LAGRANGE_IMPLEMENTATION is a no-op; the real
 * job here is to give cgo externally-linked symbols to call, and to keep the hot path
 * bulk rather than per-entity (lg_storage_find is a linear scan).
 */

/*
 * Deliberately NOT <lagrange.h>.
 *
 * The umbrella header drags in transfer.h -> patched_conic.h -> particle.h ->
 * math_simd.h, which does not compile on plain x86-64: it only includes the Intel
 * intrinsics headers under __AVX2__, but selects the __m128 code path under __SSE__
 * (always defined on x86-64), and calls _mm_rsqrt_ss with two arguments when it takes
 * one. It also drags in spatial_index.h -> libspatial.
 *
 * sim.h transitively provides everything this game needs (world, body, collider,
 * transform, integrator, gravity) and is self-contained. Revisit if orbital mechanics
 * or libspatial broadphase are ever wanted -- both would need those headers fixed.
 */
#define LAGRANGE_IMPLEMENTATION
#include <lagrange/sim.h>

#include "bridge.h"

#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <time.h>

#define AG_COLLISION_CAP 4096u

struct ag_world {
    lg_world_t* w;

    /* Collisions observed during the most recent step. Ring buffer so a pathological
     * tick drops the excess rather than growing without bound. */
    ag_collision collisions[AG_COLLISION_CAP];
    size_t collision_count;
    size_t collisions_dropped;

    double last_step_ms;
};

/*============================================================================
 * Timing
 *===========================================================================*/

static double ag_now_ms(void) {
    struct timespec ts;
    if (timespec_get(&ts, TIME_UTC) != TIME_UTC) return 0.0;
    return (double)ts.tv_sec * 1000.0 + (double)ts.tv_nsec / 1.0e6;
}

/*============================================================================
 * Collision capture
 *
 * lagrange invokes this once per contact, before contact resolution (sim.h:767),
 * so the velocities read here are the true pre-impact velocities — which is exactly
 * what salvage damage should be computed from.
 *===========================================================================*/

static void ag_record_collision(ag_world* self, lg_entity_t a, lg_entity_t b, float speed) {
    if (self->collision_count >= AG_COLLISION_CAP) {
        self->collisions_dropped++;
        return;
    }
    ag_collision* c = &self->collisions[self->collision_count++];
    c->entity_a = (uint64_t)a;
    c->entity_b = (uint64_t)b;
    c->rel_speed = speed;
    c->_pad = 0.0f;
}

/*============================================================================
 * Collision resolution
 *
 * WHY THIS IS HERE INSTEAD OF lagrange's
 * --------------------------------------
 * lagrange's narrow phase builds the contact normal pointing from A to B
 * (lg_collide_spheres: delta = pos_b - pos_a, sim.h:37), but lg_resolve_contact assumes
 * it points from B to A -- its own comments say "Correction is along normal (from B
 * toward A)" (sim.h:544) and "Body A gets pushed along normal" (sim.h:515).
 *
 * With the normal actually pointing A->B, an approaching pair computes
 * vel_along_normal > 0, which the resolver reads as "separating" and returns early
 * without resolving anything. Once the bodies have passed through each other and are
 * genuinely separating, the sign flips and the impulse pulls them back together.
 *
 * The visible result is that nothing bounces properly and a ship flies straight through
 * the middle of the mothership -- while still exchanging some momentum, which makes it
 * look superficially like collisions work.
 *
 * So: every collider is marked as a trigger, which makes lagrange's own narrow phase
 * skip it entirely (sim.h:566), and the sphere-sphere case is resolved here with a
 * consistent convention. Every body in this game is a sphere.
 *
 * Not carried over: angular impulse from contacts (rocks do not gain spin from being
 * hit) and Coulomb friction. Both are fidelity, not correctness, and neither is missed
 * in a frictionless vacuum.
 *===========================================================================*/

#define AG_SOLVER_ITERATIONS 4
#define AG_PENETRATION_SLOP 0.02f
#define AG_CORRECTION_PERCENT 0.6f

static void ag_resolve_collisions(ag_world* self, bool record) {
    lg_storage_t* s = &self->w->storage;

    for (size_t i = 0; i < s->count; i++) {
        const lg_collider_t* ca = &s->colliders[i];
        if (ca->type != LG_SHAPE_SPHERE) continue;

        for (size_t j = i + 1; j < s->count; j++) {
            const lg_collider_t* cb = &s->colliders[j];
            if (cb->type != LG_SHAPE_SPHERE) continue;

            lg_body_t* ba = &s->bodies[i];
            lg_body_t* bb = &s->bodies[j];

            float inv_a = ba->inv_mass;
            float inv_b = bb->inv_mass;
            float inv_sum = inv_a + inv_b;
            if (inv_sum <= 0.0f) continue; /* two static bodies */

            lg_vec3_t pa = s->transforms[i].position;
            lg_vec3_t pb = s->transforms[j].position;

            /* n points from A toward B, and everything below is consistent with that. */
            lg_vec3_t delta = lg_vec3_sub(pb, pa);
            float dist_sq = lg_vec3_len_sq(delta);
            float radius_sum = ca->sphere.radius + cb->sphere.radius;
            if (dist_sq > radius_sum * radius_sum) continue;

            float dist = sqrtf(dist_sq);
            lg_vec3_t n = (dist > 1e-6f) ? lg_vec3_scale(delta, 1.0f / dist)
                                         : lg_vec3(0.0f, 1.0f, 0.0f);
            float penetration = radius_sum - dist;

            /* Relative velocity of B with respect to A, along n.
             * Negative means closing. */
            lg_vec3_t rel = lg_vec3_sub(bb->velocity, ba->velocity);
            float vn = lg_vec3_dot(rel, n);

            if (record && vn < 0.0f) {
                ag_record_collision(self, s->entities[i], s->entities[j], -vn);
            }

            if (vn < 0.0f) {
                float e = fminf(s->materials[i].restitution, s->materials[j].restitution);
                float jmag = -(1.0f + e) * vn / inv_sum;
                lg_vec3_t impulse = lg_vec3_scale(n, jmag);

                /* A is driven away from B (-n); B is driven along +n. */
                ba->velocity = lg_vec3_sub(ba->velocity, lg_vec3_scale(impulse, inv_a));
                bb->velocity = lg_vec3_add(bb->velocity, lg_vec3_scale(impulse, inv_b));

                lg_body_wake(ba);
                lg_body_wake(bb);
            }

            /* Push the pair apart so they cannot settle inside one another. */
            float excess = penetration - AG_PENETRATION_SLOP;
            if (excess > 0.0f) {
                lg_vec3_t corr =
                    lg_vec3_scale(n, excess / inv_sum * AG_CORRECTION_PERCENT);
                s->transforms[i].position =
                    lg_vec3_sub(s->transforms[i].position, lg_vec3_scale(corr, inv_a));
                s->transforms[j].position =
                    lg_vec3_add(s->transforms[j].position, lg_vec3_scale(corr, inv_b));
            }
        }
    }
}

/*============================================================================
 * Lifecycle
 *===========================================================================*/

ag_world* ag_world_create(float time_step, size_t max_entities) {
    ag_world* self = (ag_world*)calloc(1, sizeof(ag_world));
    if (!self) return NULL;

    lg_world_config_t cfg = LG_WORLD_CONFIG_DEFAULT;

    /* Space: no ambient gravity. Any gravity in this game is an explicit force. */
    cfg.gravity[0] = 0.0f;
    cfg.gravity[1] = 0.0f;
    cfg.gravity[2] = 0.0f;

    cfg.time_step = time_step;
    cfg.max_entities = max_entities;
    cfg.max_bodies = max_entities;
    cfg.max_colliders = max_entities;

    /* Sleeping keeps an untouched asteroid field cheap. */
    cfg.enable_sleeping = true;
    cfg.sleep_threshold = 0.01f;

    cfg.enable_collision = true;
    cfg.collision_iterations = 4;

    self->w = lg_world_create(&cfg);
    if (!self->w) {
        free(self);
        return NULL;
    }

    /* lg_world_create seeds world->gravity from config, but be explicit: drifting is
     * the whole feel of the game and a stray -9.81 would be very confusing. */
    lg_world_set_gravity_v(self->w, lg_vec3_zero());
    self->w->use_gravity = false;

    /* No lagrange collision callback: its narrow phase is disabled (every collider is a
     * trigger) and contacts are recorded by ag_resolve_collisions instead. */

    return self;
}

void ag_world_free(ag_world* self) {
    if (!self) return;
    lg_world_destroy(self->w);
    free(self);
}

/*============================================================================
 * Entities
 *===========================================================================*/

static uint64_t ag_finish_spawn(ag_world* self, lg_entity_t e, float mass,
                                const lg_collider_t* col,
                                float px, float py, float pz) {
    if (e == LG_ENTITY_INVALID) return 0;

    lg_body_t body = lg_body(mass);

    /* Derive the inertia tensor from the actual shape. lg_body() leaves it as the unit
     * vector, which would make a 5-tonne asteroid spin like a pebble. */
    if (mass > 0.0f) {
        lg_body_set_inertia(&body, lg_collider_inertia(col, mass));
    }

    /* Mild damping so nothing drifts forever after a nudge. Space has none, but a game
     * where a bumped asteroid never stops is unplayable. Tunable per body later. */
    body.linear_damping = 0.02f;
    body.angular_damping = 0.05f;

    lg_set_body(self->w, e, &body);

    /* Flagged as a trigger so lagrange's own (sign-inverted) narrow phase skips it;
     * collisions are detected and resolved in ag_resolve_collisions instead. The shape
     * and radius are still what that resolver reads. */
    lg_collider_t trigger_col = *col;
    trigger_col.is_trigger = true;
    lg_set_collider(self->w, e, &trigger_col);
    lg_set_position(self->w, e, lg_vec3(px, py, pz));
    lg_set_rotation(self->w, e, lg_quat_identity());

    return (uint64_t)e;
}

uint64_t ag_spawn_sphere(ag_world* self, float mass, float radius,
                         float px, float py, float pz) {
    if (!self) return 0;
    lg_collider_t col = lg_collider_sphere(radius);
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, px, py, pz);
}

uint64_t ag_spawn_box(ag_world* self, float mass, float hx, float hy, float hz,
                      float px, float py, float pz) {
    if (!self) return 0;
    lg_collider_t col = lg_collider_box(hx, hy, hz);
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, px, py, pz);
}

void ag_despawn(ag_world* self, uint64_t entity) {
    if (!self) return;
    lg_entity_destroy(self->w, (lg_entity_t)entity);
}

size_t ag_body_count(const ag_world* self) {
    if (!self) return 0;
    return lg_world_entity_count(self->w);
}

/*============================================================================
 * Hot path
 *===========================================================================*/

/*
 * Apply forces in O(n + m) rather than O(m·n).
 *
 * The obvious implementation calls lg_apply_force per command, but each of those is a
 * linear scan of storage. Instead: build a transient open-addressed table over the
 * command list (m is small — one entry per ship plus one per held object), then walk
 * storage once. The table is rebuilt per call, so there is no cache to invalidate when
 * lagrange swap-removes an entity.
 *
 * Multiple commands for the same entity ACCUMULATE. This is not a detail: a ship
 * hauling an asteroid receives one command for engine thrust and a second for the grab
 * reaction, and an earlier version of this function let the second overwrite the first
 * — so grabbing anything silently cut the engines.
 */

static size_t ag_hash_slot(uint64_t entity, size_t mask) {
    /* Entity ids are small sequential integers; multiply by a 64-bit odd constant to
     * spread them across the table. */
    return (size_t)((entity * 0x9E3779B97F4A7C15ull) >> 32) & mask;
}

void ag_apply_forces(ag_world* self, const ag_force_cmd* cmds, size_t n) {
    if (!self || !cmds || n == 0) return;

    /* Power-of-two table, at least 2x the command count to keep probes short. */
    size_t cap = 16;
    while (cap < n * 2) cap <<= 1;

    ag_force_cmd* table = (ag_force_cmd*)calloc(cap, sizeof(ag_force_cmd));
    if (!table) return;

    const size_t mask = cap - 1;

    /* entity == 0 is LG_ENTITY_INVALID, so a zeroed slot reads as empty. */
    for (size_t i = 0; i < n; i++) {
        uint64_t e = cmds[i].entity;
        if (e == 0) continue;

        size_t h = ag_hash_slot(e, mask);
        while (table[h].entity != 0 && table[h].entity != e) {
            h = (h + 1) & mask;
        }

        if (table[h].entity == 0) {
            table[h] = cmds[i];
        } else {
            table[h].fx += cmds[i].fx;
            table[h].fy += cmds[i].fy;
            table[h].fz += cmds[i].fz;
            table[h].tx += cmds[i].tx;
            table[h].ty += cmds[i].ty;
            table[h].tz += cmds[i].tz;
        }
    }

    lg_storage_t* s = &self->w->storage;
    for (size_t idx = 0; idx < s->count; idx++) {
        uint64_t e = (uint64_t)s->entities[idx];
        if (e == 0) continue;

        size_t h = ag_hash_slot(e, mask);
        while (table[h].entity != 0) {
            if (table[h].entity == e) {
                lg_body_t* b = &s->bodies[idx];
                lg_body_apply_force(b, lg_vec3(table[h].fx, table[h].fy, table[h].fz));
                lg_body_apply_torque(b, lg_vec3(table[h].tx, table[h].ty, table[h].tz));
                break;
            }
            h = (h + 1) & mask;
        }
    }

    free(table);
}

void ag_step(ag_world* self) {
    if (!self) return;

    self->collision_count = 0;
    self->collisions_dropped = 0;

    double t0 = ag_now_ms();

    /* Integrates, applies queued forces, then clears them. Its own collision pass finds
     * nothing because every collider is flagged as a trigger -- see the note above
     * ag_resolve_collisions. */
    lg_world_step(self->w);

    /* Record impact speeds on the first pass only: later iterations see velocities the
     * earlier ones already corrected, so counting them again would report a stream of
     * ever-gentler phantom impacts for one collision. */
    for (int iter = 0; iter < AG_SOLVER_ITERATIONS; iter++) {
        ag_resolve_collisions(self, iter == 0);
    }

    self->last_step_ms = ag_now_ms() - t0;
}

size_t ag_snapshot(ag_world* self, ag_body_state* out, size_t max) {
    if (!self || !out) return 0;

    lg_storage_t* s = &self->w->storage;
    size_t n = s->count < max ? s->count : max;

    for (size_t i = 0; i < n; i++) {
        const lg_transform_t* t = &s->transforms[i];
        const lg_body_t* b = &s->bodies[i];
        ag_body_state* o = &out[i];

        o->entity = (uint64_t)s->entities[i];
        o->px = t->position.x;  o->py = t->position.y;  o->pz = t->position.z;
        o->qx = t->rotation.x;  o->qy = t->rotation.y;  o->qz = t->rotation.z;
        o->qw = t->rotation.w;
        o->vx = b->velocity.x;  o->vy = b->velocity.y;  o->vz = b->velocity.z;
        o->wx = b->angular_velocity.x;
        o->wy = b->angular_velocity.y;
        o->wz = b->angular_velocity.z;
        o->mass = b->mass;
        o->sleeping = b->is_sleeping ? 1u : 0u;
        o->_pad[0] = o->_pad[1] = o->_pad[2] = 0;
    }

    return n;
}

size_t ag_drain_collisions(ag_world* self, ag_collision* out, size_t max) {
    if (!self || !out) return 0;

    size_t n = self->collision_count < max ? self->collision_count : max;
    memcpy(out, self->collisions, n * sizeof(ag_collision));
    self->collision_count = 0;
    return n;
}

/*============================================================================
 * Cold path
 *===========================================================================*/

bool ag_get_body(ag_world* self, uint64_t entity, ag_body_state* out) {
    if (!self || !out) return false;

    lg_entity_t e = (lg_entity_t)entity;
    lg_transform_t* t = lg_get_transform(self->w, e);
    lg_body_t* b = lg_get_body(self->w, e);
    if (!t || !b) return false;

    out->entity = entity;
    out->px = t->position.x;  out->py = t->position.y;  out->pz = t->position.z;
    out->qx = t->rotation.x;  out->qy = t->rotation.y;  out->qz = t->rotation.z;
    out->qw = t->rotation.w;
    out->vx = b->velocity.x;  out->vy = b->velocity.y;  out->vz = b->velocity.z;
    out->wx = b->angular_velocity.x;
    out->wy = b->angular_velocity.y;
    out->wz = b->angular_velocity.z;
    out->mass = b->mass;
    out->sleeping = b->is_sleeping ? 1u : 0u;
    out->_pad[0] = out->_pad[1] = out->_pad[2] = 0;
    return true;
}

void ag_set_position(ag_world* self, uint64_t entity, float x, float y, float z) {
    if (!self) return;
    lg_set_position(self->w, (lg_entity_t)entity, lg_vec3(x, y, z));
}

void ag_set_velocity(ag_world* self, uint64_t entity, float x, float y, float z) {
    if (!self) return;
    lg_set_velocity(self->w, (lg_entity_t)entity, lg_vec3(x, y, z));
}

void ag_set_damping(ag_world* self, uint64_t entity, float linear, float angular) {
    if (!self) return;
    lg_body_t* b = lg_get_body(self->w, (lg_entity_t)entity);
    if (!b) return;
    b->linear_damping = linear;
    b->angular_damping = angular;
}

void ag_set_sleep_allowed(ag_world* self, uint64_t entity, bool allowed) {
    if (!self) return;
    lg_body_t* b = lg_get_body(self->w, (lg_entity_t)entity);
    if (!b) return;
    b->allow_sleep = allowed;
    if (!allowed) lg_body_wake(b);
}

void ag_set_material(ag_world* self, uint64_t entity, float restitution, float friction) {
    if (!self) return;
    lg_material_t* m = lg_get_material(self->w, (lg_entity_t)entity);
    if (!m) return;
    m->restitution = restitution;
    m->friction = friction;
}

double ag_last_step_ms(const ag_world* self) {
    return self ? self->last_step_ms : 0.0;
}
