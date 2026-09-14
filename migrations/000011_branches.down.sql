DROP TRIGGER IF EXISTS branch_comparisons_immutable ON branch_comparisons;
DROP TRIGGER IF EXISTS fork_groups_immutable ON fork_groups;
DROP TABLE IF EXISTS branch_comparisons;
DROP TABLE IF EXISTS branches;
DROP TABLE IF EXISTS fork_groups;
DROP FUNCTION IF EXISTS reject_branch_durable_mutation();
