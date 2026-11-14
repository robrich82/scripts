package main

import (
	"testing"
)

// scriptedAsk answers each prompt in turn and records what it was asked.
func scriptedAsk(answers ...string) (ask func(string) (string, error), prompts *[]string) {
	prompts = new([]string)
	return func(msg string) (string, error) {
		*prompts = append(*prompts, msg)
		if len(*prompts) > len(answers) {
			return "", nil
		}
		return answers[len(*prompts)-1], nil
	}, prompts
}

func TestAskRating(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		def     NZRating
		want    NZRating
		prompts int
	}{
		{"code", []string{"R16"}, RatingNone, RatingR16, 1},
		{"code with spaces and lower case", []string{" rp 16 "}, RatingG, RatingRP16, 1},
		// The bare age codes are ratings, not menu positions.
		{"13", []string{"13"}, RatingNone, Rating13, 1},
		{"15", []string{"15"}, RatingNone, Rating15, 1},
		{"16", []string{"16"}, RatingNone, Rating16, 1},
		{"18", []string{"18"}, RatingNone, Rating18, 1},
		{"a number that isn't a rating, then a code", []string{"12", "1", "M"}, RatingNone, RatingM, 3},
		{"invalid code, then a code", []string{"R17", "R18"}, RatingNone, RatingR18, 2},
		{"blank takes the default", []string{""}, RatingM, RatingM, 1},
		{"blank without a default", []string{""}, RatingNone, RatingNone, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask, prompts := scriptedAsk(tc.answers...)
			var got NZRating
			out := captureStdout(t, func() { got = askRating(ask, "header", tc.def, "do it") })
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
			if len(*prompts) != tc.prompts {
				t.Errorf("%d prompts; want %d: %q", len(*prompts), tc.prompts, *prompts)
			}
			echo := "  → " + string(tc.want) + "\n"
			if tc.want == RatingNone {
				echo = ""
			}
			if out != echo {
				t.Errorf("printed %q; want the choice echoed as %q", out, echo)
			}
		})
	}
}

func TestAskRating_PromptText(t *testing.T) {
	ask, prompts := scriptedAsk("x", "")
	captureStdout(t, func() { askRating(ask, `NZ rating for "Coco": M`, RatingM, "accept M") })

	want := "NZ rating for \"Coco\": M\nEnter rating (" + validRatings + ") [M], or blank to accept M: "
	if (*prompts)[0] != want {
		t.Errorf("prompt = %q; want %q", (*prompts)[0], want)
	}
	if want := `  "x" is not a valid rating. Enter (` + validRatings + `) or blank to accept M: `; (*prompts)[1] != want {
		t.Errorf("re-prompt = %q; want %q", (*prompts)[1], want)
	}

	ask, prompts = scriptedAsk("")
	captureStdout(t, func() { askRating(ask, "header", RatingNone, "skip") })
	if want := "header\nEnter rating (" + validRatings + "), or blank to skip: "; (*prompts)[0] != want {
		t.Errorf("prompt = %q; want %q, with no default", (*prompts)[0], want)
	}
}
