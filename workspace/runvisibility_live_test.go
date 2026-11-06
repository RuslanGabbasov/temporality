package workspace

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// RunVisibilityActors (docs/activity-scoping.md, the operator model): the
// allowed initiator list is the user, their triggers' ids, and the members of
// their granted units' subtrees. Needs the real migrated schema — the unit
// tree and membership resolve through SQL.
func TestRunVisibilityActorsLive(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	suffix := time.Now().UTC().Format("150405.000000000")
	unitA := "unit-a-" + suffix
	unitB := "unit-b-" + suffix // child of A
	unitC := "unit-c-" + suffix // outside the granted subtree
	require.NoError(t, store.CreateOrgUnit(ctx, &OrgUnit{ID: unitA, Kind: "department", Name: "A"}))
	require.NoError(t, store.CreateOrgUnit(ctx, &OrgUnit{ID: unitB, Kind: "team", Name: "B", ParentID: unitA}))
	require.NoError(t, store.CreateOrgUnit(ctx, &OrgUnit{ID: unitC, Kind: "team", Name: "C"}))
	defer func() {
		_ = store.DeleteOrgUnit(ctx, unitC)
		_ = store.DeleteOrgUnit(ctx, unitB)
		_ = store.DeleteOrgUnit(ctx, unitA)
	}()

	mkUser := func(id, unit string) {
		require.NoError(t, store.CreateUser(ctx, &User{ID: id, Name: id, Role: "operator", Active: true, OrgUnitID: unit}))
	}
	grantee := "vis-grantee-" + suffix
	memberB := "vis-member-b-" + suffix
	memberC := "vis-member-c-" + suffix
	mkUser(grantee, unitC) // the grantee sits outside the granted tree — grants still apply
	mkUser(memberB, unitB) // inside the granted subtree
	mkUser(memberC, unitC) // outside
	defer func() {
		_ = store.DeleteUser(ctx, grantee)
		_ = store.DeleteUser(ctx, memberB)
		_ = store.DeleteUser(ctx, memberC)
	}()

	trigger := "vis-trigger-" + suffix
	require.NoError(t, store.CreateProject(ctx, &Project{ID: "vis-p-" + suffix, Name: "P"}))
	defer func() { _ = store.DeleteProject(ctx, "vis-p-"+suffix) }()
	require.NoError(t, store.CreateTrigger(ctx, &Trigger{ID: trigger, ProjectID: "vis-p-" + suffix, Name: "T", Type: "webhook", Enabled: true, CreatedBy: grantee}))
	defer func() { _ = store.DeleteTrigger(ctx, trigger) }()
	require.NoError(t, store.SetOrgUnitRole(ctx, unitA, grantee, "writer", "admin"))
	defer func() { _ = store.RemoveOrgUnitRole(ctx, unitA, grantee) }()

	actors, err := store.RunVisibilityActors(ctx, PrincipalSeed{UserID: grantee})
	require.NoError(t, err)
	require.Contains(t, actors, grantee, "own runs")
	require.Contains(t, actors, trigger, "owned trigger runs")
	require.Contains(t, actors, memberB, "granted subtree member")
	require.NotContains(t, actors, memberC, "outside the granted subtree")
	require.NotContains(t, actors, "", "no empty actor entries")

	// Admin seed: unrestricted.
	actors, err = store.RunVisibilityActors(ctx, PrincipalSeed{UserID: grantee, Admin: true})
	require.NoError(t, err)
	require.Nil(t, actors, "admins see everything")

	// No user (service principal): unrestricted.
	actors, err = store.RunVisibilityActors(ctx, PrincipalSeed{})
	require.NoError(t, err)
	require.Nil(t, actors)
}
