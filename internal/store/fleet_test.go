package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEncryptedStateBackupAndScheduleClaim(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleet.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateMiner(ctx, "Heater", "braiins", "192.0.2.5", 4028)
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-private-password"
	if err = st.SaveCredentials(ctx, id, Credentials{Username: "root", Password: secret}); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveProfile(ctx, Profile{MinerID: id, Name: "quiet", Operation: "autotuning", Values: map[string]any{"powerTarget": float64(700), "password": secret}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Credentials(ctx, id)
	if err != nil || got.Password != secret {
		t.Fatalf("credentials: %+v %v", got, err)
	}
	p, err := st.Profiles(ctx, id)
	if err != nil || len(p) != 1 || p[0].Values["password"] != secret {
		t.Fatalf("profiles: %+v %v", p, err)
	}
	for _, table := range []string{"credentials", "profiles"} {
		var b []byte
		if err = st.db.QueryRow("SELECT ciphertext FROM " + table).Scan(&b); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("plaintext secret persisted")
		}
		b[len(b)-1] ^= 1
		var v any
		if st.unseal(b, &v) == nil {
			t.Fatal("tampered ciphertext accepted")
		}
	}
	info, err := os.Stat(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose Unix owner/group permission bits through Chmod.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions: %v %v", info, err)
	}
	if err = st.SaveSchedule(ctx, Schedule{MinerID: id, Name: "night", Timezone: "UTC", Days: "1", At: "22:00", Operation: "autotuning", ProfileID: p[0].ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	schedules, err := st.Schedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sid := schedules[0].ID
	for i := 0; i < 2; i++ {
		claimed, e := st.ClaimSchedule(ctx, sid, "2026-10-05T22:00Z")
		if e != nil || claimed != (i == 0) {
			t.Fatalf("claim %d: %t %v", i, claimed, e)
		}
	}
	backup, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(backup)
	content, err := os.ReadFile(backup)
	if err != nil || bytes.Contains(content, []byte(secret)) {
		t.Fatal("backup leaked plaintext secret", err)
	}
	if err = st.DeleteProfile(ctx, p[0].ID); err != nil {
		t.Fatal(err)
	}
	schedules, err = st.Schedules(ctx)
	if err != nil || len(schedules) != 0 {
		t.Fatal("profile deletion failed to remove schedule", err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.Credentials(ctx, id)
	if err != nil || got.Password != secret {
		t.Fatal("credentials did not survive reopen", err)
	}
}

func TestImportIsAtomicAndDisablesSchedules(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	miners := []Miner{{ID: 10, Name: "portable", Kind: "braiins", Host: "192.0.2.4", Port: 4028, Enabled: true}}
	profiles := []Profile{{ID: 20, MinerID: 10, Name: "quiet", Operation: "autotuning", Values: map[string]any{"powerTarget": float64(700)}}}
	schedules := []Schedule{{MinerID: 10, Name: "night", Timezone: "UTC", Days: "1", At: "22:00", ProfileID: 20, Operation: "autotuning", Enabled: true}}
	added, err := st.ImportConfiguration(ctx, miners, profiles, schedules, map[string]any{"currency": "USD"})
	if err != nil || len(added) != 1 {
		t.Fatal(added, err)
	}
	got, err := st.Schedules(ctx)
	if err != nil || len(got) != 1 || got[0].Enabled || got[0].MinerID != added[0].ID {
		t.Fatal(got, err)
	}
	profiles[0].Values = map[string]any{"bad": make(chan int)}
	if _, err = st.ImportConfiguration(ctx, miners, profiles, schedules, nil); err == nil {
		t.Fatal("invalid encrypted profile imported")
	}
	all, err := st.ListMiners(ctx)
	if err != nil || len(all) != 1 {
		t.Fatal("failed import was not rolled back", all, err)
	}
}

func TestMissingEncryptionKeyDoesNotSilentlyReinitialize(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleet.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := st.CreateMiner(ctx, "test", "braiins", "192.0.2.20", 4028)
	if err = st.SaveCredentials(ctx, id, Credentials{Password: "fixture-password"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err = os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, path); err == nil {
		reopened.Close()
		t.Fatal("encrypted database opened with replacement key")
	}
	if _, err = os.Stat(path + ".key"); !os.IsNotExist(err) {
		t.Fatal("missing key was silently recreated")
	}
}
