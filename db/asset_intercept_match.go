package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// The layer for matching and executing asset interception rules. asset_intercept.go Responsible for rule storage, here it handles storing
// 'The domain/IP/URL of the target asset matches the enabled rules. Used by agent tools (add_intent,
// insert_assets)Called before issuing intent / inserting assets, reject if hit.

// AssetInterceptKindLabel Return the Chinese label of kind for use in agent's explanation messages.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "Domain(Equal)"
	case "exact_ip":
		return "IP(Equal)"
	case "exact_url":
		return "URL(Equal)"
	case "fuzzy_domain":
		return "Domain(fuzzy)"
	case "fuzzy_ip":
		return "IP(fuzzy)"
	case "fuzzy_url":
		return "URL(fuzzy)"
	case "cidr":
		return "CIDR Cancel"
	}
	return kind
}

// Reason Return a readable reason for the hit, e.g.: Hit asset interception rule. [Domain(fuzzy): .gov.cn] (Remarks).
func (r AssetInterceptRule) Reason() string {
	s:= fmt.Sprintf("Asset interception rules hit [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note:= strings.TrimSpace(r.Note); note!= "" {
		s += " (" + note + ")"
	}
	return s
}

// matchOne Determine if a single rule enabling condition hits the given domain/IP/URL candidate string, returning the specific value that was hit.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p:= strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d:= range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip:= range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u:= range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp:= strings.ToLower(p)
		for _, d:= range domains {
			if d!= "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip:= range ips {
			if ip!= "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp:= strings.ToLower(p)
		for _, u:= range urls {
			if u!= "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err:= net.ParseCIDR(p)
		if err!= nil {
			return "", false
		}
		for _, ip:= range ips {
			if pip:= net.ParseIP(strings.TrimSpace(ip)); pip!= nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules Return the enabled rule and matched specific value of the first hit given a domain/IP/URL candidate string.
// For insert_assets to use original input (unwritten assetInputItem) to match.
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r:= range rules {
		if!r.Enabled {
			continue
		}
		if v, ok:= matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates Extract an already stored asset for domain/IP/URL candidate matching.
// URL The host will be split out and categorized, allowing service assets that only carry URLs to be hit by domain/IP rules.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add:= func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s!= "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d:= range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL!= "" {
		if u, err:= url.Parse(a.URL); err == nil {
			if h:= u.Hostname(); h!= "" {
				if net.ParseIP(h)!= nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel Return a short identifier for assets, used for agent's explanation messages.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain!= "":
		target = a.Domain
	case a.URL!= "":
		target = a.URL
	case a.IP!= "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("Asset#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule Checks if any enabled rule exists in the rule set.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r:= range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision It is the result of the 'intercept then allow' gate for a set of candidate strings.
type AssetGateDecision struct {
	Allowed bool
	Reason string // Rejection reason (excluding asset identifier); Allowed=true Is Empty
}

// EvaluateAssetGate Execute task-level gate determination:
// 1. Hits any enabled blockRules → Rejected (interception reason).
// 2. Otherwise, if allowRules has enabled items that don't match, → Refused (Outside allowed range).
// 3. Otherwise, proceed.
//
// allowRules Allow gate to be ineffective when empty/no enabled items (i.e., do not enable whitelist, allow all),
// Avoid 'unconfigured allowed rules' blocking all assets.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok:= MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok:= MatchAssetInterceptRules(allowRules, domains, ips, urls);!ok {
			return AssetGateDecision{Allowed: false, Reason: "Not allowed in the task.(Whitelist)Not allowed to test within the scope"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit Describe an asset that was blocked by the gate (interception hit or out of allowed range).
type AssetInterceptHit struct {
	Asset *Asset
	Reason string // Readable reason
}

// Describe Return a readable explanation: asset information, + Reason.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules is *DB Transparent method forwarding, allowing only AssetStore holders
// (Even agent tools can read the rules.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept Load assets by id, execute 'intercept then allow' gate determination one by one, return all
// Assets rejected. Interception rules. = global ∪ Task-level block; allow rules = Task-level allow (only for this task).
// Return quickly when no id. Use global GetByIDs (unfiltered by task scope) to ensure interception is not weakened by scope reduction.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err:= s.db.ListAssetInterceptRules()
	if err!= nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err:= s.TaskInterceptRulesSplit(taskID)
		if err!= nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// No interception rules, no enabled allow rules → No need to determine, all pass.
	if len(blockRules) == 0 &&!hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err:= s.GetByIDs(ids)
	if err!= nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a:= range assets {
		domains, ips, urls:= a.interceptCandidates()
		if d:= EvaluateAssetGate(blockRules, allowRules, domains, ips, urls);!d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
