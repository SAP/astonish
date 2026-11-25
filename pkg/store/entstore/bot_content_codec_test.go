package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentVersionCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	codec := newBotContentCodec(dir, scope)
	v := store.DefaultBotVersion()
	v.BotID = uuid.New()
	v.Charter = "SENTINEL-charter"
	v.Persona.Soul = "SENTINEL-soul"
	v.Persona.Identity = "SENTINEL-identity"
	v.Persona.User = "SENTINEL-user"
	v.Persona.Avatar = "SENTINEL-avatar"
	v.Delivery.Preferences = "SENTINEL-preferences"
	v.ChangeReason = "SENTINEL-reason"
	v.Triggers = []store.TriggerSpec{{ID: uuid.New(), Instructions: "SENTINEL-instructions", Invocation: "SENTINEL-invocation", Kind: store.TriggerManual}}
	data, err := codec.encodeVersion(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SENTINEL") {
		t.Fatal("content persisted in SQL envelope")
	}
	reopened := newBotContentCodec(dir, scope)
	got, err := reopened.decodeVersion(t.Context(), v.BotID, v.Version, data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, *got) {
		t.Fatalf("roundtrip differs: %+v", got)
	}
	old, _ := botJSONMap(v)
	if _, err = codec.decodeVersion(t.Context(), v.BotID, v.Version, old); !errors.Is(err, errBotDevelopmentContentFormat) {
		t.Fatal("old inline content accepted", err)
	}
	data["extra"] = "SENTINEL"
	if _, err = codec.decodeVersion(t.Context(), v.BotID, v.Version, data); err == nil {
		t.Fatal("unallowlisted envelope accepted")
	}
}

func TestBotContentVersionCodecMissingStorage(t *testing.T) {
	codec := &botContentCodec{Open: func(context.Context, store.BotID) (store.BotContentStaging, error) { return nil, store.ErrBotNotFound }}
	v := store.DefaultBotVersion()
	v.BotID = uuid.New()
	if _, err := codec.encodeVersion(t.Context(), v); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatal("missing storage fallback", err)
	}
}
