package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A never-checked app must not persist a 0001-01-01 sentinel timestamp: the
// omitzero tag drops the key entirely, and it round-trips back to the zero
// time that list.go already renders as an empty cell.
func TestAppStateOmitsZeroLastChecked(t *testing.T) {
	data, err := json.Marshal(&AppState{LastSeenVersion: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "last_checked") {
		t.Errorf("marshalled state = %s, want no last_checked key", data)
	}

	var back AppState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.LastChecked.IsZero() {
		t.Errorf("LastChecked = %v, want the zero time", back.LastChecked)
	}
}

func TestAppStateKeepsRealLastChecked(t *testing.T) {
	want := time.Date(2026, 8, 27, 9, 40, 31, 0, time.UTC)
	data, err := json.Marshal(&AppState{LastChecked: want})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "last_checked") {
		t.Fatalf("marshalled state = %s, want a last_checked key", data)
	}

	var back AppState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.LastChecked.Equal(want) {
		t.Errorf("LastChecked = %v, want %v", back.LastChecked, want)
	}
}
