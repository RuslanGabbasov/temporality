-- ============================================================================
-- Access model divergence report: old JSONB lists vs new org model
-- (docs/org-structure.md §48 stage 4; docs/plan-org-structure.md, wave G)
--
-- Old model (dropped by migration 000043):
--   workspace_user.projects         JSONB array of project ids;
--                                   null / [] / ["*"] (or admin) = all projects.
--   workspace_project.allowed_users JSONB; "*" / ["*"] = all users,
--                                   [] / null = admins only, otherwise user ids.
--   Installation admins bypassed both lists.
--
-- New model (docs/org-structure.md §14-16):
--   workspace_user.org_unit_id        primary unit; NULL = sees everything
--                                     (legacy super-visibility, transition).
--   org_unit.path                     materialized path; a viewer sees a unit's
--                                     subtree => viewer chain = ancestors + self.
--   resource.org_unit_id              NULL = global (visible to everyone).
--   workspace_project_org_unit        project <-> units many-to-many;
--                                     no rows = org-neutral = visible to all.
--   workspace_project_member          explicit membership (access outside org
--                                     areas); installation admins bypass.
--
-- Archived projects are tombstones hidden from everyone in both models and
-- are excluded. Audiences consider active users only (inactive users cannot
-- authenticate).
--
-- READ-ONLY: creates temp views in the session's pg_temp schema only.
-- Run via scripts/access_divergence.sh (or: psql -f scripts/access-divergence.sql)
-- ============================================================================

-- ---------------------------------------------------------------------------
-- Shared building blocks (temp views, dropped automatically at session end)
-- ---------------------------------------------------------------------------

-- Viewer chain per user: ancestor unit ids from the materialized path + self.
-- Unassigned users get a NULL member that never matches (handled by the
-- org_unit_id IS NULL branch everywhere below).
CREATE TEMP VIEW user_chain AS
SELECT u.id AS user_id,
       array_append(string_to_array(COALESCE(ou.path, ''), '.'), ou.id) AS chain
FROM workspace_user u
LEFT JOIN org_unit ou ON ou.id = u.org_unit_id;

-- Org units bound to each project (missing row set = org-neutral project).
CREATE TEMP VIEW project_units AS
SELECT pu.project_id, array_agg(pu.org_unit_id) AS units
FROM workspace_project_org_unit pu
GROUP BY pu.project_id;

-- Old model, user side: did the legacy list grant everything, and what ids
-- did it name explicitly?
CREATE TEMP VIEW old_user_scope AS
SELECT u.id AS user_id,
       (u.role = 'admin'
        OR jsonb_typeof(u.projects) IS DISTINCT FROM 'array'
        OR jsonb_array_length(u.projects) = 0
        OR u.projects @> '["*"]'::jsonb) AS sees_all,
       CASE WHEN jsonb_typeof(u.projects) = 'array'
            THEN ARRAY(SELECT jsonb_array_elements_text(u.projects))
       END AS listed
FROM workspace_user u;

-- New model, user side: (user, project) pairs visible via the org model.
CREATE TEMP VIEW new_user_visibility AS
SELECT u.id AS user_id, p.id AS project_id
FROM workspace_user u
CROSS JOIN workspace_project p
LEFT JOIN user_chain uc ON uc.user_id = u.id
LEFT JOIN project_units pu ON pu.project_id = p.id
WHERE NOT p.archived
  AND (u.role = 'admin'          -- installation admins bypass
    OR u.org_unit_id IS NULL     -- unassigned: legacy super-visibility
    OR pu.units IS NULL          -- org-neutral project: visible everywhere
    OR EXISTS (SELECT 1 FROM workspace_project_member m
               WHERE m.project_id = p.id AND m.user_id = u.id)
    OR EXISTS (SELECT 1 FROM unnest(uc.chain) AS c(unit_id)
               WHERE c.unit_id = ANY (pu.units)));

-- Old model, project side: (project, user) pairs allowed by allowed_users.
CREATE TEMP VIEW old_project_audience AS
SELECT p.id AS project_id, u.id AS user_id
FROM workspace_project p
CROSS JOIN workspace_user u
WHERE NOT p.archived AND u.active
  AND (u.role = 'admin'                              -- admin bypass
    OR p.allowed_users = '"*"'::jsonb                -- scalar "*"
    OR p.allowed_users @> '["*"]'::jsonb             -- array ["*"]
    OR p.allowed_users ? u.id);                      -- explicit list

-- New model, project side: (project, user) pairs that can see the project.
CREATE TEMP VIEW new_project_audience AS
SELECT v.project_id, v.user_id
FROM new_user_visibility v
JOIN workspace_user u ON u.id = v.user_id
WHERE u.active;

-- Per-user divergence: old visible set vs new effective set.
CREATE TEMP VIEW user_divergence AS
WITH old_set AS (
  SELECT s.user_id,
         CASE WHEN s.sees_all
              THEN ARRAY(SELECT p.id FROM workspace_project p WHERE NOT p.archived ORDER BY p.id)
              ELSE COALESCE(s.listed, '{}')
         END AS projects
  FROM old_user_scope s
),
new_set AS (
  SELECT v.user_id, ARRAY(SELECT v2.project_id FROM new_user_visibility v2
                          WHERE v2.user_id = v.user_id ORDER BY v2.project_id) AS projects
  FROM new_user_visibility v GROUP BY v.user_id
)
SELECT u.id AS user_id, u.name, u.role, u.active,
       COALESCE(u.org_unit_id, '(unassigned)') AS org_unit_id,
       COALESCE(o.projects, '{}') AS old_projects,
       COALESCE(n.projects, '{}') AS new_projects,
       ARRAY(SELECT unnest(COALESCE(o.projects, '{}'))
             EXCEPT SELECT unnest(COALESCE(n.projects, '{}'))) AS only_in_old,
       ARRAY(SELECT unnest(COALESCE(n.projects, '{}'))
             EXCEPT SELECT unnest(COALESCE(o.projects, '{}'))) AS only_in_new
FROM workspace_user u
LEFT JOIN old_set o ON o.user_id = u.id
LEFT JOIN new_set n ON n.user_id = u.id;

-- Per-project divergence: old audience vs new audience (active users).
CREATE TEMP VIEW project_divergence AS
WITH old_set AS (
  SELECT a.project_id, ARRAY(SELECT a2.user_id FROM old_project_audience a2
                             WHERE a2.project_id = a.project_id ORDER BY a2.user_id) AS users
  FROM old_project_audience a GROUP BY a.project_id
),
new_set AS (
  SELECT a.project_id, ARRAY(SELECT a2.user_id FROM new_project_audience a2
                             WHERE a2.project_id = a.project_id ORDER BY a2.user_id) AS users
  FROM new_project_audience a GROUP BY a.project_id
)
SELECT p.id AS project_id, p.name,
       COALESCE(pu.units, '{}') AS org_units,
       COALESCE(o.users, '{}') AS old_users,
       COALESCE(n.users, '{}') AS new_users,
       ARRAY(SELECT unnest(COALESCE(o.users, '{}'))
             EXCEPT SELECT unnest(COALESCE(n.users, '{}'))) AS only_in_old,
       ARRAY(SELECT unnest(COALESCE(n.users, '{}'))
             EXCEPT SELECT unnest(COALESCE(o.users, '{}'))) AS only_in_new
FROM workspace_project p
LEFT JOIN project_units pu ON pu.project_id = p.id
LEFT JOIN old_set o ON o.project_id = p.id
LEFT JOIN new_set n ON n.project_id = p.id
WHERE NOT p.archived;

-- ---------------------------------------------------------------------------
-- A. Users whose legacy projects list differs from the new effective visibility
-- ---------------------------------------------------------------------------
\echo '=== A. Users: legacy projects list vs new effective visibility (divergent only) ==='
SELECT user_id, name, role, active, org_unit_id,
       old_projects, new_projects, only_in_old, only_in_new
FROM user_divergence
WHERE cardinality(only_in_old) > 0 OR cardinality(only_in_new) > 0
ORDER BY user_id;

-- ---------------------------------------------------------------------------
-- B. Projects whose legacy allowed_users differs from the new effective audience
-- ---------------------------------------------------------------------------
\echo '=== B. Projects: legacy allowed_users vs new effective audience (divergent only) ==='
SELECT project_id, name, org_units,
       old_users, new_users, only_in_old, only_in_new
FROM project_divergence
WHERE cardinality(only_in_old) > 0 OR cardinality(only_in_new) > 0
ORDER BY project_id;

-- ---------------------------------------------------------------------------
-- C. Counts summary
-- ---------------------------------------------------------------------------
\echo '=== C. Summary ==='
SELECT
  (SELECT count(*) FROM user_divergence)                          AS users_checked,
  (SELECT count(*) FROM user_divergence
     WHERE cardinality(only_in_old) > 0 OR cardinality(only_in_new) > 0) AS users_divergent,
  (SELECT count(*) FROM old_user_scope WHERE sees_all)            AS users_legacy_see_all,
  (SELECT count(*) FROM workspace_user WHERE org_unit_id IS NULL) AS users_unassigned,
  (SELECT count(*) FROM project_divergence)                       AS projects_checked,
  (SELECT count(*) FROM project_divergence
     WHERE cardinality(only_in_old) > 0 OR cardinality(only_in_new) > 0) AS projects_divergent,
  (SELECT count(*) FROM org_unit)                                 AS org_units,
  (SELECT count(*) FROM workspace_project_org_unit)               AS project_unit_links,
  (SELECT count(*) FROM workspace_project_member)                 AS project_members;
