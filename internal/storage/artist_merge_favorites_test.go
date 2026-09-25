package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestArtistFavoriteThreeSourceMergeRollback(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		order     []int
		remaining []int
	}{
		{"FIFO", []int{0, 1, 2}, []int{1, 1, 0}},
		{"LIFO", []int{2, 1, 0}, []int{1, 1, 0}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "three.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			target := insertRoleTestArtist(t, ctx, store, "Target", "target")
			sources := []int64{
				insertRoleTestArtist(t, ctx, store, "A", "a"),
				insertRoleTestArtist(t, ctx, store, "B", "b"),
				insertRoleTestArtist(t, ctx, store, "C", "c"),
			}
			for _, i := range []int{0, 2} {
				if err = store.SetArtistFavorite(ctx, sources[i], true); err != nil {
					t.Fatal(err)
				}
			}
			ops := make([]int64, 3)
			for i, source := range sources {
				ops[i], err = store.MergeArtists(ctx, source, target)
				if err != nil {
					t.Fatal(err)
				}
			}
			for i, want := range []int{1, 0, 0} {
				assertArtistMergeFavoriteFlag(t, ctx, store, ops[i], want)
			}
			assertArtistFavoriteState(t, ctx, store, target, 1)
			for step, i := range scenario.order {
				if err = store.RollbackArtistMerge(ctx, ops[i]); err != nil {
					t.Fatal(err)
				}
				assertArtistFavoriteState(t, ctx, store, target, scenario.remaining[step])
				assertArtistFavoriteState(t, ctx, store, sources[i], map[bool]int{true: 1, false: 0}[i != 1])
				if step == 0 && i == 0 {
					assertArtistMergeFavoriteFlag(t, ctx, store, ops[2], 1)
				}
			}
		})
	}
}

func TestArtistFavoriteNestedMergeRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "nested.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	x := insertRoleTestArtist(t, ctx, store, "X", "x")
	b := insertRoleTestArtist(t, ctx, store, "B", "b")
	a := insertRoleTestArtist(t, ctx, store, "A", "a")
	target := insertRoleTestArtist(t, ctx, store, "T", "t")
	for _, id := range []int64{x, a} {
		if err = store.SetArtistFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	xToB, err := store.MergeArtists(ctx, x, b)
	if err != nil {
		t.Fatal(err)
	}
	assertArtistFavoriteState(t, ctx, store, b, 1)
	aToT, err := store.MergeArtists(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	bToT, err := store.MergeArtists(ctx, b, target)
	if err != nil {
		t.Fatal(err)
	}
	assertArtistMergeFavoriteFlag(t, ctx, store, aToT, 1)
	assertArtistMergeFavoriteFlag(t, ctx, store, bToT, 0)
	// B may be a merged source while X still points at B; rolling back X->B
	// is allowed by the merge-state check (which validates X's direct link).
	if err = store.RollbackArtistMerge(ctx, xToB); err != nil {
		t.Fatal(err)
	}
	assertArtistFavoriteState(t, ctx, store, b, 0)
	assertArtistFavoriteState(t, ctx, store, target, 1)
	if err = store.RollbackArtistMerge(ctx, aToT); err != nil {
		t.Fatal(err)
	}
	assertArtistMergeFavoriteFlag(t, ctx, store, bToT, 0) // B lost its favorite, so no transfer.
	assertArtistFavoriteState(t, ctx, store, target, 0)
	if err = store.RollbackArtistMerge(ctx, bToT); err != nil {
		t.Fatal(err)
	}
	assertArtistFavoriteState(t, ctx, store, target, 0)
	assertArtistFavoriteState(t, ctx, store, b, 0)
	assertArtistFavoriteState(t, ctx, store, x, 1)
	assertArtistFavoriteState(t, ctx, store, a, 1)
}
