package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const BotPublicReadResource = "network:public-http-read"

// BotRoutineReadPolicy resolves current scoped network stores on EVERY request.
// Public reads are the default. Private networks require exact tenant allow rules;
// deny rules at any tier win. Missing stores grant no private access.
func BotRoutineReadPolicy(s *store.Services) tools.RoutineReadPolicy {
	rules := func(ctx context.Context) ([]store.NetworkPolicyRule, error) {
		if s == nil {
			return nil, store.ErrBotForbidden
		}
		var all []store.NetworkPolicyRule
		for _, repo := range []store.NetworkPolicyStore{s.PlatformNetworkPolicies, s.NetworkPolicies, s.TeamNetworkPolicies} {
			if repo != nil {
				r, e := repo.List(ctx)
				if e != nil {
					return nil, e
				}
				all = append(all, r...)
			}
		}
		return all, nil
	}
	return tools.RoutineReadPolicy{Authorize: func(ctx context.Context, origin string) error {
		u, e := url.Parse(origin)
		if e != nil {
			return e
		}
		r, e := rules(ctx)
		if e != nil {
			return e
		}
		port, _ := strconv.Atoi(u.Port())
		for _, v := range r {
			match, _ := path.Match(v.Host, u.Hostname())
			if (v.Host == u.Hostname() || match) && (v.Port == 0 || int(v.Port) == port) && v.Action == store.NetworkPolicyDeny {
				return store.ErrBotForbidden
			}
		}
		return nil
	}, AllowIP: func(ctx context.Context, address string, ip net.IP) error {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return err
		}
		r, e := rules(ctx)
		if e != nil {
			return e
		}
		private := !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
		allow := !private
		for _, v := range r {
			match, _ := path.Match(v.Host, host)
			if (v.Host == host || v.Host == ip.String() || match) && (v.Port == 0 || int(v.Port) == port) {
				if v.Action == store.NetworkPolicyDeny {
					return store.ErrBotForbidden
				}
				if v.Action == store.NetworkPolicyAllow && v.Host == host && !strings.Contains(v.Host, "*") {
					allow = true
				}
			}
		}
		if !allow {
			return fmt.Errorf("private network denied")
		}
		return nil
	}}
}

// NewBotRoutineReadMandatePolicy is a composition-only resolver. Tenant stores and
// finite ceilings are captured, not obtained from model-supplied hosts or roles.
func NewBotRoutineReadMandatePolicy(s *store.Services, ceiling config.BotMandateConfig) store.BotMandatePolicyResolver {
	return func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		limits := execution.BotMandateLimits(ceiling)
		if ref.Validate() != nil || limits.Validate() != nil || s == nil {
			return store.BotMandatePolicy{}, store.ErrBotForbidden
		}
		resources := []string{"web_fetch", "http_request", BotPublicReadResource}
		for _, repo := range []store.NetworkPolicyStore{s.PlatformNetworkPolicies, s.NetworkPolicies, s.TeamNetworkPolicies} {
			if repo != nil {
				rules, e := repo.List(ctx)
				if e != nil {
					return store.BotMandatePolicy{}, e
				}
				for _, r := range rules {
					if r.Action != store.NetworkPolicyAllow || strings.Contains(r.Host, "*") {
						continue
					}
					for _, scheme := range []string{"http", "https"} {
						port := r.Port
						if port == 0 {
							if scheme == "http" {
								port = 80
							} else {
								port = 443
							}
						}
						origin, e := tools.RoutineReadOrigin(scheme + "://" + net.JoinHostPort(r.Host, strconv.Itoa(int(port))))
						if e == nil {
							resources = append(resources, origin)
						}
					}
				}
			}
		}
		return store.BotMandatePolicy{Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: resources}}, Limits: limits, TTL: 24 * time.Hour}, nil
	}
}
