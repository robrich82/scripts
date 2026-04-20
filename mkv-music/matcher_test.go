package main

import "testing"

func TestSequenceRatio_SameString(t *testing.T) {
	if r := sequenceRatio("hello", "hello"); r != 1.0 {
		t.Errorf("got %v, want 1.0", r)
	}
}

func TestSequenceRatio_BothEmpty(t *testing.T) {
	if r := sequenceRatio("", ""); r != 1.0 {
		t.Errorf("got %v, want 1.0", r)
	}
}

func TestSequenceRatio_OneEmpty(t *testing.T) {
	if r := sequenceRatio("abc", ""); r != 0.0 {
		t.Errorf("got %v, want 0.0", r)
	}
}

func TestSequenceRatio_PartialMatch(t *testing.T) {
	r := sequenceRatio("abc", "abcd")
	// LCS=3, total=7 → 6/7 ≈ 0.857
	if r <= 0.8 || r >= 1.0 {
		t.Errorf("got %v, want ~0.857", r)
	}
}

func TestSequenceRatio_NoCommon(t *testing.T) {
	r := sequenceRatio("abc", "xyz")
	if r != 0.0 {
		t.Errorf("got %v, want 0.0", r)
	}
}

func TestGetCloseMatch_NoMatch_HighCutoff(t *testing.T) {
	_, ok := getCloseMatch("xyz", []string{"abc", "def"}, 0.9)
	if ok {
		t.Error("expected no match with high cutoff")
	}
}

func TestGetCloseMatch_FindsBestCandidate(t *testing.T) {
	m, ok := getCloseMatch("hello world", []string{"hello world!", "goodbye world"}, 0.5)
	if !ok {
		t.Fatal("expected a match")
	}
	if m != "hello world!" {
		t.Errorf("got %q, want 'hello world!'", m)
	}
}

func TestGetCloseMatch_EmptyChoices(t *testing.T) {
	_, ok := getCloseMatch("anything", []string{}, 0.3)
	if ok {
		t.Error("expected no match for empty choices")
	}
}

func TestRemoveIgnoredParts_RemovesPattern(t *testing.T) {
	result := removeIgnoredParts("Artist (Official Video) Title", []string{"(Official Video) "})
	if result != "Artist Title" {
		t.Errorf("got %q, want 'Artist Title'", result)
	}
}

func TestRemoveIgnoredParts_CollapsesDoubleSpaces(t *testing.T) {
	// Removing "[HD]" from "Artist [HD]  Title" leaves "Artist   Title";
	// the collapse loop reduces that to "Artist Title".
	result := removeIgnoredParts("Artist [HD]  Title", []string{"[HD]"})
	if result != "Artist Title" {
		t.Errorf("got %q, want 'Artist Title'", result)
	}
}

func TestRemoveIgnoredParts_NoPatterns(t *testing.T) {
	result := removeIgnoredParts("Artist - Title", []string{})
	if result != "Artist - Title" {
		t.Errorf("got %q, want 'Artist - Title'", result)
	}
}

func TestRemoveIgnoredParts_MultiplePatterns(t *testing.T) {
	result := removeIgnoredParts("Artist [HD] (Remaster) - Title", []string{"[HD] ", "(Remaster) "})
	if result != "Artist - Title" {
		t.Errorf("got %q, want 'Artist - Title'", result)
	}
}
