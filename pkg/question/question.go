// Package question defines user answers, never permission or execution grants.
package question

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type Kind string

const (
	YesNo           Kind = "yesno"
	Select          Kind = "select"
	Text            Kind = "text"
	MaxAnswerLength      = 4096
)

var ErrInvalid = errors.New("invalid question or answer")

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}
type Question struct {
	QuestionID        string   `json:"questionId,omitempty"`
	Kind              Kind     `json:"kind"`
	Prompt            string   `json:"prompt"`
	Options           []Option `json:"options,omitempty"`
	AllowCustomAnswer *bool    `json:"allow_custom_answer,omitempty"`
}

func (q Question) Validate() error {
	if strings.TrimSpace(q.Prompt) == "" || (q.AllowCustomAnswer != nil && q.Kind != Select) {
		return ErrInvalid
	}
	switch q.Kind {
	case YesNo, Text:
	case Select:
		if len(q.Options) == 0 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || seen[o.ID] {
				return ErrInvalid
			}
			seen[o.ID] = true
		}
	default:
		return ErrInvalid
	}
	return nil
}
func ValidateText(answer string) error {
	if !utf8.ValidString(answer) || strings.TrimSpace(answer) == "" || utf8.RuneCountInString(answer) > MaxAnswerLength {
		return ErrInvalid
	}
	return nil
}
func (q Question) ValidateAnswer(answer string) error {
	if q.Validate() != nil || ValidateText(answer) != nil {
		return ErrInvalid
	}
	switch q.Kind {
	case Text:
		return nil
	case YesNo:
		if answer == "yes" || answer == "no" {
			return nil
		}
	case Select:
		for _, o := range q.Options {
			if o.ID == answer {
				return nil
			}
		}
		if q.AllowCustomAnswer != nil && *q.AllowCustomAnswer {
			return nil
		}
	}
	return ErrInvalid
}
