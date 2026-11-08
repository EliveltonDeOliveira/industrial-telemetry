package main

import (
	"errors"
	"strings"
	"testing"
)

func TestNext(t *testing.T) {
	allowed := map[[2]status]bool{
		{statusOpen, statusInProgress}:     true,
		{statusOpen, statusCanceled}:       true,
		{statusInProgress, statusDone}:     true,
		{statusInProgress, statusCanceled}: true,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				got, err := change{Status: &to}.next(from)
				var conflict *conflictError
				switch {
				case allowed[[2]status{from, to}] && (err != nil || got != to):
					t.Errorf("got %q, %v; want %q", got, err, to)
				case !allowed[[2]status{from, to}] && !errors.As(err, &conflict):
					t.Errorf("got %q, %v; want a conflict", got, err)
				}
			})
		}
		// Only the assignee: the status stays, unless the order is closed.
		t.Run(string(from)+", assignee only", func(t *testing.T) {
			assignee := "tecnico-1"
			got, err := change{Assignee: &assignee}.next(from)
			closed := from == statusDone || from == statusCanceled
			if closed != (err != nil) || (!closed && got != from) {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	str := func(s string) *string { return &s }
	st := func(s status) *status { return &s }
	tests := []struct {
		name   string
		change change
		want   string // start of the error, empty for none
	}{
		{"status", change{Status: st(statusInProgress)}, ""},
		{"assignee", change{Assignee: str("tecnico-1")}, ""},
		{"both", change{Status: st(statusInProgress), Assignee: str("tecnico-1")}, ""},
		{"longest assignee", change{Assignee: str(strings.Repeat("ç", maxAssigneeLen))}, ""},
		{"nothing", change{}, "status:"},
		{"unknown status", change{Status: st("fechada")}, "status:"},
		{"blank assignee", change{Assignee: str("   ")}, "assignee:"},
		{"assignee too long", change{Assignee: str(strings.Repeat("a", maxAssigneeLen+1))}, "assignee:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.change.validate()
			if (tt.want == "") != (err == nil) || (err != nil && !strings.HasPrefix(err.Error(), tt.want)) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}

	c := change{Assignee: str("  tecnico-1 ")}
	if err := c.validate(); err != nil || *c.Assignee != "tecnico-1" {
		t.Errorf("got %q, %v; want the assignee trimmed", *c.Assignee, err)
	}
}
