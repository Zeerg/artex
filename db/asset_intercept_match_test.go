package db

import "testing"

func rule(kind, pattern string, enabled bool) AssetInterceptRule {
	return AssetInterceptRule{Kind: kind, Pattern: pattern, Enabled: enabled}
}

func TestMatchAssetInterceptRules(t *testing.T) {
	cases:= []struct {
		name string
		rules []AssetInterceptRule
		domains []string
		ips []string
		urls []string
		want bool
		wantVal string
	}{
		{"Built-in fuzzy government domain hit", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
			[]string{"www.beijing.gov.cn"}, nil, nil, true, "www.beijing.gov.cn"},
		{"Fuzzy education domain hit", []AssetInterceptRule{rule("fuzzy_domain", ".edu", true)},
			[]string{"mit.edu"}, nil, nil, true, "mit.edu"},
		{"Full domain name hits are case-insensitive.", []AssetInterceptRule{rule("exact_domain", "Example.com", true)},
			[]string{"example.com"}, nil, nil, true, "example.com"},
		{"Full domain names not matching subdomains", []AssetInterceptRule{rule("exact_domain", "example.com", true)},
			[]string{"a.example.com"}, nil, nil, false, ""},
		{"Full IP hit", []AssetInterceptRule{rule("exact_ip", "203.0.113.5", true)},
			nil, []string{"203.0.113.5"}, nil, true, "203.0.113.5"},
		{"Fuzzy IP prefix hit", []AssetInterceptRule{rule("fuzzy_ip", "203.0.113.", true)},
			nil, []string{"203.0.113.99"}, nil, true, "203.0.113.99"},
		{"CIDR Hit", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"192.168.5.20"}, nil, true, "192.168.5.20"},
		{"CIDR Misses", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"10.0.0.1"}, nil, false, ""},
		{"Full URL Match", []AssetInterceptRule{rule("exact_url", "https://a.gov.cn/login", true)},
			nil, nil, []string{"https://a.gov.cn/login"}, true, "https://a.gov.cn/login"},
		{"Fuzzy URL hits path.", []AssetInterceptRule{rule("fuzzy_url", "/admin", true)},
			nil, nil, []string{"https://x.com/admin/panel"}, true, "https://x.com/admin/panel"},
		{"Disable rules not hit.", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", false)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
		{"No rule matched", nil, []string{"www.gov.cn"}, nil, nil, false, ""},
		{"Empty pattern does not match.", []AssetInterceptRule{rule("fuzzy_domain", "  ", true)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
	}
	for _, c:= range cases {
		t.Run(c.name, func(t *testing.T) {
			r, val, ok:= MatchAssetInterceptRules(c.rules, c.domains, c.ips, c.urls)
			if ok!= c.want {
				t.Fatalf("Hit = %v, Expectation %v (rule=%+v)", ok, c.want, r)
			}
			if ok && val!= c.wantVal {
				t.Fatalf("Match value = %q, Expectation %q", val, c.wantVal)
			}
		})
	}
}

func TestEvaluateAssetGate(t *testing.T) {
	block:= []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}
	allow:= []AssetInterceptRule{rule("fuzzy_domain", "example.com", true)}

	// 1. Intercept rule hit → Refused (Interception reason takes precedence).
	if d:= EvaluateAssetGate(block, allow, []string{"www.gov.cn"}, nil, nil); d.Allowed {
		t.Fatal("Intercept rules should be rejected")
	}

	// 2. Not intercepted, has allow rule but not matched → Refuse (not allowed).
	d:= EvaluateAssetGate(block, allow, []string{"foo.other.com"}, nil, nil)
	if d.Allowed {
		t.Fatal("If there is a whitelist and it does not match, it should be rejected.")
	}
	if d.Reason == "" {
		t.Fatal("Rejection reason required")
	}

	// 3. Not intercepted, intercepted by allow rule → Allow.
	if d:= EvaluateAssetGate(block, allow, []string{"api.example.com"}, nil, nil);!d.Allowed {
		t.Fatal("Allow through if it hits the whitelist")
	}

	// 4. No allowed rules (whitelist not enabled)→ Allowed if not intercepted.
	if d:= EvaluateAssetGate(block, nil, []string{"foo.other.com"}, nil, nil);!d.Allowed {
		t.Fatal("Allow passage when not on the whitelist and no intercept is hit")
	}

	// 5. Disable all allow rules → Considered as whitelist not enabled, allowing.
	disabledAllow:= []AssetInterceptRule{rule("fuzzy_domain", "example.com", false)}
	if d:= EvaluateAssetGate(nil, disabledAllow, []string{"foo.other.com"}, nil, nil);!d.Allowed {
		t.Fatal("When the whitelist is fully disabled, allow it through")
	}

	// 6. Interception takes precedence over allowance: Same target hits both interception and allowance → Deny.
	if d:= EvaluateAssetGate(
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]string{"www.gov.cn"}, nil, nil,
	); d.Allowed {
		t.Fatal("Interception should take precedence over allowing")
	}
}

func TestAssetInterceptCandidates(t *testing.T) {
	// Services with URL-only assets: host should be split and categorized under domain candidates, thus being matched by fuzzy_domain.
	a:= &Asset{Type: "service", URL: "https://portal.beijing.gov.cn:8443/app"}
	domains, _, urls:= a.interceptCandidates()
	if len(urls)!= 1 || urls[0]!= a.URL {
		t.Fatalf("urls = %v", urls)
	}
	found:= false
	for _, d:= range domains {
		if d == "portal.beijing.gov.cn" {
			found = true
		}
	}
	if!found {
		t.Fatalf("URL host Domain candidates have not been split: %v", domains)
	}
	r, _, ok:= MatchAssetInterceptRules([]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}, domains, nil, urls)
	if!ok {
		t.Fatalf("Government service assets with only URLs should be matched by fuzzy_domain, rule=%+v", r)
	}

	// URL host When it's an IP, it should be categorized as an IP candidate and can be hit by CIDR.
	b:= &Asset{Type: "service", URL: "http://10.1.2.3/x"}
	_, ips, _:= b.interceptCandidates()
	if r, _, ok:= MatchAssetInterceptRules([]AssetInterceptRule{rule("cidr", "10.0.0.0/8", true)}, nil, ips, nil);!ok {
		t.Fatalf("URL The IPs should be matched by CIDR., ips=%v rule=%+v", ips, r)
	}
}
