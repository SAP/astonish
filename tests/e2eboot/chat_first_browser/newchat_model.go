package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
)

const chatModelContract = "chat-model-v1"

var chatModelRequired = []string{"title-thinking", "title-web-result", "question-reload-once", "schedule-independent", "paused-interactive", "candidate-node", "question-restart", "schedule-consent"}

// No old workflow receipt, partial result, or skipped run can satisfy this contract.
func validateChatModelReceipts(receipts map[string]smokeReceipt, candidate string) error {
	for _, key := range chatModelRequired {
		r := receipts[key]
		if r.Candidate != candidate || r.BotID == "" || r.Home == "" || r.Detail == "" {
			return fmt.Errorf("missing title-first receipt: %s", key)
		}
	}
	return nil
}

func newChatModelWorkflow(basePage *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, base string, evidence map[string]any, save func(), pass func(string)) {
	evidence["contract"] = chatModelContract
	receipts := map[string]smokeReceipt{}
	checkpoint := filepath.Join(root, "chat-model-checkpoint.json")
	type checkpointData struct {
		Candidate string
		Actions   map[string]bool
		Bots      map[string]string
		Receipts  map[string]smokeReceipt
	}
	cp := checkpointData{Candidate: state["binary_sha256"].(string), Actions: map[string]bool{}, Bots: map[string]string{}, Receipts: receipts}
	if raw, err := os.ReadFile(checkpoint); err == nil {
		must(json.Unmarshal(raw, &cp))
		if cp.Candidate != state["binary_sha256"] {
			panic("title-first candidate changed")
		}
		receipts = cp.Receipts
	}
	persist := func() {
		raw, err := json.MarshalIndent(cp, "", "  ")
		must(err)
		must(os.WriteFile(checkpoint, raw, 0600))
	}
	stage := func(s string) {
		evidence["stage"] = s
		save()
		fmt.Println("STAGE", s, time.Now().UTC().Format(time.RFC3339))
	}
	page := func() *rod.Page { return independentPageTimeout(basePage, 40*time.Second) }
	capture := func(name, path string) string {
		raw := api("GET", path, nil)
		must(os.WriteFile(filepath.Join(root, "chat-model-"+name+".json"), []byte(raw), 0600))
		return raw
	}
	receipt := func(key, id, detail string) {
		receipts[key] = smokeReceipt{BotID: id, Candidate: cp.Candidate, Home: base + "/#/chat/bots/personal/" + id, Detail: detail}
		persist()
		evidence["receipts"] = receipts
		pass(key)
	}
	create := func(key, title string) string {
		stage("create-" + key)
		if id := cp.Bots[key]; id != "" {
			page().MustNavigate(base + "/#/chat/bots/personal/" + id)
			return id
		}
		if cp.Actions["create-"+key] {
			panic("creation already attempted; inspect owned state, never duplicate")
		}
		page().MustNavigate(base + "/#/chat")
		page().MustElementR("button", "^New Bot$").MustClick()
		page().MustElement(`[aria-label="Bot name"]`).MustInput(title)
		cp.Actions["create-"+key] = true
		persist()
		page().MustElementR("button", "^Start conversation$").MustClick()
		page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		home := page().MustInfo().URL
		parts := strings.Split(home, "/")
		id := parts[len(parts)-1]
		if !strings.Contains(home, "/#/chat/bots/personal/") {
			panic("creation did not open owned Home")
		}
		cp.Bots[key] = id
		persist()
		state["bot_id"] = id
		raw, _ := json.MarshalIndent(state, "", "  ")
		must(os.WriteFile(statePath, raw, 0600))
		return id
	}
	runs := func(id string) []store.BotRun {
		var v struct{ Runs []store.BotRun }
		must(json.Unmarshal([]byte(capture(id+"-runs", "/api/studio/bots/"+id+"/runs?scope=personal")), &v))
		return v.Runs
	}
	bot := func(id string) store.Bot {
		var v store.Bot
		must(json.Unmarshal([]byte(capture(id+"-bot", "/api/studio/bots/"+id+"?scope=personal")), &v))
		return v
	}
	approvals := func(id string) []store.BotApproval {
		var v struct{ Approvals []store.BotApproval }
		must(json.Unmarshal([]byte(capture(id+"-approvals", "/api/studio/bots/"+id+"/approvals?scope=personal")), &v))
		return v.Approvals
	}
	noSchedules := func(id string) {
		b := bot(id)
		var v struct {
			Triggers []struct {
				Enabled bool `json:"enabled"`
			}
		}
		must(json.Unmarshal([]byte(capture(id+"-version", fmt.Sprintf("/api/studio/bots/%s/versions/%d?scope=personal", id, b.Version))), &v))
		for _, t := range v.Triggers {
			if t.Enabled {
				panic("schedule enabled without explicit consent")
			}
		}
	}
	id := create("weather", "Weather in Orlando, FL today")
	if receipts["title-thinking"].Detail == "" {
		stage("title-thinking")
		page().MustElementR(`[aria-label="Bot workspace"]`, "Thinking").MustVisible()
		receipt("title-thinking", id, "Native title creation showed existing Thinking indicator before any follow-up message")
	}
	stage("title-real-provider")
	boundedWait("real title web work", 12*time.Minute, func() bool {
		rr := runs(id)
		if len(rr) != 1 {
			if len(rr) > 1 {
				panic("duplicate initial title run")
			}
			return false
		}
		r := rr[0]
		if receipts["candidate-node"].Detail == "" && r.SessionID.String() != "00000000-0000-0000-0000-000000000000" {
			name := smokeContainerName(r.SessionID)
			// Product chroot node executable, not host daemon or an unrelated image.
			out, err := exec.Command("docker", "exec", name, "sha256sum", "/sandbox/rootfs/usr/local/bin/astonish").CombinedOutput()
			if err == nil && strings.HasPrefix(string(out), cp.Candidate+" ") {
				receipt("candidate-node", id, "Physical session "+name+" node SHA256 matches candidate: "+string(out))
			}
		}
		if !r.Status.Terminal() {
			return false
		}
		if string(r.Status) != "succeeded" {
			panic("title run not succeeded: " + string(r.Status) + " " + r.Report.Summary)
		}
		b := bot(id)
		history := capture(id+"-history", "/api/studio/sessions/"+b.HomeSessionID.String())
		stream := capture(id+"-events", "/api/studio/bots/"+id+"/sessions/"+r.SessionID.String()+"/stream?scope=personal")
		if !hasRealWebEvidence(history) || !hasRealWebEvidence(stream) {
			panic("successful title lacks correlated actual web tool/result evidence")
		}
		messages, err := chatModelMessages(history)
		must(err)
		selector := fmt.Sprintf(`[aria-label="Bot workspace"] [data-message-index="%d"]`, chatModelAnswerIndex(messages))
		answerElement := page().MustElement(selector)
		// Sources belongs to this answer; do not require duplicate model prose citations.
		if pill, err := answerElement.Element(`button.source-citation-pill`); err == nil {
			pill.MustClick()
		}
		text := answerElement.MustText()
		var hrefs []string
		must(json.Unmarshal([]byte(answerElement.MustEval(`function(){return JSON.stringify(Array.from(this.querySelectorAll('a[href^="http"]')).filter(a=>a.getClientRects().length).map(a=>a.href))}`).Str()), &hrefs))
		if !chatModelVisibleAnswer(history, text, hrefs) {
			panic("weather answer lacks actual visible response/source link")
		}
		noSchedules(id)
		receipt("title-web-result", id, "Single successful title run; genuine web call/result and sourced Orlando answer in same Home")
		return true
	})
	id = create("ambiguous", "Weather for my trip")
	stage("ambiguous-question")
	var question store.BotApproval
	boundedWait("existing ask_user question", 8*time.Minute, func() bool {
		for _, a := range approvals(id) {
			if a.Interaction != nil && string(a.Status) == "pending" {
				question = a
				return true
			}
		}
		for _, run := range runs(id) {
			if run.Status.Terminal() {
				// Recheck after observing terminal state to avoid a publication race.
				for _, a := range approvals(id) {
					if a.Interaction != nil && a.Status == store.ApprovalPending {
						question = a
						return true
					}
				}
				panic(fmt.Sprintf("ambiguous title run terminal without ask_user question: %s: %s", run.Status, run.Report.Summary))
			}
		}
		return false
	})
	// Restart with the question pending: authentication and process-local execution
	// authorization are separate, and neither may replay the original title.
	stage("question-restart")
	beforeRestart := capture(id+"-before-restart", "/api/studio/sessions/"+bot(id).HomeSessionID.String())
	beforeRuns := len(runs(id))
	if cp.Actions["question-restart-attempt"] {
		panic("restart already attempted; inspect checkpoint, never repeat")
	}
	cp.Actions["question-restart-attempt"] = true
	persist()
	out, restartErr := exec.Command("python3", "tests/e2eboot/local_bot_smoke_launcher.py", "--state", statePath, "--restart-owned").CombinedOutput()
	if restartErr != nil {
		panic(fmt.Sprintf("owned restart failed: %s: %v", out, restartErr))
	}
	must(redactedAuthentication(func() {
		raw, err := os.ReadFile(filepath.Join(root, "credentials.json"))
		must(err)
		var credentials map[string]string
		must(json.Unmarshal(raw, &credentials))
		api("POST", "/api/auth/login", credentials)
	}))
	if beforeRestart != capture(id+"-after-restart", "/api/studio/sessions/"+bot(id).HomeSessionID.String()) || len(runs(id)) != beforeRuns {
		panic("restart changed retained history or replayed title")
	}
	page().MustReload()
	selector := `[data-review-id="` + question.ID.String() + `"]`
	page().MustElement(selector).MustVisible()
	if cp.Actions["answer-"+question.ID.String()] {
		panic("answer already attempted; inspect saved interaction, never replay")
	}
	cp.Actions["answer-"+question.ID.String()] = true
	persist()
	// Existing Chat custom-answer input, never a harness-created question widget.
	custom, err := page().Element(selector + ` textarea`)
	if err == nil {
		custom.MustInput("Orlando, Florida, today. Just this lookup; no recurring schedule.")
		page().MustElementR(selector+" button", "(?i)send|submit|answer").MustClick()
	} else {
		page().MustElementR(selector+` [role="radio"]`, "(?i)orlando|florida").MustClick()
	}
	stage("question-continuation")
	cp.Actions["continue-"+question.ID.String()] = true
	persist()
	page().MustElementR(selector+" button", "^Continue interactive$").MustClick()
	boundedWait("saved answer consumed", 8*time.Minute, func() bool {
		for _, a := range approvals(id) {
			if a.ID == question.ID {
				return string(a.Status) == "consumed"
			}
		}
		return false
	})
	page().MustReload()
	time.Sleep(2 * time.Second)
	for _, a := range approvals(id) {
		if a.ID == question.ID && string(a.Status) != "consumed" {
			panic("answer changed on reload")
		}
	}
	if page().MustEval(`s=>!!document.querySelector(s+' [role="radio"]:not([aria-disabled="true"])')`, selector).Bool() {
		panic("answered question actionable after reload")
	}
	receipt("question-restart", id, "Owned daemon restart retained pending question and run count; reauthenticated, then explicitly Continue interactive once")
	receipt("question-reload-once", id, "Saved ask_user answered once using native Chat UI; consumed answer persisted across reload")
	noSchedules(id)
	receipt("schedule-independent", id, "Useful interactive lookup and explicit no-recurring answer left all schedules disabled")
	// Paused interactive acceptance is mandatory; no skipped/partial whole-workflow pass.
	stage("paused-interactive")
	send := func(key, text string) {
		boundedWait("previous interactive turn finished", 8*time.Minute, func() bool {
			for _, r := range runs(id) {
				if r.Status == store.RunQueued || r.Status == store.RunRunning {
					return false
				}
			}
			return true
		})
		if cp.Actions[key] {
			return
		}
		cp.Actions[key] = true
		persist()
		page().MustElement(`[aria-label="Bot workspace"] [data-testid="chat-input"]`).MustInput(text)
		page().MustElement(`[aria-label="Bot workspace"] [data-testid="send-button"]`).MustClick()
	}
	send("pause-request", "Pause your independent work. I still want to chat with you here. Do not create or enable any schedule.")
	var proposal store.BotControlProposal
	boundedWait("pause review", 8*time.Minute, func() bool {
		var v struct{ Proposals []store.BotControlProposal }
		must(json.Unmarshal([]byte(capture(id+"-proposals", "/api/studio/bots/"+id+"/proposals?scope=personal")), &v))
		for _, p := range v.Proposals {
			if p.Payload.Operation == store.BotControlConfigurationOperation && p.Payload.Configuration != nil && p.Payload.Configuration.Pause && p.Status == store.BotControlProposed {
				proposal = p
				return true
			}
		}
		return false
	})
	if cp.Actions["pause-consent"] {
		panic("pause consent already attempted; inspect checkpoint, never replay")
	}
	cp.Actions["pause-consent"] = true
	persist()
	page().MustElementR(`[data-review-id="`+proposal.ID.String()+`"] button`, "^Confirm exact change$").MustClick()
	boundedWait("paused persisted", time.Minute, func() bool { return bot(id).Status == store.BotPaused })
	before := map[string]bool{}
	for _, r := range runs(id) {
		before[r.ID.String()] = true
	}
	send("paused-message", "What is the current weather in Orlando, Florida? Look it up now, just once. Leave independent work paused and do not schedule anything.")
	boundedWait("interactive work while paused", 12*time.Minute, func() bool {
		for _, r := range runs(id) {
			if !before[r.ID.String()] {
				if !r.Status.Terminal() {
					return false
				}
				if string(r.Status) != "succeeded" {
					panic("paused interactive run failed: " + r.Report.Summary)
				}
				if bot(id).Status != store.BotPaused {
					panic("interactive work resumed autonomous Bot")
				}
				noSchedules(id)
				stream := capture(id+"-paused-events", "/api/studio/bots/"+id+"/sessions/"+r.SessionID.String()+"/stream?scope=personal")
				if !hasRealWebEvidence(stream) {
					panic("paused lookup lacks correlated actual web tool/result")
				}
				return true
			}
		}
		return false
	})
	page().MustReload()
	receipt("paused-interactive", id, "Native pause review explicitly confirmed; subsequent interactive lookup succeeded with real tools while Bot remained paused and schedules disabled")
	// Positive optional scheduling is a separate decision, never inferred from
	// title creation or an ask_user answer. Use the original unpaused weather Bot.
	id = cp.Bots["weather"]
	page().MustNavigate(base + "/#/chat/bots/personal/" + id)
	stage("schedule-choice")
	noSchedules(id)
	send("schedule-choice-request", "I may want this Orlando weather lookup every day at 09:00 America/New_York. First ask me using ask_user whether I want recurring weather or only one-time lookups. Do not create a schedule or change permissions until I choose and separately confirm exact reviews.")
	var scheduleQuestion store.BotApproval
	boundedWait("optional scheduling choice", 8*time.Minute, func() bool {
		for _, a := range approvals(id) {
			if a.Interaction != nil && a.Status == store.ApprovalPending {
				scheduleQuestion = a
				return true
			}
		}
		return false
	})
	noSchedules(id)
	choiceSelector := `[data-review-id="` + scheduleQuestion.ID.String() + `"]`
	cp.Actions["schedule-choice-answer"] = true
	persist()
	var choice struct {
		Kind    string `json:"kind"`
		Options []struct {
			Label string `json:"label"`
		} `json:"options"`
	}
	must(json.Unmarshal(scheduleQuestion.Interaction.Payload, &choice))
	label := ""
	if choice.Kind == "yesno" {
		label = "Yes"
	} else {
		for _, option := range choice.Options {
			lower := strings.ToLower(option.Label)
			if (strings.Contains(lower, "daily") || strings.Contains(lower, "recurring")) && !strings.Contains(lower, "no ") && !strings.Contains(lower, "not ") {
				if label != "" {
					panic("ambiguous schedule options")
				}
				label = option.Label
			}
		}
	}
	if label == "" {
		panic("no explicit positive recurring choice")
	}
	page().MustElementR(choiceSelector+` button, `+choiceSelector+` [role="radio"]`, "^"+regexp.QuoteMeta(label)+"$").MustClick()
	noSchedules(id)
	// Request a review explicitly; answering the question is intent, not authority.
	boundedWait("schedule choice consumed", 8*time.Minute, func() bool {
		for _, a := range approvals(id) {
			if a.ID == scheduleQuestion.ID {
				return a.Status == store.ApprovalConsumed
			}
		}
		return false
	})
	send("schedule-review-request", "Please propose the exact daily 09:00 America/New_York Orlando weather schedule, enabled. If independent read-only weather lookup needs permissions, propose those separately with only web_fetch and wttr.in, no writes, publishing, contacts, or activation. I will review each exact change.")
	var scheduled store.BotControlProposal
	boundedWait("separate permission and schedule reviews", 8*time.Minute, func() bool {
		var v struct{ Proposals []store.BotControlProposal }
		must(json.Unmarshal([]byte(capture(id+"-schedule-proposals", "/api/studio/bots/"+id+"/proposals?scope=personal")), &v))
		for _, p := range v.Proposals {
			if p.Status != store.BotControlProposed {
				continue
			}
			if p.Payload.Operation == store.BotControlConfigurationOperation {
				must(validateChatModelWeatherPermission(p))
				key := "permission-" + p.ID.String()
				if cp.Actions[key] {
					continue
				}
				cp.Actions[key] = true
				persist()
				noSchedules(id)
				page().MustElementR(`[data-review-id="`+p.ID.String()+`"] button`, "^Confirm exact change$").MustClick()
				return false
			}
			if p.Payload.Operation == store.BotControlScheduleCreate && p.Payload.Schedule != nil {
				t := p.Payload.Schedule.Trigger
				if t.Cron != "0 9 * * *" || t.Timezone != "America/New_York" || !t.Enabled {
					panic("schedule differs from exact requested cadence")
				}
				scheduled = p
				return true
			}
		}
		return false
	})
	noSchedules(id)
	cp.Actions["schedule-exact-consent"] = true
	persist()
	page().MustElementR(`[data-review-id="`+scheduled.ID.String()+`"] button`, "^Confirm exact change$").MustClick()
	boundedWait("schedule saved after exact consent", time.Minute, func() bool {
		b := bot(id)
		var v store.BotVersion
		must(json.Unmarshal([]byte(capture(id+"-scheduled-version", fmt.Sprintf("/api/studio/bots/%s/versions/%d?scope=personal", id, b.Version))), &v))
		for _, t := range v.Triggers {
			if t.ID == scheduled.Payload.Schedule.Trigger.ID && t.Enabled && t.Cron == "0 9 * * *" && t.Timezone == "America/New_York" {
				return true
			}
		}
		return false
	})
	receipt("schedule-consent", id, "Optional ask_user choice did not enable schedules; separate bounded permission review and exact daily schedule consent persisted the requested trigger; no activation or scheduled tick claimed")
	must(validateChatModelReceipts(receipts, cp.Candidate))
	evidence["status"] = "verified"
	evidence["candidate"] = cp.Candidate
	save()
}

func validateChatModelWeatherPermission(p store.BotControlProposal) error {
	c := p.Payload.Configuration
	if c == nil || c.Pause || c.Autonomy.CanContactPeople || c.Autonomy.CanPublish || len(c.Autonomy.AllowedPaths) > 0 || c.Autonomy.MaxSpendPerRunMinor != 0 || c.Autonomy.MaxSpendPerMonthMinor != 0 {
		return fmt.Errorf("weather permission review exceeds read-only scope")
	}
	if len(c.Autonomy.AllowedTools) != 1 || c.Autonomy.AllowedTools[0] != "web_fetch" || len(c.Autonomy.AllowedHosts) != 1 || c.Autonomy.AllowedHosts[0] != "wttr.in" {
		return fmt.Errorf("weather permission review must be only web_fetch at wttr.in")
	}
	return nil
}

func hasRealWebEvidence(history string) bool {
	messages, err := chatModelMessages(history)
	if err != nil {
		return false
	}
	calls := map[string]string{}
	for _, m := range messages {
		if m.Type == "tool_call" && m.ToolID != "" && (m.ToolName == "web_search" || m.ToolName == "web_fetch" || m.ToolName == "browser_navigate") {
			calls[m.ToolID] = m.ToolName
		}
		if m.Type == "tool_result" && m.ToolID != "" && calls[m.ToolID] == m.ToolName && successfulWebResult(m.ToolResult) {
			return true
		}
	}
	return false
}

type chatModelMessage struct {
	InvocationID string          `json:"invocationId"`
	Type         string          `json:"type"`
	Content      string          `json:"content"`
	ToolName     string          `json:"toolName"`
	ToolID       string          `json:"toolId"`
	ToolResult   json.RawMessage `json:"toolResult"`
}

// Studio GET is flat; only the finite Bot SSE contains raw ADK content.parts.
func chatModelMessages(raw string) ([]chatModelMessage, error) {
	var h struct {
		Messages []chatModelMessage `json:"messages"`
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		err := json.Unmarshal([]byte(raw), &h)
		return h.Messages, err
	}
	var messages []chatModelMessage
	complete := false
	for _, frame := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n\n") {
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) == 0 {
			continue
		}
		var e struct {
			Persisted bool `json:"persisted"`
			Content   *struct {
				Parts []struct {
					Call   *struct{ ID, Name string } `json:"functionCall"`
					Result *struct {
						ID, Name string
						Response json.RawMessage `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"content"`
		}
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e); err != nil {
			return nil, err
		}
		complete = complete || e.Persisted
		if e.Content == nil {
			continue
		}
		for _, p := range e.Content.Parts {
			if p.Call != nil {
				messages = append(messages, chatModelMessage{Type: "tool_call", ToolName: p.Call.Name, ToolID: p.Call.ID})
			}
			if p.Result != nil {
				messages = append(messages, chatModelMessage{Type: "tool_result", ToolName: p.Result.Name, ToolID: p.Result.ID, ToolResult: p.Result.Response})
			}
		}
	}
	if !complete {
		return nil, fmt.Errorf("incomplete persisted event stream")
	}
	return messages, nil
}
func successfulWebResult(raw json.RawMessage) bool {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil || len(v) == 0 {
		return false
	}
	if e, ok := v["error"]; ok && e != nil && e != "" {
		return false
	}
	if s, ok := v["status"].(float64); ok && (s < 200 || s >= 400) {
		return false
	}
	if s, ok := v["success"].(bool); ok && !s {
		return false
	}
	return len(raw) > 50
}
func chatModelAnswerIndex(messages []chatModelMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == "agent" && !strings.HasPrefix(messages[i].Content, "## Run report") {
			return i
		}
	}
	return -1
}

func chatModelVisibleAnswer(history, visible string, hrefs []string) bool {
	messages, err := chatModelMessages(history)
	if err != nil || !hasRealWebEvidence(history) {
		return false
	}
	index := chatModelAnswerIndex(messages)
	if index < 0 {
		return false
	}
	answer := messages[index].Content
	// Only successful, paired web calls in this user turn and invocation can
	// supply a citation. Memory/tool payload URLs and earlier turns cannot.
	start := index
	for start > 0 && messages[start-1].Type != "user" {
		start--
	}
	calls := map[string]string{}
	sources := map[string]bool{}
	for _, m := range messages[start:index] {
		if m.InvocationID != messages[index].InvocationID {
			continue
		}
		if m.Type == "tool_call" && m.ToolID != "" && (m.ToolName == "web_fetch" || m.ToolName == "web_search" || m.ToolName == "browser_navigate") {
			calls[m.ToolID] = m.ToolName
		}
		if m.Type != "tool_result" || m.ToolID == "" || calls[m.ToolID] != m.ToolName || !successfulWebResult(m.ToolResult) {
			continue
		}
		var result struct {
			URL     string `json:"url"`
			Results []struct {
				URL string `json:"url"`
			} `json:"results"`
		}
		if json.Unmarshal(m.ToolResult, &result) == nil {
			sources[result.URL] = true
			for _, r := range result.Results {
				sources[r.URL] = true
			}
		}
	}
	if len(answer) < 40 || !strings.Contains(strings.ToLower(answer), "orlando") {
		return false
	}
	matched := 0
	for _, word := range strings.Fields(answer) {
		word = strings.Trim(word, "*#_.,:;()[]!")
		if len(word) > 3 && strings.Contains(visible, word) {
			matched++
		}
	}
	if matched < 8 {
		return false
	}
	for _, href := range hrefs {
		if (strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "http://")) && sources[href] {
			return true
		}
	}
	return false
}
