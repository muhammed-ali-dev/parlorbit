package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoresCommittedWALAndAccess(t *testing.T) {
	ctx := context.Background()
	original := testStore(t)
	ss, token, err := original.createSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	house, invite, err := original.createHouse(ctx, ss.ID, "Backup House", "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if err = original.setGame(ctx, house.ID, house.Rooms[0].ID, ss.ID, "https://codenames.game/r/backup-night", 0); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "snapshot.db")
	// Original stays open: committed records can still live in its WAL.
	if err = BackupDatabase(ctx, originalPath(t, original), target); err != nil {
		t.Fatal(err)
	}
	if err = BackupDatabase(ctx, originalPath(t, original), target); err == nil {
		t.Fatal("overwrote an existing backup")
	}
	restored, err := OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered, err := restored.sessionByToken(ctx, token)
	if err != nil || recovered.ID != ss.ID {
		t.Fatalf("guest access lost: %v", err)
	}
	snapshot, err := restored.snapshot(ctx, house.ID, ss.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rooms[0].Game == nil || snapshot.Rooms[0].Game.URL != "https://codenames.game/r/backup-night" {
		t.Fatal("game lost")
	}
	if _, err = restored.invitePreview(ctx, invite); err != nil {
		t.Fatalf("invite lost: %v", err)
	}
	// Restored writes are independent from the original database.
	if err = restored.updateHouse(ctx, house.ID, ss.ID, "Restored House"); err != nil {
		t.Fatal(err)
	}
	live, err := original.snapshot(ctx, house.ID, ss.ID, nil, 0)
	if err != nil || live.Name != "Backup House" {
		t.Fatalf("restore modified live database: %v", err)
	}
}

func originalPath(t *testing.T, store *Store) string {
	t.Helper()
	var seq int
	var name, path string
	if err := store.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupRejectsMissingAndCorruptFiles(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.db")
	if err := BackupDatabase(ctx, missing, filepath.Join(directory, "out.db")); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created missing source")
	}
	corrupt := filepath.Join(directory, "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDatabase(ctx, corrupt); err == nil {
		t.Fatal("corruption accepted")
	}
}
