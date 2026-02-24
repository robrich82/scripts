package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Prompter asks the user a question and returns their trimmed answer.
type Prompter interface {
	Ask(prompt string) (string, error)
}

type stdinPrompter struct {
	scanner *bufio.Scanner
}

func newStdinPrompter() Prompter {
	return &stdinPrompter{scanner: bufio.NewScanner(os.Stdin)}
}

func (p *stdinPrompter) Ask(prompt string) (string, error) {
	fmt.Print(prompt)
	if !p.scanner.Scan() {
		return "", p.scanner.Err()
	}
	return strings.TrimRight(p.scanner.Text(), "\r"), nil
}
