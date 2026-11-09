package store

import (
	"context"
	"errors"
	"testing"
)

type botModelPlatformSettings struct {
	PlatformSettingsStore
	value *PlatformSettings
	err   error
}

func (s botModelPlatformSettings) Get(context.Context) (*PlatformSettings, error) {
	return s.value, s.err
}

type botModelOrgSettings struct {
	OrgSettingsStore
	value *OrgSettings
	err   error
}

func (s botModelOrgSettings) Get(context.Context) (*OrgSettings, error) { return s.value, s.err }

type botModelTeamSettings struct {
	SettingsStore
	value *TeamSettings
	err   error
}

func (s botModelTeamSettings) Get(context.Context) (*TeamSettings, error) { return s.value, s.err }

type botModelPersonalSettings struct {
	PersonalSettingsStore
	value *PersonalSettings
	err   error
}

func (s botModelPersonalSettings) Get(context.Context) (*PersonalSettings, error) {
	return s.value, s.err
}

func TestResolveBotModelSelection(t *testing.T) {
	ctx := context.Background()
	platform := botModelPlatformSettings{value: &PlatformSettings{DefaultProvider: "platform", DefaultModel: "base"}}
	org := botModelOrgSettings{value: &OrgSettings{DefaultProvider: "org"}}
	team := botModelTeamSettings{value: &TeamSettings{DefaultModel: "team-model"}}
	personal := botModelPersonalSettings{value: &PersonalSettings{DefaultProvider: "owner"}}
	for _, tc := range []struct {
		name     string
		pin      *SessionPin
		personal PersonalSettingsStore
		want     SessionPin
	}{
		{"team", nil, nil, SessionPin{Provider: "org", Model: "team-model"}},
		{"personal", nil, personal, SessionPin{Provider: "owner", Model: "team-model"}},
		{"empty-pin", &SessionPin{}, personal, SessionPin{Provider: "owner", Model: "team-model"}},
		{"explicit-pin", &SessionPin{Provider: "pinned", Model: "pinned-model"}, personal, SessionPin{Provider: "pinned", Model: "pinned-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBotModelSelection(ctx, tc.pin, platform, org, team, tc.personal)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	failure := errors.New("settings unavailable")
	for _, layer := range []string{"platform", "org", "team", "personal"} {
		t.Run(layer+"-read-fails-closed", func(t *testing.T) {
			p, o, g, u := platform, org, team, personal
			switch layer {
			case "platform":
				p.err = failure
			case "org":
				o.err = failure
			case "team":
				g.err = failure
			case "personal":
				u.err = failure
			}
			if _, err := ResolveBotModelSelection(ctx, nil, p, o, g, u); !errors.Is(err, failure) {
				t.Fatalf("read error ignored: %v", err)
			}
		})
	}
	for _, pin := range []*SessionPin{{Provider: "partial"}, {Model: "partial"}} {
		if _, err := ResolveBotModelSelection(ctx, pin, platform, org, team, personal); !errors.Is(err, ErrBotInvalid) {
			t.Fatalf("partial pin accepted: %v", err)
		}
	}
	if _, err := ResolveBotModelSelection(ctx, nil, nil, nil, nil, nil); !errors.Is(err, ErrBotUnsupported) {
		t.Fatalf("missing defaults: %v", err)
	}
}
