package main

import "strings"

// naturalLess compares two strings using natural (numeric) sort order,
// case-insensitively. This matches the ordering most CBZ viewers use.
func naturalLess(a, b string) bool {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		aDigit := a[0] >= '0' && a[0] <= '9'
		bDigit := b[0] >= '0' && b[0] <= '9'
		if aDigit && bDigit {
			an, aRest := leadingInt(a)
			bn, bRest := leadingInt(b)
			if an != bn {
				return an < bn
			}
			a, b = aRest, bRest
		} else {
			if a[0] != b[0] {
				return a[0] < b[0]
			}
			a, b = a[1:], b[1:]
		}
	}
	return len(a) < len(b)
}

func leadingInt(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n := 0
	for j := 0; j < i; j++ {
		n = n*10 + int(s[j]-'0')
	}
	return n, s[i:]
}
