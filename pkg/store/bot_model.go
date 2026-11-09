package store

import "context"

// ResolveBotModelSelection reads only server-selected owner scope. A failed read
// is never treated as an absent setting. Pins override inherited defaults without
// mutating any settings; a partial pin is invalid rather than silently ignored.
func ResolveBotModelSelection(ctx context.Context, pin *SessionPin, platform PlatformSettingsStore, org OrgSettingsStore, team SettingsStore, personal PersonalSettingsStore) (SessionPin, error) {
	if pin != nil && (pin.Provider != "" || pin.Model != "") {
		if pin.Provider == "" || pin.Model == "" {
			return SessionPin{}, ErrBotInvalid
		}
		return *pin, nil
	}
	var result SessionPin
	merge := func(p, m string) {
		if p != "" {
			result.Provider = p
		}
		if m != "" {
			result.Model = m
		}
	}
	if platform != nil {
		s, err := platform.Get(ctx)
		if err != nil {
			return result, err
		}
		if s != nil {
			merge(s.DefaultProvider, s.DefaultModel)
		}
	}
	if org != nil {
		s, err := org.Get(ctx)
		if err != nil {
			return result, err
		}
		if s != nil {
			merge(s.DefaultProvider, s.DefaultModel)
		}
	}
	if team != nil {
		s, err := team.Get(ctx)
		if err != nil {
			return result, err
		}
		if s != nil {
			merge(s.DefaultProvider, s.DefaultModel)
		}
	}
	if personal != nil {
		s, err := personal.Get(ctx)
		if err != nil {
			return result, err
		}
		if s != nil {
			merge(s.DefaultProvider, s.DefaultModel)
		}
	}
	if result.Provider == "" || result.Model == "" {
		return result, ErrBotUnsupported
	}
	return result, nil
}
