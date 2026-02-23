package main

import (
	"fmt"
	"testing"
)

// scriptedPrompter is a test double that returns pre-queued answers.
type scriptedPrompter struct {
	answers []string
	idx     int
}

func (p *scriptedPrompter) Ask(_ string) (string, error) {
	if p.idx >= len(p.answers) {
		return "", fmt.Errorf("prompter: queue exhausted after %d answers", p.idx)
	}
	ans := p.answers[p.idx]
	p.idx++
	return ans, nil
}

func TestScriptedPrompter_ReturnsAnswersInOrder(t *testing.T) {
	p := &scriptedPrompter{answers: []string{"first", "second"}}
	a1, _ := p.Ask("q1")
	a2, _ := p.Ask("q2")
	if a1 != "first" || a2 != "second" {
		t.Errorf("got %q, %q; want \"first\", \"second\"", a1, a2)
	}
}

func TestScriptedPrompter_ExhaustedReturnsError(t *testing.T) {
	p := &scriptedPrompter{answers: []string{"only"}}
	p.Ask("q1")
	_, err := p.Ask("q2")
	if err == nil {
		t.Error("expected error when queue is exhausted")
	}
}
