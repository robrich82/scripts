package main

import (
	"strings"
)

// NZRating represents a New Zealand content rating value.
type NZRating string

const (
	RatingG    NZRating = "G"
	RatingPG   NZRating = "PG"
	RatingM    NZRating = "M"
	RatingRP13 NZRating = "RP13"
	Rating13   NZRating = "13"
	RatingR13  NZRating = "R13"
	RatingRP15 NZRating = "RP15"
	Rating15   NZRating = "15"
	RatingR15  NZRating = "R15"
	RatingRP16 NZRating = "RP16"
	Rating16   NZRating = "16"
	RatingR16  NZRating = "R16"
	RatingR    NZRating = "R" // generic Restricted (used by some older NZ classifications)
	RatingRP18 NZRating = "RP18"
	Rating18   NZRating = "18"
	RatingR18  NZRating = "R18"
	RatingNone NZRating = "" // unknown / unrated
)

// nzRatingOrder maps each rating to its severity index for comparison.
// Higher index = more restrictive.
// Order within each age group: RP (parental guidance) < plain number < R (strictly restricted).
var nzRatingOrder = map[NZRating]int{
	RatingG:    0,
	RatingPG:   1,
	RatingM:    2,
	RatingRP13: 3,
	Rating13:   4,
	RatingR13:  5,
	RatingRP15: 6,
	Rating15:   7,
	RatingR15:  8,
	RatingRP16: 9,
	Rating16:   10,
	RatingR16:  11,
	RatingR:    12,
	RatingRP18: 13,
	Rating18:   14,
	RatingR18:  15,
}

// MaxRating returns the more restrictive of two NZ ratings.
// RatingNone is treated as the least restrictive.
func MaxRating(a, b NZRating) NZRating {
	if a == RatingNone {
		return b
	}
	if b == RatingNone {
		return a
	}
	if nzRatingOrder[a] >= nzRatingOrder[b] {
		return a
	}
	return b
}

// validRatings lists every accepted NZ rating, used in prompts and flag errors.
const validRatings = "G/PG/M/13/RP13/R13/15/RP15/R15/16/RP16/R16/R/18/RP18/R18"

// ParseNZRating normalises a raw string to a canonical NZRating.
// Accepts case-insensitive input and strips internal spaces (e.g. "r 16" → "R16").
// Returns (rating, true) if recognised, (RatingNone, false) if not.
func ParseNZRating(s string) (NZRating, bool) {
	// Normalise: uppercase, strip spaces.
	norm := strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	r := NZRating(norm)
	if _, ok := nzRatingOrder[r]; ok {
		return r, true
	}
	return RatingNone, false
}

// RatingSource is the interface for any system that can provide an NZ content
// rating for a given movie.
type RatingSource interface {
	// Name returns a human-readable label used in log/prompt messages.
	Name() string
	// FetchRating returns the NZ rating for the movie identified by key+title.
	// key is a TMDB ID (if known) or a "title:YEAR" fallback string.
	// Returns (RatingNone, nil) when the source has no data.
	// Returns (RatingNone, err) on network or parse failure.
	FetchRating(key, title string) (NZRating, error)
}
