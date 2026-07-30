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

    /* Which local axis each capsule/cylinder runs along, as an ag_axis, indexed directly by
     * entity id.
     *
     * A flat array is safe here because lagrange keeps ids small and bounded: they come
     * from next_id++ or a free list with no version bits (world.h:199), and a fresh high id
     * is only minted when the free list is empty — which means every id below it is live,
     * and storage is capped at max_entities. So no id can exceed max_entities. Indexing is
     * bounds-checked anyway, because relying on that reasoning without a guard is how it
     * stops being true.
     *
     * Meaningless for other shapes, and left as AG_AXIS_Y for them. */
    uint8_t* shape_axis;
    size_t shape_axis_cap;

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
 * Narrow phase
 *
 * WHY THIS IS HERE INSTEAD OF lg_narrow_phase
 * -------------------------------------------
 * lagrange has the geometry for every pair this game could want — sphere, box, capsule,
 * cylinder, plane — and that is the bulk of the work. What it does not have is a
 * consistent answer to "which way does the contact normal point".
 *
 *   lg_collide_spheres        (sim.h:37)   delta = pos_b - pos_a          ->  A to B
 *   lg_collide_sphere_box     (sim.h:131)  delta = sphere - closest_on_box ->  B to A
 *   lg_collide_box_box        (sim.h:167)  sign from pos_a > pos_b         ->  B to A
 *   lg_collide_sphere_capsule (sim.h:215)  delta = sphere - closest        ->  B to A
 *   lg_collide_capsule_capsule(sim.h:306)  delta = c1_on_a - c2_on_b       ->  B to A
 *   lg_collide_sphere_cylinder(sim.h:379)  delta = sphere - edge           ->  B to A
 *   lg_collide_sphere_plane   (sim.h:76)   the plane's outward normal      ->  B to A
 *   lg_collide_box_plane      (sim.h:113)  the plane's outward normal      ->  B to A
 *
 * So lg_contact_t's "Normal pointing from A to B" (sim.h:25) describes exactly one of the
 * eight, and lg_resolve_contact — which assumes B to A (sim.h:515, 544) — is right about
 * the other seven. Sphere-sphere is the lone exception, and sphere-sphere was every body
 * in this game, which is why a ship flew through the middle of the mothership while
 * exchanging just enough momentum to look like collisions worked.
 *
 * This wrapper calls lagrange's geometry and flips the odd ones out, so everything below
 * it can rely on one rule: **the normal points from i toward j.**
 *
 * ROTATION. lagrange's box routines are AABB-only — they clamp against position ±
 * half_extents in world axes and never read the rotation. Rather than reimplement the
 * clamp, ag_sphere_vs_box rotates the sphere into the box's own frame, where the box *is*
 * axis-aligned, and rotates the resulting normal back out. Capsule and cylinder need no
 * such help: lagrange already takes their axis as a rotated vector.
 *===========================================================================*/

typedef struct {
    lg_vec3_t normal;       /* unit, points from i toward j */
    float penetration;      /* > 0 where they overlap */
} ag_contact;

/* The long axis of a capsule or cylinder, in world space.
 *
 * Defaults to the body's local +Y, which is lagrange's convention — lg_collider_inertia
 * builds its tensor around Y as well (collider.h:159, 167), so shape and inertia agree for
 * free. AG_AXIS_Z bodies use local +Z instead and have their inertia swizzled at spawn to
 * match; see ag_finish_spawn and the note on ag_axis in bridge.h. */
static lg_vec3_t ag_shape_axis(const ag_world* self, lg_entity_t e,
                               const lg_transform_t* t) {
    lg_vec3_t local = lg_vec3(0.0f, 1.0f, 0.0f);
    if (self->shape_axis && (size_t)e < self->shape_axis_cap &&
        self->shape_axis[e] == AG_AXIS_Z) {
        local = lg_vec3(0.0f, 0.0f, 1.0f);
    }
    return lg_quat_rotate(t->rotation, local);
}

/* Sphere vs box, honouring the box's rotation. Normal points sphere -> box. */
static bool ag_sphere_vs_box(lg_vec3_t sp, float sr,
                             const lg_transform_t* bt, lg_vec3_t bhalf,
                             ag_contact* out) {
    lg_vec3_t local = lg_quat_rotate(lg_quat_conj(bt->rotation),
                                     lg_vec3_sub(sp, bt->position));

    lg_contact_t c;
    if (!lg_collide_sphere_box(local, sr, lg_vec3_zero(), bhalf, &c)) return false;

    /* lagrange gives box -> sphere in box space; negate for sphere -> box, then back to
     * world. */
    out->normal = lg_quat_rotate(bt->rotation, lg_vec3_neg(c.normal));
    out->penetration = c.penetration;
    return true;
}

/* The plane's outward normal in world space. lg_narrow_phase is inconsistent here too —
 * it rotates for sphere-plane (sim.h:592) but uses the unrotated field for box-plane
 * (sim.h:613). Rotating always is the defensible one. */
static lg_vec3_t ag_plane_normal(const lg_transform_t* t, const lg_collider_t* c) {
    lg_vec3_t n = lg_quat_rotate(t->rotation, c->plane.normal);
    return (lg_vec3_len_sq(n) > 1e-12f) ? lg_vec3_norm(n) : lg_vec3_up();
}

/* Contact between storage slots i and j with the normal pointing i -> j.
 *
 * Ordered pairs are written once and the reversed order reuses them with the normal
 * negated, so there is one implementation per shape pair rather than two. */
static bool ag_narrow_phase(ag_world* self, size_t i, size_t j, ag_contact* out) {
    lg_storage_t* s = &self->w->storage;
    const lg_collider_t* ci = &s->colliders[i];
    const lg_collider_t* cj = &s->colliders[j];
    const lg_transform_t* ti = &s->transforms[i];
    const lg_transform_t* tj = &s->transforms[j];

    const int a = (int)ci->type, b = (int)cj->type;
    lg_contact_t c;

    /* --- sphere vs sphere: the one pair lagrange already points i -> j --- */
    if (a == LG_SHAPE_SPHERE && b == LG_SHAPE_SPHERE) {
        if (!lg_collide_spheres(ti->position, ci->sphere.radius,
                                tj->position, cj->sphere.radius, &c)) return false;
        out->normal = c.normal;
        out->penetration = c.penetration;
        return true;
    }

    /* --- sphere vs box --- */
    if (a == LG_SHAPE_SPHERE && b == LG_SHAPE_BOX)
        return ag_sphere_vs_box(ti->position, ci->sphere.radius, tj, cj->box.half_extents, out);
    if (a == LG_SHAPE_BOX && b == LG_SHAPE_SPHERE) {
        if (!ag_sphere_vs_box(tj->position, cj->sphere.radius, ti, ci->box.half_extents, out))
            return false;
        out->normal = lg_vec3_neg(out->normal);   /* was j -> i */
        return true;
    }

    /* --- sphere vs capsule --- */
    if (a == LG_SHAPE_SPHERE && b == LG_SHAPE_CAPSULE) {
        if (!lg_collide_sphere_capsule(ti->position, ci->sphere.radius,
                                       tj->position, ag_shape_axis(self, s->entities[j], tj),
                                       cj->capsule.half_height, cj->capsule.radius, &c))
            return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }
    if (a == LG_SHAPE_CAPSULE && b == LG_SHAPE_SPHERE) {
        if (!lg_collide_sphere_capsule(tj->position, cj->sphere.radius,
                                       ti->position, ag_shape_axis(self, s->entities[i], ti),
                                       ci->capsule.half_height, ci->capsule.radius, &c))
            return false;
        out->normal = c.normal;   /* capsule -> sphere is already i -> j */
        out->penetration = c.penetration;
        return true;
    }

    /* --- sphere vs cylinder --- */
    if (a == LG_SHAPE_SPHERE && b == LG_SHAPE_CYLINDER) {
        if (!lg_collide_sphere_cylinder(ti->position, ci->sphere.radius,
                                        tj->position, ag_shape_axis(self, s->entities[j], tj),
                                        cj->cylinder.half_height, cj->cylinder.radius, &c))
            return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }
    if (a == LG_SHAPE_CYLINDER && b == LG_SHAPE_SPHERE) {
        if (!lg_collide_sphere_cylinder(tj->position, cj->sphere.radius,
                                        ti->position, ag_shape_axis(self, s->entities[i], ti),
                                        ci->cylinder.half_height, ci->cylinder.radius, &c))
            return false;
        out->normal = c.normal;
        out->penetration = c.penetration;
        return true;
    }

    /* --- sphere/box vs plane --- */
    if (a == LG_SHAPE_SPHERE && b == LG_SHAPE_PLANE) {
        if (!lg_collide_sphere_plane(ti->position, ci->sphere.radius,
                                     ag_plane_normal(tj, cj), cj->plane.distance, &c))
            return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }
    if (a == LG_SHAPE_PLANE && b == LG_SHAPE_SPHERE) {
        if (!lg_collide_sphere_plane(tj->position, cj->sphere.radius,
                                     ag_plane_normal(ti, ci), ci->plane.distance, &c))
            return false;
        out->normal = c.normal;
        out->penetration = c.penetration;
        return true;
    }
    if (a == LG_SHAPE_BOX && b == LG_SHAPE_PLANE) {
        if (!lg_collide_box_plane(ti->position, ci->box.half_extents,
                                  ag_plane_normal(tj, cj), cj->plane.distance, &c))
            return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }
    if (a == LG_SHAPE_PLANE && b == LG_SHAPE_BOX) {
        if (!lg_collide_box_plane(tj->position, cj->box.half_extents,
                                  ag_plane_normal(ti, ci), ci->plane.distance, &c))
            return false;
        out->normal = c.normal;
        out->penetration = c.penetration;
        return true;
    }

    /* --- capsule vs capsule --- */
    if (a == LG_SHAPE_CAPSULE && b == LG_SHAPE_CAPSULE) {
        if (!lg_collide_capsule_capsule(ti->position, ag_shape_axis(self, s->entities[i], ti),
                                        ci->capsule.half_height, ci->capsule.radius,
                                        tj->position, ag_shape_axis(self, s->entities[j], tj),
                                        cj->capsule.half_height, cj->capsule.radius, &c))
            return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }

    /* --- box vs box ---
     *
     * lagrange's is an AABB overlap test: it ignores both rotations. That is a real
     * limitation and it is left in place rather than papered over, because in this game
     * the pair cannot arise — every dynamic body is a sphere, so two boxes are always two
     * pieces of static scenery, and the resolver skips static/static before it ever gets
     * here. If a dynamic box is ever added, this needs SAT over the 15 separating axes and
     * the note on ag_spawn_box has to change with it. */
    if (a == LG_SHAPE_BOX && b == LG_SHAPE_BOX) {
        if (!lg_collide_box_box(ti->position, ci->box.half_extents,
                                tj->position, cj->box.half_extents, &c)) return false;
        out->normal = lg_vec3_neg(c.normal);
        out->penetration = c.penetration;
        return true;
    }

    /* capsule/box, capsule/cylinder, cylinder/cylinder, cylinder/box, plane/plane and
     * plane/capsule: lagrange has no routine for these either. Reporting no contact is the
     * honest answer, and ag_collider_pair_supported lets Go refuse the spawn instead of
     * letting a body silently fall through the world. */
    return false;
}

/*============================================================================
 * Collision resolution
 *
 * Runs on the contacts above rather than lagrange's lg_resolve_contact, which reads the
 * normal in the opposite direction to the one sphere-sphere produces (see the table).
 * Every collider is still flagged as a trigger so lagrange's own pass finds nothing
 * (sim.h:566) and cannot resolve the same contact twice with the other sign.
 *
 * Not carried over: angular impulse from contacts (rocks do not gain spin from being
 * hit) and Coulomb friction. Both are fidelity, not correctness, and neither is missed
 * in a frictionless vacuum. Note that angular impulse matters more for the non-sphere
 * shapes, whose contact points are genuinely off-centre — a box clipped on one corner
 * ought to tumble and will not.
 *===========================================================================*/

#define AG_SOLVER_ITERATIONS 4
#define AG_PENETRATION_SLOP 0.02f
#define AG_CORRECTION_PERCENT 0.6f

static void ag_resolve_collisions(ag_world* self, bool record) {
    lg_storage_t* s = &self->w->storage;

    for (size_t i = 0; i < s->count; i++) {
        for (size_t j = i + 1; j < s->count; j++) {
            lg_body_t* ba = &s->bodies[i];
            lg_body_t* bb = &s->bodies[j];

            float inv_a = ba->inv_mass;
            float inv_b = bb->inv_mass;
            float inv_sum = inv_a + inv_b;
            if (inv_sum <= 0.0f) continue; /* two static bodies */

            ag_contact contact;
            if (!ag_narrow_phase(self, i, j, &contact)) continue;

            /* n points from A toward B, and everything below is consistent with that. */
            lg_vec3_t n = contact.normal;
            float penetration = contact.penetration;

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

    /* +1 because ids are 1-based and bounded by max_entities, so max_entities itself is a
     * valid index. calloc leaves every entry AG_AXIS_Y, which is the right default. */
    self->shape_axis_cap = max_entities + 1;
    self->shape_axis = (uint8_t*)calloc(self->shape_axis_cap, sizeof(uint8_t));
    if (!self->shape_axis) {
        lg_world_destroy(self->w);
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
    free(self->shape_axis);
    free(self);
}

/*============================================================================
 * Entities
 *===========================================================================*/

static uint64_t ag_finish_spawn(ag_world* self, lg_entity_t e, float mass,
                                const lg_collider_t* col, int axis,
                                float px, float py, float pz) {
    if (e == LG_ENTITY_INVALID) return 0;

    /* Record the axis before anything can early-return, so a recycled id never inherits the
     * previous occupant's choice. */
    if (self->shape_axis && (size_t)e < self->shape_axis_cap) {
        self->shape_axis[e] = (axis == AG_AXIS_Z) ? AG_AXIS_Z : AG_AXIS_Y;
    }

    lg_body_t body = lg_body(mass);

    /* Derive the inertia tensor from the actual shape. lg_body() leaves it as the unit
     * vector, which would make a 5-tonne asteroid spin like a pebble. */
    if (mass > 0.0f) {
        lg_vec3_t inertia = lg_collider_inertia(col, mass);

        /* lg_collider_inertia assumes the long axis is Y, returning (ixz, iy, ixz) for
         * capsules and cylinders (collider.h:159, 167). A Z-axis body is long along Z
         * instead, so the cheap term belongs on Z: swap Y and Z. Without this a Z-axis
         * capsule would collide along one axis and resist spin about another — the exact
         * inconsistency the normal table above documents. */
        if (axis == AG_AXIS_Z &&
            (col->type == LG_SHAPE_CAPSULE || col->type == LG_SHAPE_CYLINDER)) {
            float tmp = inertia.y;
            inertia.y = inertia.z;
            inertia.z = tmp;
        }

        lg_body_set_inertia(&body, inertia);
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
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, AG_AXIS_Y,
                           px, py, pz);
}

uint64_t ag_spawn_box(ag_world* self, float mass, float hx, float hy, float hz,
                      float px, float py, float pz) {
    if (!self) return 0;
    lg_collider_t col = lg_collider_box(hx, hy, hz);
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, AG_AXIS_Y,
                           px, py, pz);
}

uint64_t ag_spawn_capsule(ag_world* self, float mass, float radius, float half_height,
                          int axis, float px, float py, float pz) {
    if (!self) return 0;
    /* lg_collider_capsule takes the *full* height and halves it internally
     * (collider.h:83), so undo that here — every other shape in this API is expressed in
     * half-extents and a capsule that came out twice as long as asked for would be a
     * miserable thing to debug. */
    lg_collider_t col = lg_collider_capsule(radius, half_height * 2.0f);
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, axis,
                           px, py, pz);
}

uint64_t ag_spawn_cylinder(ag_world* self, float mass, float radius, float half_height,
                           int axis, float px, float py, float pz) {
    if (!self) return 0;
    lg_collider_t col = lg_collider_cylinder(radius, half_height * 2.0f);
    return ag_finish_spawn(self, lg_entity_create(self->w), mass, &col, axis,
                           px, py, pz);
}

uint64_t ag_spawn_plane(ag_world* self, float nx, float ny, float nz, float distance) {
    if (!self) return 0;
    /* Always static: an infinite half-space with a mass is not a thing. */
    lg_collider_t col = lg_collider_plane(lg_vec3(nx, ny, nz), distance);
    return ag_finish_spawn(self, lg_entity_create(self->w), 0.0f, &col, AG_AXIS_Y,
                           0.0f, 0.0f, 0.0f);
}

void ag_set_rotation(ag_world* self, uint64_t entity, float x, float y, float z, float w) {
    if (!self) return;
    lg_quat_t q = {x, y, z, w};
    lg_set_rotation(self->w, (lg_entity_t)entity, lg_quat_norm(q));
}

bool ag_collider_pair_supported(int shape_a, int shape_b) {
    /* Mirrors the dispatch in ag_narrow_phase. Kept as a query so Go can reject a spawn
     * whose shape cannot collide with anything it will meet, rather than letting the body
     * fall through the world silently — which is how the old box collider behaved and is a
     * far worse failure than an error at spawn time. */
    const int SP = LG_SHAPE_SPHERE, BX = LG_SHAPE_BOX, CA = LG_SHAPE_CAPSULE,
              CY = LG_SHAPE_CYLINDER, PL = LG_SHAPE_PLANE;

    int lo = shape_a < shape_b ? shape_a : shape_b;
    int hi = shape_a < shape_b ? shape_b : shape_a;

    if (lo == SP && (hi == SP || hi == BX || hi == CA || hi == CY || hi == PL)) return true;
    if (lo == BX && (hi == BX || hi == PL)) return true;
    if (lo == CA && hi == CA) return true;
    return false;
}

void ag_despawn(ag_world* self, uint64_t entity) {
    if (!self) return;
    /* Reset the axis so a recycled id starts from the default rather than inheriting it.
     * ag_finish_spawn also writes it unconditionally, so this is belt and braces — but the
     * failure it guards against is a capsule that silently collides about the wrong axis,
     * which is not the sort of thing that announces itself. */
    if (self->shape_axis && entity < self->shape_axis_cap) {
        self->shape_axis[entity] = AG_AXIS_Y;
    }
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
