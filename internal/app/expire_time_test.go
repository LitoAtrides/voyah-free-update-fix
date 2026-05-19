package app

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSetOTATaskExpireTime(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	defer db.Close()

	insertTaskDataRow(t, db, `{"task_id":42,"expire_time":100,"packages_info":[{"ecu":"A"}]}`)

	if err := SetOTATaskExpireTime(db, 1848642286); err != nil {
		t.Fatalf("SetOTATaskExpireTime returned error: %v", err)
	}

	got, ok, err := GetOTATaskExpireTime(db)
	if err != nil {
		t.Fatalf("GetOTATaskExpireTime returned error: %v", err)
	}

	if !ok {
		t.Fatal("expected expire_time to be present")
	}

	if got != 1848642286 {
		t.Fatalf("unexpected expire_time: got %d, want %d", got, 1848642286)
	}

	taskData := extractTaskDataMap(t, db)

	rawExpireTime, ok := taskData["expire_time"]
	if !ok {
		t.Fatal("expire_time missing after update")
	}

	if rawExpireTime != float64(1848642286) {
		t.Fatalf("unexpected expire_time in taskData: got %v, want %d", rawExpireTime, 1848642286)
	}

	packagesInfo, ok := taskData["packages_info"].([]any)
	if !ok {
		t.Fatalf("packages_info has unexpected type: %T", taskData["packages_info"])
	}

	if len(packagesInfo) != 1 {
		t.Fatalf("unexpected packages_info length: %d", len(packagesInfo))
	}

	encoded, err := json.Marshal(taskData)
	if err != nil {
		t.Fatalf("failed to marshal taskData: %v", err)
	}

	if len(encoded) == 0 {
		t.Fatal("unexpected empty marshalled taskData")
	}
}

func TestIsExpireTimeExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.Local)

	if isExpireTimeExpired(time.Date(2026, 5, 17, 12, 0, 0, 0, time.Local).Unix(), now) {
		t.Fatal("expire_time equal to now must not be treated as expired")
	}

	if !isExpireTimeExpired(time.Date(2026, 5, 17, 11, 59, 59, 0, time.Local).Unix(), now) {
		t.Fatal("past expire_time must be treated as expired")
	}

	if isExpireTimeExpired(time.Date(2026, 5, 17, 12, 0, 1, 0, time.Local).Unix(), now) {
		t.Fatal("future expire_time must not be treated as expired")
	}
}
