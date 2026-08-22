package themes

import "fmt"

type Theme struct {
	Schema int               `json:"schema"`
	Name   string            `json:"name"`
	Tokens map[string]string `json:"tokens"`
}

var builtin = map[string]Theme{
	"light-corporate": {Schema: 1, Name: "light-corporate", Tokens: map[string]string{"surface": "#FFFFFF", "ink": "#172033", "accent": "#1E40AF"}},
}

func Lookup(name string) (Theme, error) {
	theme, ok := builtin[name]
	if !ok {
		return Theme{}, fmt.Errorf("unknown slides theme %q", name)
	}
	return theme, nil
}
