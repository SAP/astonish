package memory

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultScenarioAutoMergeThreshold = 0.82
	DefaultScenarioReviewThreshold    = 0.55
)

type ScenarioIdentity struct {
	Intent        string   `json:"intent,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	System        string   `json:"system,omitempty"`
	Service       string   `json:"service,omitempty"`
	ResourceType  string   `json:"resource_type,omitempty"`
	Operation     string   `json:"operation,omitempty"`
	Environment   string   `json:"environment,omitempty"`
	Credentials   []string `json:"credentials,omitempty"`
	EndpointHosts []string `json:"endpoint_hosts,omitempty"`
	APIFamilies   []string `json:"api_families,omitempty"`
	HTTPMethods   []string `json:"http_methods,omitempty"`
	URLPaths      []string `json:"url_paths,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	AnchorTerms   []string `json:"anchor_terms,omitempty"`
}

type ScenarioSupersededItem struct {
	Value        string `json:"value"`
	SupersededBy string `json:"superseded_by,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type ScenarioCorpusStats struct {
	CardCount int
	AnchorDF  map[string]int
}

type ScenarioMatchScore struct {
	Score              float64  `json:"score"`
	Decision           string   `json:"decision"`
	PositiveSignals    []string `json:"positive_signals,omitempty"`
	NegativeSignals    []string `json:"negative_signals,omitempty"`
	ExtractionWarnings []string `json:"extraction_warnings,omitempty"`
}

type ScenarioCandidate struct {
	ID    string
	Card  ScenarioCard
	Score ScenarioMatchScore
}

func ExtractScenarioIdentity(card ScenarioCard) ScenarioIdentity {
	identity := card.Identity
	text := scenarioCardIdentityText(card)
	extracted := ExtractScenarioIdentityFromText(text)
	identity = mergeScenarioIdentity(identity, extracted)
	identity.AnchorTerms = appendUniqueMany(identity.AnchorTerms, scenarioAnchorTerms(identity))
	return normalizeScenarioIdentity(identity)
}

func ExtractScenarioIdentityFromText(text string) ScenarioIdentity {
	lower := strings.ToLower(text)
	identity := ScenarioIdentity{}
	if strings.Contains(lower, "openstack") || strings.Contains(lower, "keystone") || strings.Contains(lower, "octavia") || strings.Contains(lower, "lbaas") {
		identity.Domain = "infrastructure"
		identity.System = "openstack"
	}
	if strings.Contains(lower, "kubernikus") {
		identity.Domain = "infrastructure"
		identity.System = "openstack"
		identity.Service = "kubernikus"
		identity.ResourceType = "kubernetes-cluster"
		identity.APIFamilies = appendUnique(identity.APIFamilies, "kubernikus")
	}
	if strings.Contains(lower, "kubernetes") || strings.Contains(lower, "k8s") {
		identity.Domain = firstNonEmpty(identity.Domain, "infrastructure")
		if identity.ResourceType == "" {
			identity.ResourceType = "kubernetes-cluster"
		}
	}
	if hasAny(lower, "octavia", "lbaas", "load balancer", "load-balancer", "loadbalancer", "load balancing", "loadbalancing") {
		identity.Domain = firstNonEmpty(identity.Domain, "infrastructure")
		identity.Service = "openstack-load-balancing"
		identity.ResourceType = "load-balancer"
		identity.APIFamilies = appendUnique(identity.APIFamilies, "openstack-octavia")
		identity.APIFamilies = appendUnique(identity.APIFamilies, "openstack-lbaas")
	}
	if hasAny(lower, "server", "compute", "nova instance", "instance list", "instances") && identity.ResourceType == "" {
		identity.Domain = firstNonEmpty(identity.Domain, "infrastructure")
		identity.Service = "openstack-compute"
		identity.ResourceType = "compute-server"
		identity.APIFamilies = appendUnique(identity.APIFamilies, "openstack-nova")
	}
	identity.Operation = extractScenarioOperation(lower)
	identity.Intent = identity.Operation
	identity.Credentials = appendUniqueMany(identity.Credentials, extractCredentialAnchors(lower))
	identity.EndpointHosts = appendUniqueMany(identity.EndpointHosts, extractEndpointHosts(text))
	identity.HTTPMethods = appendUniqueMany(identity.HTTPMethods, httpMethodPattern.FindAllString(strings.ToUpper(text), -1))
	identity.URLPaths = appendUniqueMany(identity.URLPaths, extractURLPathAnchors(text))
	if env := extractEnvironment(lower); env != "" {
		identity.Environment = env
	}
	return normalizeScenarioIdentity(identity)
}

func BuildScenarioCorpusStats(cards []ScenarioCard) ScenarioCorpusStats {
	stats := ScenarioCorpusStats{CardCount: len(cards), AnchorDF: make(map[string]int)}
	for _, card := range cards {
		anchors := scenarioIdentityAnchors(ExtractScenarioIdentity(card))
		seen := make(map[string]bool)
		for _, anchor := range anchors {
			if anchor == "" || seen[anchor] {
				continue
			}
			seen[anchor] = true
			stats.AnchorDF[anchor]++
		}
	}
	return stats
}

func ScoreScenarioPair(a, b ScenarioCard, stats ScenarioCorpusStats) ScenarioMatchScore {
	a.Identity = ExtractScenarioIdentity(a)
	b.Identity = ExtractScenarioIdentity(b)
	score := 0.0
	var positives []string
	var negatives []string
	if a.CanonicalKey != "" && b.CanonicalKey != "" && strings.EqualFold(a.CanonicalKey, b.CanonicalKey) {
		score += 0.50
		positives = append(positives, "same canonical key alias")
	}
	if aliasOverlap(a, b) {
		score += 0.16
		positives = append(positives, "shared alias or service vocabulary")
	}
	if sameNonEmpty(a.Identity.Environment, b.Identity.Environment) {
		score += rarityWeight("env:"+a.Identity.Environment, stats, 0.16)
		positives = append(positives, "same environment "+a.Identity.Environment)
	} else if a.Identity.Environment != "" && b.Identity.Environment != "" {
		score -= 0.22
		negatives = append(negatives, fmt.Sprintf("different environments %s vs %s", a.Identity.Environment, b.Identity.Environment))
	}
	if sameNonEmpty(a.Identity.ResourceType, b.Identity.ResourceType) {
		score += rarityWeight("resource:"+a.Identity.ResourceType, stats, 0.20)
		positives = append(positives, "same resource type "+a.Identity.ResourceType)
	} else if a.Identity.ResourceType != "" && b.Identity.ResourceType != "" {
		score -= 0.35
		negatives = append(negatives, fmt.Sprintf("different resource types %s vs %s", a.Identity.ResourceType, b.Identity.ResourceType))
	}
	if sameNonEmpty(a.Identity.System, b.Identity.System) {
		score += 0.08
		positives = append(positives, "same system "+a.Identity.System)
	}
	if sameNonEmpty(a.Identity.Service, b.Identity.Service) {
		score += 0.12
		positives = append(positives, "same service family "+a.Identity.Service)
	}
	if sameNonEmpty(a.Identity.Operation, b.Identity.Operation) {
		score += 0.14
		positives = append(positives, "same operation "+a.Identity.Operation)
	} else if a.CanonicalKey != "" && b.CanonicalKey != "" && strings.EqualFold(a.CanonicalKey, b.CanonicalKey) {
		score += 0.20
		positives = append(positives, "same scenario alias without conflicting operation")
	} else if a.Identity.Operation != "" && b.Identity.Operation != "" && incompatibleOperations(a.Identity.Operation, b.Identity.Operation) {
		score -= 0.30
		negatives = append(negatives, fmt.Sprintf("different operations %s vs %s", a.Identity.Operation, b.Identity.Operation))
	}
	if matches := intersect(a.Identity.Credentials, b.Identity.Credentials); len(matches) > 0 {
		score += rarityWeight("credential:"+matches[0], stats, 0.20)
		positives = append(positives, "shared credential "+matches[0])
	}
	if host := matchingEndpointHost(a.Identity.EndpointHosts, b.Identity.EndpointHosts); host != "" {
		score += rarityWeight("host:"+host, stats, 0.22)
		positives = append(positives, "shared endpoint family "+host)
	}
	if api := firstIntersection(a.Identity.APIFamilies, b.Identity.APIFamilies); api != "" {
		score += 0.08
		positives = append(positives, "shared API family "+api)
	}
	if path := firstIntersection(a.Identity.URLPaths, b.Identity.URLPaths); path != "" {
		score += rarityWeight("path:"+path, stats, 0.12)
		positives = append(positives, "shared URL path "+path)
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	decision := "distinct"
	if score >= DefaultScenarioAutoMergeThreshold && len(negatives) == 0 {
		decision = "merge"
	} else if score >= DefaultScenarioReviewThreshold {
		decision = "review"
	}
	return ScenarioMatchScore{Score: score, Decision: decision, PositiveSignals: positives, NegativeSignals: negatives}
}

func FindBestScenarioCandidate(existing []ScenarioCandidate) (ScenarioCandidate, bool) {
	if len(existing) == 0 {
		return ScenarioCandidate{}, false
	}
	sort.SliceStable(existing, func(i, j int) bool {
		return existing[i].Score.Score > existing[j].Score.Score
	})
	best := existing[0]
	if best.Score.Decision != "merge" {
		return ScenarioCandidate{}, false
	}
	if len(existing) > 1 && existing[1].Score.Decision == "merge" && math.Abs(best.Score.Score-existing[1].Score.Score) < 0.03 {
		return ScenarioCandidate{}, false
	}
	return best, true
}

func scenarioCardIdentityText(card ScenarioCard) string {
	parts := []string{card.CanonicalKey, card.Title, card.Category}
	parts = append(parts, card.Aliases...)
	parts = append(parts, card.Facts...)
	parts = append(parts, card.RecommendedRecipe...)
	parts = append(parts, card.Conditions...)
	parts = append(parts, card.CautionsOrConditionalFailures...)
	parts = append(parts, card.Verification...)
	return strings.Join(parts, "\n")
}

func mergeScenarioIdentity(a, b ScenarioIdentity) ScenarioIdentity {
	a.Intent = firstNonEmpty(a.Intent, b.Intent)
	a.Domain = firstNonEmpty(a.Domain, b.Domain)
	a.System = firstNonEmpty(a.System, b.System)
	a.Service = firstNonEmpty(a.Service, b.Service)
	a.ResourceType = firstNonEmpty(a.ResourceType, b.ResourceType)
	a.Operation = firstNonEmpty(a.Operation, b.Operation)
	a.Environment = firstNonEmpty(a.Environment, b.Environment)
	a.Credentials = appendUniqueMany(a.Credentials, b.Credentials)
	a.EndpointHosts = appendUniqueMany(a.EndpointHosts, b.EndpointHosts)
	a.APIFamilies = appendUniqueMany(a.APIFamilies, b.APIFamilies)
	a.HTTPMethods = appendUniqueMany(a.HTTPMethods, b.HTTPMethods)
	a.URLPaths = appendUniqueMany(a.URLPaths, b.URLPaths)
	a.Tools = appendUniqueMany(a.Tools, b.Tools)
	a.AnchorTerms = appendUniqueMany(a.AnchorTerms, b.AnchorTerms)
	return a
}

func normalizeScenarioIdentity(identity ScenarioIdentity) ScenarioIdentity {
	identity.Intent = normalizeAnchor(identity.Intent)
	identity.Domain = normalizeAnchor(identity.Domain)
	identity.System = normalizeAnchor(identity.System)
	identity.Service = normalizeAnchor(identity.Service)
	identity.ResourceType = normalizeAnchor(identity.ResourceType)
	identity.Operation = normalizeAnchor(identity.Operation)
	identity.Environment = normalizeAnchor(identity.Environment)
	identity.Credentials = normalizeUnique(identity.Credentials)
	identity.EndpointHosts = normalizeUnique(identity.EndpointHosts)
	identity.APIFamilies = normalizeUnique(identity.APIFamilies)
	identity.HTTPMethods = normalizeUnique(identity.HTTPMethods)
	identity.URLPaths = normalizeUnique(identity.URLPaths)
	identity.Tools = normalizeUnique(identity.Tools)
	identity.AnchorTerms = normalizeUnique(identity.AnchorTerms)
	return identity
}

func scenarioAnchorTerms(identity ScenarioIdentity) []string {
	var anchors []string
	for _, value := range []string{identity.Domain, identity.System, identity.Service, identity.ResourceType, identity.Operation, identity.Environment} {
		anchors = appendUnique(anchors, value)
	}
	anchors = appendUniqueMany(anchors, identity.Credentials)
	anchors = appendUniqueMany(anchors, identity.EndpointHosts)
	anchors = appendUniqueMany(anchors, identity.APIFamilies)
	anchors = appendUniqueMany(anchors, identity.URLPaths)
	return anchors
}

func scenarioIdentityAnchors(identity ScenarioIdentity) []string {
	var anchors []string
	if identity.Environment != "" {
		anchors = append(anchors, "env:"+identity.Environment)
	}
	if identity.ResourceType != "" {
		anchors = append(anchors, "resource:"+identity.ResourceType)
	}
	for _, credential := range identity.Credentials {
		anchors = append(anchors, "credential:"+credential)
	}
	for _, host := range identity.EndpointHosts {
		anchors = append(anchors, "host:"+host)
	}
	for _, path := range identity.URLPaths {
		anchors = append(anchors, "path:"+path)
	}
	return anchors
}

func normalizeAnchor(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.Join(strings.Fields(value), "-")
	return value
}

func normalizeUnique(values []string) []string {
	var out []string
	for _, value := range values {
		value = normalizeAnchor(value)
		if value != "" {
			out = appendUnique(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func hasAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func extractScenarioOperation(lower string) string {
	switch {
	case regexp.MustCompile(`\b(list|lookup|find|get|fetch|inspect|show|query|read)\b`).MatchString(lower):
		return "list"
	case regexp.MustCompile(`\b(create|provision|add|build)\b`).MatchString(lower):
		return "create"
	case regexp.MustCompile(`\b(delete|remove|destroy)\b`).MatchString(lower):
		return "delete"
	case regexp.MustCompile(`\b(update|patch|modify|change|rotate)\b`).MatchString(lower):
		return "update"
	}
	return ""
}

func extractEnvironment(lower string) string {
	matches := environmentPattern.FindAllString(lower, -1)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func extractCredentialAnchors(lower string) []string {
	var credentials []string
	for _, value := range credentialPattern.FindAllString(lower, -1) {
		value = strings.TrimSpace(value)
		if ignoredCredentialAnchor(value) {
			continue
		}
		credentials = appendUnique(credentials, value)
	}
	return credentials
}

func ignoredCredentialAnchor(value string) bool {
	switch normalizeAnchor(value) {
	case "resolve-credential", "x-auth-token", "auth-token", "keystone-token", "credential", "credentials":
		return true
	}
	return false
}

func extractEndpointHosts(text string) []string {
	var hosts []string
	for _, raw := range urlPattern.FindAllString(text, -1) {
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Hostname() != "" {
			hosts = appendUnique(hosts, parsed.Hostname())
		}
	}
	for _, host := range endpointHostPattern.FindAllString(strings.ToLower(text), -1) {
		if strings.Contains(host, "astonish") {
			continue
		}
		hosts = appendUnique(hosts, host)
	}
	return hosts
}

func extractURLPathAnchors(text string) []string {
	var paths []string
	urlRanges := urlMatchRanges(text)
	for _, raw := range urlPattern.FindAllString(text, -1) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Path == "" || ignoredURLPathAnchor(parsed.Path) {
			continue
		}
		paths = appendUnique(paths, parsed.Path)
	}
	for _, match := range urlPathPattern.FindAllStringIndex(text, -1) {
		if matchInsideAnyRange(match, urlRanges) {
			continue
		}
		path := text[match[0]:match[1]]
		if strings.HasPrefix(path, "//") || ignoredURLPathAnchor(path) {
			continue
		}
		paths = appendUnique(paths, path)
	}
	return paths
}

func urlMatchRanges(text string) [][2]int {
	matches := urlPattern.FindAllStringIndex(text, -1)
	ranges := make([][2]int, 0, len(matches))
	for _, match := range matches {
		ranges = append(ranges, [2]int{match[0], match[1]})
	}
	return ranges
}

func matchInsideAnyRange(match []int, ranges [][2]int) bool {
	for _, r := range ranges {
		if match[0] >= r[0] && match[1] <= r[1] {
			return true
		}
	}
	return false
}

func ignoredURLPathAnchor(path string) bool {
	path = normalizeAnchor(strings.TrimSpace(path))
	switch path {
	case "", "/", "/efficient-successful-path", "/password":
		return true
	}
	return strings.Contains(path, "astonish") || strings.Contains(path, "password")
}

func aliasOverlap(a, b ScenarioCard) bool {
	left := scenarioAliasSet(a)
	for alias := range scenarioAliasSet(b) {
		if left[alias] {
			return true
		}
	}
	return false
}

func scenarioAliasSet(card ScenarioCard) map[string]bool {
	aliases := make(map[string]bool)
	for _, value := range append([]string{card.CanonicalKey, card.Title, card.Identity.Service, card.Identity.ResourceType}, card.Aliases...) {
		for _, alias := range normalizeScenarioAliases(value) {
			aliases[alias] = true
		}
	}
	return aliases
}

func normalizeScenarioAliases(value string) []string {
	lower := strings.ToLower(value)
	var aliases []string
	if hasAny(lower, "octavia", "lbaas", "loadbalancer", "load-balancer", "load balancing", "loadbalancing", "load balancer") {
		aliases = append(aliases, "openstack-load-balancing", "load-balancer")
	}
	for _, token := range regexp.MustCompile(`[a-z0-9]+`).FindAllString(lower, -1) {
		if len(token) > 2 {
			aliases = appendUnique(aliases, token)
		}
	}
	return aliases
}

func sameNonEmpty(a, b string) bool { return a != "" && strings.EqualFold(a, b) }

func incompatibleOperations(a, b string) bool {
	if a == b || a == "" || b == "" {
		return false
	}
	writeOps := map[string]bool{"create": true, "delete": true, "update": true}
	return writeOps[a] || writeOps[b]
}

func intersect(a, b []string) []string {
	left := make(map[string]bool, len(a))
	for _, value := range a {
		left[value] = true
	}
	var out []string
	for _, value := range b {
		if left[value] {
			out = append(out, value)
		}
	}
	return out
}

func firstIntersection(a, b []string) string {
	matches := intersect(a, b)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func matchingEndpointHost(a, b []string) string {
	for _, left := range a {
		for _, right := range b {
			if left == right {
				return left
			}
			if hostFamily(left) != "" && hostFamily(left) == hostFamily(right) {
				return hostFamily(left)
			}
		}
	}
	return ""
}

func hostFamily(host string) string {
	host = strings.ToLower(host)
	for _, prefix := range []string{"loadbalancer", "loadbalancing", "octavia", "lbaas"} {
		if strings.HasPrefix(host, prefix) {
			parts := strings.Split(host, ".")
			if len(parts) > 1 {
				return "openstack-load-balancing." + strings.Join(parts[1:], ".")
			}
			return "openstack-load-balancing"
		}
	}
	return host
}

func rarityWeight(anchor string, stats ScenarioCorpusStats, base float64) float64 {
	if stats.CardCount <= 1 || stats.AnchorDF == nil {
		return base
	}
	df := stats.AnchorDF[anchor]
	if df <= 1 {
		return base * 1.25
	}
	if df >= stats.CardCount/2 && stats.CardCount > 3 {
		return base * 0.65
	}
	return base
}

var (
	credentialPattern   = regexp.MustCompile(`\b[a-z0-9]+(?:-[a-z0-9]+)*(?:-keystone|-token|-credential|_credential)\b`)
	endpointHostPattern = regexp.MustCompile(`\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9][a-z0-9-]*){2,}\b`)
	environmentPattern  = regexp.MustCompile(`\b(?:qa|dev|prod|stage|staging|test|sandbox|eu|us|ap)-[a-z]{2,}-?\d*\b`)
	httpMethodPattern   = regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE|HEAD)\b`)
	urlPattern          = regexp.MustCompile(`https?://[^\s)\]}>"']+`)
	urlPathPattern      = regexp.MustCompile(`/[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)
)
