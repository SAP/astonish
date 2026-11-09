package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/model"
)

func TestBotProposalGoalCriteriaDeclaredAndValidated(t *testing.T) {
	for _, op := range []string{"goal_create", "goal_edit"} {
		for _, field := range []string{"", `,"completion_criteria":null`, `,"completion_criteria":""`, `,"completion_criteria":"   "`, `,"completion_criteria":"Each weather update is easy to understand."`} {
			t.Run(op+field, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
				f.services.Bots, f.services.PersonalBots = repo, repo
				binding := &botFactoryBinding{owner: store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				raw, err := newBotProposalTool(binding)
				if err != nil {
					t.Fatal(err)
				}
				tool := raw.(*botProposalTool)
				ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: context.Background()}, sid: f.req.Run.SessionID.String()}
				req := &model.LLMRequest{}
				if err = tool.ProcessRequest(ctx, req); err != nil {
					t.Fatal(err)
				}
				var schema jsonschema.Schema
				data, _ := json.Marshal(req.Config.Tools[0].FunctionDeclarations[0].ParametersJsonSchema)
				if err = json.Unmarshal(data, &schema); err != nil {
					t.Fatal(err)
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				var args map[string]any
				wire := `{"payload":{"operation":"` + op + `","reason":"Owner requested a goal","goal":{"expected_revision":1,"goal":{"title":"Orlando weather clarity","description":"Make each Orlando weather update easy to understand.","status":"active"` + field + `}}}}`
				if err = json.Unmarshal([]byte(wire), &args); err != nil {
					t.Fatal(err)
				}
				valid := strings.Contains(field, "Each weather")
				if err = resolved.Validate(args); (err == nil) != valid {
					t.Errorf("declared goal schema validity=%v: %v", valid, err)
				}
				// The mandated variant must expose the identical required goal semantics.
				mandated, err := botMandatedProposalInputSchema().Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = mandated.Validate(args); (err == nil) != valid {
					t.Errorf("mandated goal schema validity=%v: %v", valid, err)
				}
				out, err := tool.Run(ctx, args)
				if valid {
					if err != nil || out["proposal_id"] == nil || repo.calls != 1 {
						t.Fatalf("valid goal: %+v %v calls=%d", out, err, repo.calls)
					}
				} else if !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "completion_criteria") || repo.calls != 0 {
					t.Fatalf("need source-turn field error without capture, got %+v %v calls=%d", out, err, repo.calls)
				}
			})
		}
	}
}
