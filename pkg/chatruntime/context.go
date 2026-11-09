package chatruntime

import (
	"context"
	"reflect"
)

// capabilityContext keeps cancellation and execution identity/policy/budget on
// the parent, but removes inherited store/credential/sandbox capabilities. All
// keys in these packages are masked, including stores not currently bound here.
// This deliberately fails closed for future context stores in those packages.
type capabilityContext struct{ context.Context }

func (c capabilityContext) Value(key any) any {
	t := reflect.TypeOf(key)
	if t != nil {
		switch t.PkgPath() {
		case "github.com/SAP/astonish/pkg/store", "github.com/SAP/astonish/pkg/credentials", "github.com/SAP/astonish/pkg/sandbox":
			return nil
		case "github.com/SAP/astonish/pkg/agent":
			switch t.Name() {
			case "llmOverrideKey", "requestMCPGroupsKey", "promptOverridesKey":
				return nil
			}
		}
	}
	return c.Context.Value(key)
}
