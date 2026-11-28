package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type skillFormatModel struct {
	*workStreamModel
	check func(context.Context, *model.LLMRequest)
	after func()
}

func (m *skillFormatModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.check(ctx, request)
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range m.workStreamModel.GenerateContent(ctx, request, stream) {
			if !yield(response, err) {
				return
			}
		}
		if m.after != nil {
			m.after()
		}
	}
}

func TestBotSkillJSONFormatRecovery(t *testing.T) {
	for _, learning := range []bool{false, true} {
		name, prompt, schema := "orientation", botSkillOrientationPrompt, botSkillSchema()
		valid := `{"kind":"no_skill_needed","skill_id":"","title":"","responsibility":"","method":[],"expectations":[],"replace_expectation_ids":[],"reason":"Greeting"}`
		if learning {
			name, prompt, schema = "learning", botSkillLearningPrompt, botSkillLearningSchema()
			valid = `{"method":[],"output_format":"","evaluation_rules":[],"evidence_refs":[],"reason":""}`
		}
		for _, scenario := range []string{"recover", "repeated-malformed", "schema-mismatch", "repeated-schema", "tool-call", "partial", "interrupted", "provider-error", "cancel-before-correction"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				base, req, _ := botChatTestRequest(t)
				ctx, cancel := context.WithCancel(base)
				defer cancel()
				first := workStreamText(`{} {}`, false, 4, 2)
				second := workStreamText(valid, false, 7, 3)
				wantCalls, wantErr := 1, ""
				switch scenario {
				case "recover":
					wantCalls = 2
				case "repeated-malformed":
					wantCalls, wantErr, second = 2, "after format correction", workStreamText("{", false, 7, 3)
				case "schema-mismatch", "repeated-schema":
					first = workStreamText(`{"unknown":true}`, false, 4, 2)
					wantCalls = 2
					if scenario == "repeated-schema" {
						second = workStreamText(`{"unknown":true}`, false, 7, 3)
						wantErr = "invalid schema"
					}
				case "tool-call":
					first.response.Content.Parts = append(first.response.Content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{Name: "forbidden"}})
					wantErr = "tool call"
				case "partial":
					first.response.Partial = true
					wantErr = "incomplete"
				case "interrupted":
					first.response.Interrupted = true
					wantErr = "interrupted"
				case "provider-error":
					first.err = errors.New("provider failed")
					wantErr = "provider failed"
				case "cancel-before-correction":
					wantErr = "context canceled"
				}
				var out any = &botSkillOrientationAnswer{}
				if learning {
					out = &botSkillLearningAnswer{}
				}
				before, _ := json.Marshal(out)
				var original string
				llm := &skillFormatModel{workStreamModel: &workStreamModel{t: t, attempts: [][]workStreamEvent{{first}, {second}}}}
				llm.check = func(callCtx context.Context, request *model.LLMRequest) {
					if !reflect.DeepEqual(request.Config.ResponseJsonSchema, schema) || request.Config.ResponseMIMEType != "application/json" || request.Config.MaxOutputTokens != 8192 {
						t.Fatal("schema or output budget changed")
					}
					if callCtx.Err() != nil {
						t.Fatal("called model after cancellation")
					}
					input := request.Contents[0].Parts[0].Text
					if llm.calls == 0 {
						original = input
					} else if input != original || !strings.Contains(request.Config.SystemInstruction.Parts[0].Text, "FORMAT CORRECTION ONLY") || !strings.Contains(request.Contents[1].Parts[0].Text, "UNTRUSTED") {
						t.Fatal("correction changed original input or trust boundary")
					}
				}
				if scenario == "cancel-before-correction" {
					llm.after = cancel
				}
				usage := store.BotUsage{InputTokens: 10, OutputTokens: 20, ToolCalls: 2}
				err := botWorkJSON(ctx, req, llm, prompt, map[string]string{"source": "original"}, out, &usage)
				if wantErr == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), wantErr) {
					t.Fatalf("want %s got %v", wantErr, err)
				}
				if wantErr != "" {
					after, _ := json.Marshal(out)
					if string(after) != string(before) {
						t.Fatal("failure mutated output")
					}
				}
				wantInput, wantOutput := int64(14), int64(22)
				if wantCalls == 2 {
					wantInput, wantOutput = 21, 25
				}
				if llm.calls != wantCalls || usage.InputTokens != wantInput || usage.OutputTokens != wantOutput || usage.ToolCalls != 2 {
					t.Fatalf("calls=%d usage=%+v", llm.calls, usage)
				}
			})
		}
	}
}

func TestBotSkillJSONFormatRecoverySharedBudget(t *testing.T) {
	for _, prompt := range []string{botSkillOrientationPrompt, botSkillLearningPrompt} {
		for _, limited := range []bool{false, true} {
			t.Run(strings.SplitN(prompt, "\n", 2)[0]+"/limited="+fmt.Sprint(limited), func(t *testing.T) {
				ctx, req, _ := botChatTestRequest(t)
				limit := int64(20000)
				if limited {
					limit = 8200
				}
				budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				req.Budget = budget
				valid := `{"method":[],"output_format":"","evaluation_rules":[],"evidence_refs":[],"reason":""}`
				var out any = &botSkillLearningAnswer{}
				if prompt == botSkillOrientationPrompt {
					valid = `{"kind":"no_skill_needed","skill_id":"","title":"","responsibility":"","method":[],"expectations":[],"reason":"Greeting"}`
					out = &botSkillOrientationAnswer{}
				}
				llm := &workStreamModel{t: t, attempts: [][]workStreamEvent{{workStreamText("{", false, 4, 2)}, {workStreamText(valid, false, 7, 3)}}}
				metered, err := provider.NewBudgetLLM(llm, budget, provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) {
					if llm.calls > 0 {
						return 8195, nil
					}
					return 4, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				var usage store.BotUsage
				err = botWorkJSON(ctx, req, metered, prompt, map[string]string{"source": "original"}, out, &usage)
				calls, input, output := 2, int64(11), int64(5)
				if limited {
					calls, input, output = 1, 4, 2
					if !errors.Is(err, bot.ErrBudgetExceeded) {
						t.Fatalf("correction escaped original budget: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				charged := budget.Usage()
				if llm.calls != calls || usage.InputTokens != input || usage.OutputTokens != output || charged.InputTokens != input || charged.OutputTokens != output {
					t.Fatalf("calls=%d usage=%+v budget=%+v", llm.calls, usage, charged)
				}
			})
		}
	}
}

func TestBotSkillInvalidPinBlocksProduction(t *testing.T) {
	for _, phase := range []store.BotWorkPhase{store.BotWorkPreparing, store.BotWorkProducing, store.BotWorkRepairing} {
		for _, defect := range []string{"digest", "foreign-bot", "expectation-text", "missing-skill-state"} {
			t.Run(string(phase)+"/"+defect, func(t *testing.T) {
				ctx, req, _ := botChatTestRequest(t)
				p := store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, BotID: req.Bot.ID, RunID: req.Run.ID, SessionID: req.Run.SessionID, EventID: "owner", SourceDigest: botSkillHash("Report receipt"), Quote: "Report receipt", RecordedAt: time.Now().UTC()}
				r := store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: req.Bot.ID, SkillID: uuid.New(), Revision: 1, Responsibility: "Report receipt", Expectations: []store.BotSkillExpectation{{ID: "report", Text: p.Quote, Provenance: p}}, Method: []string{"Read receipt"}, ChangeReason: "Owner assignment", Provenance: []store.BotSkillProvenance{p}}
				r.Digest = r.ContentDigest()
				pin := &store.BotSkillInvocation{SkillID: r.SkillID, Revision: r.Revision, Digest: r.Digest, Snapshot: r, Intent: "Report receipt"}
				w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter, Skill: pin})
				w.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented}
				w.Phase = phase
				if phase != store.BotWorkPreparing {
					w.Criteria = []store.BotWorkCriterion{{ID: "report", SourceIDs: []string{"assignment", "charter"}, Text: "Report receipt"}}
				}
				if phase == store.BotWorkRepairing {
					w.Repairs = 1
					w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "prior", Text: "Incomplete"}
					w.Candidate.Digest = w.Candidate.ContentDigest()
					w.Unresolved = []string{"Complete report"}
				}
				if err := w.Validate(); err != nil {
					t.Fatalf("invalid test setup: %v", err)
				}
				switch defect {
				case "digest":
					w.Contract.Skill.Snapshot.Method[0] = "Tampered method"
				case "foreign-bot":
					w.Contract.Skill.Snapshot.BotID = uuid.New()
				case "expectation-text":
					w.Contract.Skill.Snapshot.Expectations[0].Text = "Unrelated requirement"
				case "missing-skill-state":
					w.SkillState = nil
				}
				// Rehash the outer contract so the nested validator, not only a
				// stale outer digest, has to reject the malicious pin.
				if defect != "digest" {
					w.Contract.Skill.Snapshot.Digest = w.Contract.Skill.Snapshot.ContentDigest()
					w.Contract.Skill.Digest = w.Contract.Skill.Snapshot.Digest
				}
				w.RequirementsDigest = store.NewBotWorkState(w.Contract).RequirementsDigest
				req.Work = w.Clone()
				if phase != store.BotWorkPreparing {
					ctx = context.WithValue(ctx, botResumeInteractionKey{}, &store.BotApproval{})
				}
				mdl := &workAssessmentModel{t: t}
				calls := 0
				result, err := runBotWork(ctx, req, &BotChatResources{Work: &workMemoryStore{state: w}}, mdl, false, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
					calls++
					return bot.RunResult{Status: store.RunSucceeded}, nil
				})
				if err == nil || result.Status == store.RunSucceeded || calls != 0 || mdl.calls != 0 {
					t.Fatalf("invalid pin reached execution: producer=%d model=%d status=%s err=%v", calls, mdl.calls, result.Status, err)
				}
			})
		}
	}
}

func TestBotSkillFeedback(t *testing.T) {
	owner := func(id, text string) store.BotSkillExpectation {
		return store.BotSkillExpectation{ID: id, Text: text, Provenance: store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, Quote: text}}
	}
	old := []store.BotSkillExpectation{owner("units", "Use Celsius"), owner("source", "Cite source")}
	identical, err := botSkillFeedback(old, []store.BotSkillExpectation{owner("new", "Use Celsius")}, []string{"units"}, 2)
	if err != nil || !reflect.DeepEqual(identical, old) {
		t.Fatalf("explicit identical correction changed order or evidence: %+v %v", identical, err)
	}
	next, err := botSkillFeedback(old, []store.BotSkillExpectation{owner("new", "Use Fahrenheit")}, []string{"units"}, 2)
	if err != nil || len(next) != 2 || next[0].Text != "Cite source" || next[1].Text != "Use Fahrenheit" {
		t.Fatalf("replacement: %+v %v", next, err)
	}
	again, err := botSkillFeedback(next, []store.BotSkillExpectation{owner("new", "Use Fahrenheit")}, nil, 3)
	if err != nil || len(again) != 2 || again[1].ID != next[1].ID || again[1].Text != next[1].Text {
		t.Fatal("identical feedback changed knowledge")
	}
	if _, err := botSkillFeedback(old, []store.BotSkillExpectation{owner("new", "Use Fahrenheit")}, []string{"unknown"}, 2); err == nil {
		t.Fatal("unknown replacement accepted")
	}
}

func TestBotAgentSkillOwnerSourceDoesNotForgeMessage(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Run.Trigger = store.TriggerManual
	req.Run.UserEventID = ""
	req.Instructions = "Owner-admitted task"
	w := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1, Contract: store.BotWorkContract{Instructions: req.Instructions}}
	// No session/transcript adapters: resolving an admitted task must not append
	// a synthetic user event, including when the saved goal is the owner source.
	p, text, err := botSkillOwnerSource(ctx, req, &BotChatResources{}, w)
	if err != nil || p == nil || p.Source != store.BotSkillSourceRunAdmission || p.Level != store.BotSkillOwnerSpecified || p.EventID != "" || p.GoalID != nil || p.BotID != req.Bot.ID || p.RunID != req.Run.ID || p.SessionID != req.Run.SessionID || p.SourceDigest != botSkillHash(req.Instructions) || text != req.Instructions || !p.RecordedAt.Equal(req.Run.CreatedAt) {
		t.Fatalf("admission must retain canonical provenance without a fabricated event: %+v %q %v", p, text, err)
	}
	w.Contract.Goals = []store.BotGoal{{ID: uuid.New(), Revision: 1, Title: "Observe receipt"}}
	p, text, err = botSkillOwnerSource(ctx, req, &BotChatResources{}, w)
	if err != nil || p == nil || p.GoalID == nil || p.EventID != "" || !strings.Contains(text, "Observe receipt") {
		t.Fatalf("saved goal did not remain the canonical source: %+v %q %v", p, text, err)
	}
}

type importedScheduleSkillStore struct {
	store.BotSkillStore
	list   func(context.Context, store.BotID, store.BotSkillPage) (*store.BotSkillList, error)
	orient func(context.Context, store.BotSkillOrientation) (*store.BotSkillOrientationResult, error)
}

func (s *importedScheduleSkillStore) ListSkills(ctx context.Context, id store.BotID, page store.BotSkillPage) (*store.BotSkillList, error) {
	return s.list(ctx, id, page)
}

func (s *importedScheduleSkillStore) OrientSkill(ctx context.Context, q store.BotSkillOrientation) (*store.BotSkillOrientationResult, error) {
	return s.orient(ctx, q)
}

func TestBotAgentImportedScheduleSelection(t *testing.T) {
	for _, scenario := range []string{"matching", "older-import", "no-import", "wrong-trigger", "changed-instructions", "future-import", "missing-trigger", "manual", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Run.Trigger, req.Run.TriggerID, req.Run.Version = store.TriggerCron, uuid.New(), 3
			req.Run.SkillID = nil
			req.Version.Triggers = []store.TriggerSpec{{ID: req.Run.TriggerID, Instructions: "Read current receipt"}}
			selected := store.BotSkill{ID: uuid.New(), BotID: req.Bot.ID, Legacy: &store.BotSkillLegacyAssociation{TriggerID: req.Run.TriggerID, Version: 3, InstructionsDigest: botSkillHash("Read current receipt")}}
			list := &store.BotSkillList{Skills: []store.BotSkill{selected}}
			wantPin := scenario == "matching" || scenario == "older-import"
			switch scenario {
			case "older-import":
				selected.Legacy.Version = 2
			case "no-import":
				list.Skills[0].Legacy = nil
			case "wrong-trigger":
				selected.Legacy.TriggerID = uuid.New()
			case "changed-instructions":
				req.Version.Triggers[0].Instructions = "Read another receipt"
			case "future-import":
				selected.Legacy.Version = 4
			case "missing-trigger":
				req.Version.Triggers = nil
			case "manual":
				req.Run.Trigger = store.TriggerManual
			case "ambiguous":
				other := selected
				other.ID = uuid.New()
				list.Skills = append(list.Skills, other)
			}
			w := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1, Revision: 7, Phase: store.BotWorkWorking, Contract: store.BotWorkContract{Instructions: req.Instructions}}
			before := w.Clone()
			lists, orientations := 0, 0
			skills := &importedScheduleSkillStore{}
			skills.list = func(_ context.Context, id store.BotID, page store.BotSkillPage) (*store.BotSkillList, error) {
				lists++
				if id != req.Bot.ID || page.Limit != store.BotSkillMaxSkills || page.IncludeArchived {
					t.Fatalf("unbounded or unscoped import lookup: %s %+v", id, page)
				}
				return list, nil
			}
			skills.orient = func(_ context.Context, q store.BotSkillOrientation) (*store.BotSkillOrientationResult, error) {
				orientations++
				if !wantPin || q.Kind != store.BotSkillUseExisting || q.SkillID == nil || *q.SkillID != selected.ID || q.Mutation.BotID != req.Bot.ID || q.Mutation.ActorID != req.Bot.OwnerID || q.Mutation.ExpectedRevision != w.Revision || !reflect.DeepEqual(q.Mutation.Lease, req.Run.Lease) || q.Intent != w.Contract.Instructions {
					t.Fatalf("incorrect imported selection: %+v", q)
				}
				next := w.Clone()
				next.Contract.Skill = &store.BotSkillInvocation{SkillID: selected.ID}
				return &store.BotSkillOrientationResult{Work: next}, nil
			}
			mdl := &workAssessmentModel{t: t}
			usage := store.BotUsage{}
			got, err := orientBotSkill(ctx, req, &BotChatResources{Skills: skills}, mdl, w, &usage)
			if scenario == "ambiguous" {
				if !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("ambiguous import must fail closed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantLists := 1
			if scenario == "manual" {
				wantLists = 0
			}
			wantOrientations := 0
			if wantPin {
				wantOrientations = 1
			}
			if lists != wantLists || orientations != wantOrientations || mdl.calls != 0 || usage != (store.BotUsage{}) {
				t.Fatalf("unexpected lookup/selection/model usage: %d/%d/%d %+v", lists, orientations, mdl.calls, usage)
			}
			if !reflect.DeepEqual(w, before) {
				t.Fatal("selection mutated original work")
			}
			if wantPin {
				if got == nil || got.Contract.Skill == nil || got.Contract.Skill.SkillID != selected.ID {
					t.Fatalf("import was not pinned: %+v", got)
				}
				// Once pinned, later calls must not re-list or re-pin the run.
				again, err := orientBotSkill(ctx, req, &BotChatResources{Skills: skills}, mdl, got, &usage)
				if err != nil || again != got || lists != 1 || orientations != 1 || mdl.calls != 0 {
					t.Fatalf("repinned imported run: %v", err)
				}
			} else if got == nil || got.Contract.Skill != nil || got.SkillState != nil {
				t.Fatalf("unmatched or ambiguous import fabricated a skill: %+v", got)
			}
		})
	}
}

func TestBotSkillRequirementGrounding(t *testing.T) {
	for _, kind := range []store.BotWorkVerificationKind{store.BotWorkVerificationSourceURL, store.BotWorkVerificationRetrievalTime} {
		for _, level := range []store.BotSkillEvidenceLevel{store.BotSkillOwnerSpecified, store.BotSkillProposed, store.BotSkillObserved} {
			t.Run(string(kind)+"/"+string(level), func(t *testing.T) {
				contract := store.BotWorkContract{Instructions: "Repeat the report", Skill: &store.BotSkillInvocation{Snapshot: store.BotSkillRevision{
					Expectations: []store.BotSkillExpectation{{Text: "Include source URLs and retrieval timestamps", Provenance: store.BotSkillProvenance{Level: level}}},
					Method:       []string{"Include source URLs and retrieval timestamps"},
				}}}
				criterion := store.BotWorkCriterion{SourceIDs: []string{"assignment"}, VerificationKind: kind}
				if got := botWorkMechanicalRequirementGrounded(contract, criterion); got != (level == store.BotSkillOwnerSpecified) {
					t.Fatalf("grounding=%v for provenance %s", got, level)
				}
				criterion.SourceIDs = []string{"charter"}
				if botWorkMechanicalRequirementGrounded(contract, criterion) {
					t.Fatal("uncited skill promoted into charter requirements")
				}
			})
		}
	}
}
