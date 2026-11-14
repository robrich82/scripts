package main

import (
	"fmt"
	"strings"
)

// askRating asks for an NZ rating code. header opens the prompt; def is what a
// blank answer gives (RatingNone: none) and blankDoes says so. The choice is
// echoed back, so a slip shows before it is cached.
func askRating(ask func(string) (string, error), header string, def NZRating, blankDoes string) NZRating {
	question := "Enter rating (" + validRatings + ")"
	if def != RatingNone {
		question += " [" + string(def) + "]"
	}
	msg := header + "\n" + question + ", or blank to " + blankDoes + ": "
	for {
		ans, _ := ask(msg)
		ans = strings.TrimSpace(ans)
		rating, ok := def, true
		if ans != "" {
			rating, ok = ParseNZRating(ans)
		}
		if ok {
			if rating != RatingNone {
				fmt.Printf("  → %s\n", rating)
			}
			return rating
		}
		msg = fmt.Sprintf("  %q is not a valid rating. Enter (%s) or blank to %s: ", ans, validRatings, blankDoes)
	}
}
