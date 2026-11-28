package api

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

const (
	botContentMaxSourceViews  = 1024
	botContentMaxSourceDepth  = 64
	botContentSourceByteLimit = store.BotWorkMaxCandidateBytes + store.BotWorkMaxContractBytes
)

var errBotContentSourceLimit = errors.New("complete assessment source views exceed limit")

// Reject incomplete presentation before schema/model invocation, without changing
// botContentInput's callers or the citation correction's immutable-input protocol.
func (in botWorkContentInput) MarshalJSON() ([]byte, error) {
	if in.sourceErr != nil {
		return nil, in.sourceErr
	}
	type input botWorkContentInput
	return json.Marshal(input(in))
}

// Origins are private assessment data, never durable/public evidence IDs. A
// short ID is bound to one exact captured object and decoding path, not a URL or
// observation ordinal. Raw capture and candidate digests are never rewritten.
type botContentSourceOrigin struct {
	CaptureRef    string `json:"capture_ref"`
	Path          string `json:"path"`
	BasePath      string `json:"base_path"` // Exact field/token, never inferred by stripping path suffixes.
	CaptureDigest string `json:"capture_sha256"`
}

type botContentViews struct {
	in                        *botWorkContentInput
	captureRef, captureDigest string
	count, bytes              int
}

func (v *botContentViews) add(basePath, path, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if v.count >= botContentMaxSourceViews || len(path)+len(text) > botContentSourceByteLimit-v.bytes {
		return errBotContentSourceLimit
	}
	v.count++
	ref := fmt.Sprintf("source_%d", v.count)
	v.in.Sources[ref] = text
	v.in.Origins[ref] = botContentSourceOrigin{CaptureRef: v.captureRef, Path: path, BasePath: basePath, CaptureDigest: v.captureDigest}
	v.bytes += len(path) + len(text)
	return nil
}

const (
	botContentNBSPSuffix      = "#nbsp-space"
	botContentAmpersandSuffix = "#markdown-ampersand"
)

// Preserve the original and supply only the two observed presentation variants.
// Order is raw, NBSP, ampersand, combined. Every view uses bounded add; no generic
// Markdown rendering, whitespace folding, or recursive unescaping occurs.
func (v *botContentViews) addText(path, text string) error {
	if err := v.add(path, path, text); err != nil {
		return err
	}
	space := strings.ReplaceAll(text, "\u00a0", " ")
	if space != text {
		if err := v.add(path, path+botContentNBSPSuffix, space); err != nil {
			return err
		}
	}
	amp := strings.ReplaceAll(text, `\&`, "&")
	if amp != text {
		if err := v.add(path, path+botContentAmpersandSuffix, amp); err != nil {
			return err
		}
		if space != text {
			return v.add(path, path+botContentNBSPSuffix+botContentAmpersandSuffix, strings.ReplaceAll(space, `\&`, "&"))
		}
	}
	return nil
}

// Rebind only to a previously supplied exact view of this very field/token.
// Explicit base paths avoid aliasing a literal JSON key ending in a view suffix.
// Lexically ordered server IDs break ties deterministically; quote bytes stay intact.
func botContentAlternateRef(in botWorkContentInput, ref, quote string) string {
	origin, ok := in.Origins[ref]
	if !ok || origin.CaptureRef == "" || origin.CaptureDigest == "" || origin.BasePath == "" || strings.TrimSpace(quote) == "" {
		return ""
	}
	best := ""
	for id, body := range in.Sources {
		other, ok := in.Origins[id]
		if ok && other.CaptureRef == origin.CaptureRef && other.CaptureDigest == origin.CaptureDigest && other.BasePath == origin.BasePath && strings.Contains(body, quote) && (best == "" || id < best) {
			best = id
		}
	}
	return best
}

// Only known content-bearing fields of a captured FunctionResponse envelope
// qualify. Never traverse metadata looking for plausible prose or infer an
// observation-to-body relationship. Unknown/legacy envelopes remain captured,
// but supply no content evidence until a known content field exists.
func botContentSourceViews(in *botWorkContentInput, bodies map[string]string) error {
	keys := make([]string, 0, len(bodies))
	for ref := range bodies {
		if strings.HasPrefix(ref, "tool-response:") {
			keys = append(keys, ref)
		}
	}
	sort.Strings(keys)
	views := botContentViews{in: in}
	for _, ref := range keys {
		raw := bodies[ref]
		if len(raw) > store.BotWorkMaxCandidateBytes || len(raw)+len(ref) > botContentSourceByteLimit-views.bytes {
			return errBotContentSourceLimit
		}
		views.bytes += len(raw) + len(ref)
		views.captureRef, views.captureDigest = ref, fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		var envelope map[string]any
		if json.Unmarshal([]byte(raw), &envelope) != nil || envelope == nil {
			continue
		}
		// Retain transport fields separately for reviewing own actions; neither
		// these nor observation refs appear in the content source enum.
		metadata := make(map[string]any)
		for key, value := range envelope {
			if key != "content" && key != "body" {
				metadata[key] = value
			}
		}
		if len(metadata) != 0 {
			encoded, _ := json.Marshal(metadata)
			in.ExecutionEvidence[ref] = string(encoded)
		}
		for _, key := range []string{"body", "content"} {
			if value, ok := envelope[key]; ok {
				if err := views.jsonStrings("#json:/"+key, value, 1, botContentXMLType(envelope)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func botContentPointerSegment(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func botContentXMLType(object map[string]any) bool {
	contentType, _ := object["content_type"].(string)
	if contentType == "" {
		if headers, ok := object["headers"].(map[string]any); ok {
			// Header casing must not make source selection map-iteration dependent.
			for _, key := range []string{"Content-Type", "content-type", "Content-type"} {
				if text, ok := headers[key].(string); ok {
					contentType = text
					break
				}
			}
		}
	}
	media, _, err := mime.ParseMediaType(contentType)
	return err == nil && (media == "application/xml" || media == "text/xml" || strings.HasSuffix(media, "+xml"))
}

func (v *botContentViews) jsonStrings(path string, value any, depth int, xmlBody bool) error {
	if depth > botContentMaxSourceDepth {
		return errBotContentSourceLimit
	}
	switch x := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := v.jsonStrings(path+"/"+botContentPointerSegment(key), x[key], depth+1, false); err != nil {
				return err
			}
		}
	case []any:
		for i, value := range x {
			if err := v.jsonStrings(fmt.Sprintf("%s/%d", path, i), value, depth+1, xmlBody); err != nil {
				return err
			}
		}
	case string:
		if err := v.addText(path, x); err != nil {
			return err
		}
		nodes, err := botContentXMLNodes(x, xmlBody)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if err := v.addText(path+"#xml:"+node.Ref, node.Quote); err != nil {
				return err
			}
		}
	}
	return nil
}

type botContentXMLFrame struct {
	path     string
	children map[xml.Name]int
	texts    int
}

// Token text is decoded exactly once by encoding/xml (CDATA is literal). Text
// tokens, siblings and fields are separate views: no descendant flattening,
// markup stripping, HTML unescape, external entities or network resolution.
// Commit views only after a complete single-root document was parsed strictly.
func botContentXMLNodes(text string, explicit bool) ([]botWorkContentSource, error) {
	if !strings.HasPrefix(strings.TrimSpace(text), "<") {
		return nil, nil
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.Strict = true
	var stack []botContentXMLFrame
	var nodes []botWorkContentSource
	rootSeen := false
	bytes := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !rootSeen || len(stack) != 0 {
				return nil, nil
			}
			return nodes, nil
		}
		if err != nil {
			return nil, nil
		}
		switch token := token.(type) {
		case xml.Directive:
			// Do not interpret DTDs or declarations, even if they contain no references.
			return nil, nil
		case xml.StartElement:
			if len(stack) >= botContentMaxSourceDepth {
				return nil, errBotContentSourceLimit
			}
			ordinal, parent := 1, ""
			if len(stack) == 0 {
				if rootSeen {
					return nil, nil
				}
				rootSeen = true
				rss := token.Name.Local == "rss" && token.Name.Space == ""
				atom := token.Name.Local == "feed" && (token.Name.Space == "" || token.Name.Space == "http://www.w3.org/2005/Atom")
				if !explicit && !rss && !atom {
					return nil, nil
				}
			} else {
				frame := &stack[len(stack)-1]
				frame.children[token.Name]++
				ordinal, parent = frame.children[token.Name], frame.path
			}
			name := token.Name.Local
			if token.Name.Space != "" {
				name = "{" + botContentPointerSegment(token.Name.Space) + "}" + name
			}
			stack = append(stack, botContentXMLFrame{path: fmt.Sprintf("%s/%s[%d]", parent, name, ordinal), children: map[xml.Name]int{}})
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(token)) != "" {
					return nil, nil
				}
				continue
			}
			frame := &stack[len(stack)-1]
			frame.texts++
			if strings.TrimSpace(string(token)) == "" {
				continue
			}
			ref := fmt.Sprintf("%s/text()[%d]", frame.path, frame.texts)
			if len(nodes) >= botContentMaxSourceViews || len(ref)+len(token) > botContentSourceByteLimit-bytes {
				return nil, errBotContentSourceLimit
			}
			bytes += len(ref) + len(token)
			nodes = append(nodes, botWorkContentSource{Ref: ref, Quote: string(token)})
		}
	}
}
