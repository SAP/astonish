package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// Exercise the production capture, durable JSON round trip and reviewer input;
// hand-written already-decoded source maps missed the transport-escaping bug.
func capturedContentInput(t *testing.T, fields map[string]any) botWorkContentInput {
	t.Helper()
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
	ctx, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(context.Background(), id))
	cr := &ChatRunner{ctx: ctx}
	cr.observeBotToolOutcome("user", &genai.FunctionResponse{ID: "capture", Name: "http_request", Response: fields})
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: "A material claim.", Revision: 1, EventID: "event", Bodies: cr.completion.reportBodies}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	digest := w.Candidate.Digest
	saved, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var restored store.BotWorkState
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	in := botContentInput(&restored)
	if restored.Candidate.Bodies["tool-response:capture"] != string(raw) || restored.Candidate.ContentDigest() != digest {
		t.Fatal("citation presentation changed captured bytes/digest")
	}
	return in
}

func checkContentQuote(t *testing.T, in botWorkContentInput, ref, quote string, valid bool) {
	t.Helper()
	// Tests name the exact provenance path for readability; production only
	// accepts its short ID, never guesses a receipt/body linkage.
	for id, origin := range in.Origins {
		if origin.CaptureRef+origin.Path == ref {
			ref = id
			break
		}
	}
	review := botWorkContentReview{ContentAssessments: []botWorkContentAssessment{{ItemID: in.Items[0].ID, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Exact captured passage, not a factual attestation", Sources: []botWorkContentSource{{Ref: ref, Quote: quote}}}}}
	if err := applyBotContentReview(in, &review); (err == nil) != valid {
		t.Fatalf("quote %q ref %q valid=%v: %v", quote, ref, valid, err)
	}
}

func TestBotContentDecodedCaptureRoundTrip(t *testing.T) {
	const quote = "Lab says \"AI\".\nPath C:\\models & research."
	in := capturedContentInput(t, map[string]any{"content": quote, "other": "Separate claim.", "body": map[string]any{"a/b~c": []any{"Another\npassage."}}})
	const ref = "tool-response:capture#json:/content"
	checkContentQuote(t, in, ref, quote, true)
	checkContentQuote(t, in, "tool-response:capture", quote, false)
	checkContentQuote(t, in, ref, quote+"Separate claim.", false)
	checkContentQuote(t, in, ref, "Lab says \"AI\".\nPath C:\\models & 90 million users.", false)
	checkContentQuote(t, in, "tool-response:capture#json:/body/a~1b~0c/0", "Another\npassage.", true)
	checkContentQuote(t, in, "tool-response:capture", `\"AI\"`, false)
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	schema := botWorkResponseSchema(botWorkReviewPrompt, raw)
	refs := schema["properties"].(map[string]any)["content_assessments"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["sources"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["ref"].(map[string]any)["enum"].([]string)
	if len(refs) != len(in.Sources) {
		t.Fatal("schema/validator source maps differ")
	}
	for _, ref := range refs {
		if _, ok := in.Sources[ref]; !ok {
			t.Fatal("schema ref not validated", ref)
		}
	}
}

func TestBotContentDecodedNodeBoundaries(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"body": `<rss><item>A &amp; B<em>inner</em> tail</item><item>second</item><item><![CDATA[&amp;]]> &amp;</item></rss>`})
	base := "tool-response:capture#json:/body#xml:/rss[1]/item[1]"
	checkContentQuote(t, in, base+"/text()[1]", "A & B", true)
	checkContentQuote(t, in, base+"/text()[1]", "A & Binner tailsecond", false)
	checkContentQuote(t, in, base+"/text()[2]", " tail", true)
	checkContentQuote(t, in, base+"/em[1]/text()[1]", "inner", true)
	checkContentQuote(t, in, "tool-response:capture#json:/body#xml:/rss[1]/item[3]/text()[1]", "&amp; &", false)
}

func TestBotContentDecodedSourceLimits(t *testing.T) {
	for _, mode := range []string{"views", "bytes", "json-depth", "xml-depth"} {
		t.Run(mode, func(t *testing.T) {
			fields := map[string]any{}
			switch mode {
			case "views":
				values := make([]string, botContentMaxSourceViews+1)
				for i := range values {
					values[i] = "passage"
				}
				fields["content"] = values
			case "bytes":
				fields["content"] = strings.Repeat("x", store.BotWorkMaxCandidateBytes)
			case "json-depth":
				var value any = "text"
				for i := 0; i < botContentMaxSourceDepth+1; i++ {
					value = []any{value}
				}
				fields["content"] = value
			case "xml-depth":
				fields["content"] = "<rss>" + strings.Repeat("<x>", botContentMaxSourceDepth) + "text" + strings.Repeat("</x>", botContentMaxSourceDepth) + "</rss>"
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: "claim", Bodies: map[string]string{"tool-response:limit": string(raw)}}}
			in := botContentInput(w)
			if _, err := json.Marshal(in); err == nil {
				t.Fatal("incomplete source presentation silently marshaled")
			}
			if err := applyBotContentReview(in, &botWorkContentReview{}); err == nil {
				t.Fatal("incomplete source presentation accepted")
			}
			if w.Candidate.Bodies["tool-response:limit"] != string(raw) {
				t.Fatal("oversized raw source omitted")
			}
		})
	}
}

func TestBotContentDecodedHeadersAndRefCollision(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"body": `<document>A &amp; B</document>`, "headers": map[string]any{"Content-Type": "application/xml"}})
	checkContentQuote(t, in, "tool-response:capture#json:/body#xml:/document[1]/text()[1]", "A & B", true)
	// A supplied raw ref must never be overwritten by a derived view.
	sources := map[string]string{"tool-response:capture": `{"content":"decoded"}`, "tool-response:capture#json:/content": "original raw"}
	in = botContentInput(&store.BotWorkState{Candidate: &store.BotWorkCandidate{Bodies: sources}})
	if len(in.Sources) != 1 || in.Sources["source_1"] != "decoded" || in.Origins["source_1"].CaptureRef != "tool-response:capture" {
		t.Fatal("ambiguous origin", in.Origins)
	}
	if sources["tool-response:capture#json:/content"] != "original raw" {
		t.Fatal("raw ref overwritten")
	}
}

func TestBotContentDecodedLegacyAndDeterminism(t *testing.T) {
	for _, raw := range []string{`plain &amp; text`, `{"content":"broken`, `["not an envelope"]`, `"not an envelope"`} {
		sources := map[string]string{"tool-response:legacy": raw}
		in := botContentInput(&store.BotWorkState{Candidate: &store.BotWorkCandidate{Bodies: sources}})
		if in.sourceErr != nil || len(in.Sources) != 0 || sources["tool-response:legacy"] != raw {
			t.Fatalf("legacy source transformed: %#v", in)
		}
	}
	in := capturedContentInput(t, map[string]any{"body": map[string]any{"b": "line\n2", "a": `<rss><item>A &amp; B</item></rss>`}})
	first, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(botContentInput(in.BotWorkState))
		if err != nil || string(first) != string(next) {
			t.Fatal("nondeterministic source views")
		}
	}
	// JSON-looking text is not a second transport envelope.
	nested := capturedContentInput(t, map[string]any{"content": `{"quote":"A\\nB"}`})
	if len(nested.Sources) != 1 || nested.Origins["source_1"].Path != "#json:/content" {
		t.Fatal("recursively decoded string as JSON", nested.Origins)
	}
}

// Public article excerpts from the captured real-review mismatch; no provider
// request, credential, or private capture identifier is needed for the regression.
func TestBotContentNBSPObservedQuote(t *testing.T) {
	for _, tc := range []struct{ name, raw, quote string }{
		{"10-year", "The yield on the\u00a0[10-year U.S. Treasury](https://www.cnbc.com/quotes/US10Y/)\u00a0note — the benchmark for mortgages, auto loans and credit card debt —\u00a0rose more than 11 basis points to 5.107%.", "The yield on the [10-year U.S. Treasury](https://www.cnbc.com/quotes/US10Y/) note — the benchmark for mortgages, auto loans and credit card debt — rose more than 11 basis points to 5.107%."},
		{"30-year", "The longer-dated\u00a0[30-year Treasury bond](https://www.cnbc.com/quotes/US30Y/) yield rose more than 8 basis points to 5.437%.", "The longer-dated [30-year Treasury bond](https://www.cnbc.com/quotes/US30Y/) yield rose more than 8 basis points to 5.437%."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := capturedContentInput(t, map[string]any{"content": tc.raw})
			const base = "tool-response:capture#json:/content"
			checkContentQuote(t, in, base, tc.raw, true)
			checkContentQuote(t, in, base, tc.quote, true)
			checkContentQuote(t, in, base+"#nbsp-space", tc.quote, true)
			checkContentQuote(t, in, base+"#nbsp-space", tc.raw, true)
			if len(in.Sources) != 2 || in.Sources["source_1"] != tc.raw || in.Sources["source_2"] != tc.quote {
				t.Fatal("original and NBSP-space views must remain separate and exact")
			}
		})
	}
}

func TestBotContentNBSPBoundaries(t *testing.T) {
	const raw = "Yield\u00a0did not rise to 5.107%.\nNext\tline  stays\u200bhere\u202f— \"quoted\"."
	const canonical = "Yield did not rise to 5.107%.\nNext\tline  stays\u200bhere\u202f— \"quoted\"."
	in := capturedContentInput(t, map[string]any{"content": raw, "body": "Separate\u00a0field."})
	const ref = "tool-response:capture#json:/content#nbsp-space"
	checkContentQuote(t, in, ref, canonical, true)
	for _, tc := range []struct{ name, quote string }{
		{"digits", strings.ReplaceAll(canonical, "5.107", "5.109")},
		{"negation", strings.ReplaceAll(canonical, "not ", "")},
		{"newline-join", strings.ReplaceAll(canonical, "\n", " ")},
		{"zero-width", strings.ReplaceAll(canonical, "\u200b", "")},
		{"tab", strings.ReplaceAll(canonical, "\t", " ")},
		{"space-collapse", strings.ReplaceAll(canonical, "  ", " ")},
		{"narrow-nbsp", strings.ReplaceAll(canonical, "\u202f", " ")},
		{"punctuation", strings.ReplaceAll(canonical, "—", "-")},
		{"fuzzy-quotes", strings.ReplaceAll(canonical, "\"quoted\"", "“quoted”")},
		{"field-join", canonical + "Separate field."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkContentQuote(t, in, ref, tc.quote, false)
		})
	}
	t.Run("xml-token-boundaries", func(t *testing.T) {
		in := capturedContentInput(t, map[string]any{"body": `<rss><item>A&#160;&amp; B<em>inner</em> tail</item><item>second</item></rss>`})
		const base = "tool-response:capture#json:/body#xml:/rss[1]/item[1]/text()[1]"
		checkContentQuote(t, in, base, "A\u00a0& B", true)
		checkContentQuote(t, in, base+"#nbsp-space", "A & B", true)
		checkContentQuote(t, in, base+"#nbsp-space", "A & Binner tailsecond", false)
		for id := range in.Sources {
			checkContentQuote(t, in, id, "A & Binner tailsecond", false)
		}
	})
	t.Run("no-nbsp-no-duplicate", func(t *testing.T) {
		in := capturedContentInput(t, map[string]any{"content": "Ordinary spaces\n\t\u200b\u202f &nbsp; \\u00a0", "title": "Metadata\u00a0only"})
		if len(in.Sources) != 1 || in.Origins["source_1"].Path != "#json:/content" {
			t.Fatal("non-NBSP text or metadata produced an additional view")
		}
	})
}

func TestBotContentNBSPProvenanceAndDeterminism(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"body": map[string]any{"b": "same\u00a0text", "a": "same\u00a0text"}, "content": "same\u00a0text"})
	if len(in.Sources) != 6 {
		t.Fatalf("want six separately bound views, got %d", len(in.Sources))
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(in.Candidate.Bodies["tool-response:capture"])))
	for i, path := range []string{"#json:/body/a", "#json:/body/b", "#json:/content"} {
		for j, suffix := range []string{"", "#nbsp-space"} {
			id := fmt.Sprintf("source_%d", i*2+j+1)
			want := botContentSourceOrigin{CaptureRef: "tool-response:capture", Path: path + suffix, BasePath: path, CaptureDigest: digest}
			if in.Origins[id] != want {
				t.Fatalf("incorrect origin for %s: %#v", id, in.Origins[id])
			}
		}
	}
	for i := 0; i < 20; i++ {
		next := botContentInput(in.BotWorkState)
		if next.sourceErr != nil || !reflect.DeepEqual(in.Sources, next.Sources) || !reflect.DeepEqual(in.Origins, next.Origins) {
			t.Fatal("nondeterministic NBSP source views")
		}
	}
}

func TestBotContentNBSPLimits(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		values := make([]string, botContentMaxSourceViews/2)
		for i := range values {
			values[i] = "A\u00a0B"
		}
		in := capturedContentInput(t, map[string]any{"content": values})
		if in.sourceErr != nil || len(in.Sources) != botContentMaxSourceViews {
			t.Fatalf("NBSP views not counted at boundary: count=%d err=%v", len(in.Sources), in.sourceErr)
		}
		in = capturedContentInput(t, map[string]any{"content": append(values, "one more")})
		if !errors.Is(in.sourceErr, errBotContentSourceLimit) {
			t.Fatal("additional NBSP views bypassed count limit")
		}
		if _, err := json.Marshal(in); err == nil {
			t.Fatal("partial view map marshaled after count limit")
		}
		if err := applyBotContentReview(in, &botWorkContentReview{}); err == nil {
			t.Fatal("partial view map accepted after count limit")
		}
	})
	t.Run("bytes", func(t *testing.T) {
		const path, raw, canonical = "#json:/content", "A\u00a0B", "A B"
		cost := len(path) + len(raw) + len(path+"#nbsp-space") + len(canonical)
		for _, spare := range []int{0, -1} {
			in := botWorkContentInput{Sources: map[string]string{}, Origins: map[string]botContentSourceOrigin{}}
			v := botContentViews{in: &in, bytes: botContentSourceByteLimit - cost - spare}
			err := v.jsonStrings(path, raw, 1, false)
			if spare == 0 {
				if err != nil || v.bytes != botContentSourceByteLimit || v.count != 2 {
					t.Fatalf("NBSP view bytes not accounted exactly: bytes=%d count=%d err=%v", v.bytes, v.count, err)
				}
			} else if !errors.Is(err, errBotContentSourceLimit) {
				t.Fatal("NBSP view bypassed byte limit")
			}
		}
	})
}

func TestBotContentAmpersandViews(t *testing.T) {
	const raw = "S\\&P\u00a0did not rise 5.107%.\nC:\\models \\*literal\\* \\&amp;  end\t."
	space := strings.ReplaceAll(raw, "\u00a0", " ")
	amp := strings.ReplaceAll(raw, `\&`, "&")
	both := strings.ReplaceAll(space, `\&`, "&")
	in := capturedContentInput(t, map[string]any{"content": raw, "title": `metadata\&only`})
	want := []string{raw, space, amp, both}
	suffixes := []string{"", "#nbsp-space", "#markdown-ampersand", "#nbsp-space#markdown-ampersand"}
	if len(in.Sources) != len(want) {
		t.Fatalf("want four bounded views, got %d", len(in.Sources))
	}
	for i, text := range want {
		id := fmt.Sprintf("source_%d", i+1)
		if in.Sources[id] != text || in.Origins[id].Path != "#json:/content"+suffixes[i] {
			t.Fatalf("wrong exact view %s: %#v", id, in)
		}
		checkContentQuote(t, in, "source_1", text, true)
	}
	for _, bad := range []string{
		strings.ReplaceAll(both, "5.107", "5.109"), strings.ReplaceAll(both, "not ", ""),
		strings.ReplaceAll(both, `\*`, "*"), strings.ReplaceAll(both, `C:\models`, "C:models"),
		strings.ReplaceAll(both, "\n", " "), strings.ReplaceAll(both, "&amp;", "&"),
	} {
		checkContentQuote(t, in, "source_1", bad, false)
	}
	for i := 0; i < 20; i++ {
		next := botContentInput(in.BotWorkState)
		if !reflect.DeepEqual(in.Sources, next.Sources) || !reflect.DeepEqual(in.Origins, next.Origins) {
			t.Fatal("unstable view order/origins")
		}
	}
	plain := capturedContentInput(t, map[string]any{"content": `S&P \*unchanged\*`})
	if len(plain.Sources) != 1 {
		t.Fatal("duplicate view without escaped ampersand")
	}
}

func TestBotContentAmpersandLimits(t *testing.T) {
	const path = "#json:/content"
	const raw = "S\\&P\u00a0index"
	texts := []string{raw, "S\\&P index", "S&P\u00a0index", "S&P index"}
	suffixes := []string{"", "#nbsp-space", "#markdown-ampersand", "#nbsp-space#markdown-ampersand"}
	cost := 0
	for i, s := range texts {
		cost += len(path+suffixes[i]) + len(s)
	}
	for _, spare := range []int{0, -1} {
		in := botWorkContentInput{Sources: map[string]string{}, Origins: map[string]botContentSourceOrigin{}}
		v := botContentViews{in: &in, bytes: botContentSourceByteLimit - cost - spare}
		err := v.addText(path, raw)
		if spare == 0 {
			if err != nil || v.bytes != botContentSourceByteLimit || v.count != 4 {
				t.Fatalf("wrong accounting: %#v %v", v, err)
			}
		} else if !errors.Is(err, errBotContentSourceLimit) {
			t.Fatal("variant bypassed byte limit")
		}
	}
	values := make([]string, botContentMaxSourceViews/4)
	for i := range values {
		values[i] = raw
	}
	in := capturedContentInput(t, map[string]any{"content": values})
	if in.sourceErr != nil || len(in.Sources) != botContentMaxSourceViews {
		t.Fatal("wrong count boundary")
	}
	in = capturedContentInput(t, map[string]any{"content": append(values, raw)})
	if !errors.Is(in.sourceErr, errBotContentSourceLimit) {
		t.Fatal("variant bypassed count limit")
	}
	if _, err := json.Marshal(in); err == nil {
		t.Fatal("partial source map supplied")
	}
}

func TestBotContentDecodedXML(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, path, quote string
		xml                                  bool
	}{
		{"atom", `<feed xmlns="http://www.w3.org/2005/Atom"><entry>A &amp; B</entry></feed>`, "", "/{http:~1~1www.w3.org~12005~1Atom}feed[1]/{http:~1~1www.w3.org~12005~1Atom}entry[1]/text()[1]", "A & B", true},
		{"xml-looking-prose", `Report: <rss><item>A &amp; B</item></rss>`, "", "", "", false},
		{"foreign-feed", `<feed xmlns="urn:unrelated"><entry>A &amp; B</entry></feed>`, "", "", "", false},
		{"explicit-truncated", `<document><item>A &amp; B</item>`, "application/xml", "", "", false},
		{"rss-entities", `<rss><item>Lab &amp; &quot;AI&quot; &#65; &#x42;.</item><item>Separate.</item></rss>`, "", "/rss[1]/item[1]/text()[1]", `Lab & "AI" A B.`, true},
		{"cdata-once", `<rss><item><![CDATA[Literal &amp;amp;.]]></item></rss>`, "", "/rss[1]/item[1]/text()[1]", `Literal &amp;amp;.`, true},
		{"entity-once", `<rss><item>Literal &amp;amp;.</item></rss>`, "", "/rss[1]/item[1]/text()[1]", `Literal &amp;.`, true},
		{"explicit-type", `<document><item>A &amp; B</item></document>`, "application/xml; charset=utf-8", "/document[1]/item[1]/text()[1]", "A & B", true},
		{"arbitrary-markup", `<document><item>A &amp; B</item></document>`, "", "", "", false},
		{"truncated", `<rss><item>A &amp; B</item>`, "", "", "", false},
		{"malformed", `<rss><item>A &amp; B</rss>`, "", "", "", false},
		{"multiple-roots", `<rss><item>A</item></rss><rss/>`, "", "", "", false},
		{"external-entity", `<!DOCTYPE rss [<!ENTITY remote SYSTEM "http://127.0.0.1/never">]><rss><item>&remote;</item></rss>`, "", "", "", false},
		{"html-entity", `<rss><item>&nbsp;</item></rss>`, "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := capturedContentInput(t, map[string]any{"body": tc.body, "content_type": tc.contentType})
			checkContentQuote(t, in, "tool-response:capture#json:/body", tc.body, true)
			found := false
			for _, origin := range in.Origins {
				if strings.Contains(origin.Path, "#xml:") {
					found = true
				}
			}
			if found != tc.xml {
				t.Fatalf("XML views present=%v want=%v: %#v", found, tc.xml, in.Sources)
			}
			if tc.xml {
				ref := "tool-response:capture#json:/body#xml:" + tc.path
				checkContentQuote(t, in, ref, tc.quote, true)
				checkContentQuote(t, in, ref, tc.quote+" invented 90 million users", false)
				if tc.name == "entity-once" || tc.name == "cdata-once" {
					checkContentQuote(t, in, ref, "Literal &.", false)
				}
			}
		})
	}
}
