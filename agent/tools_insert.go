package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Zeerg/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// assetInterceptCandidates Extract a candidate string for domain/IP/URL from an asset input item to be used for asset interception matching.
// URL The host will be categorized to allow 'URL-only' services/endpoints assets to be matched by domain/IP rules.
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add:= func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s!= "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d:= range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL!= "" {
		if u, err:= url.Parse(item.URL); err == nil {
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

// assetInputLabel Return a short identifier for an asset to be inserted, used in interception explanation messages.
func assetInputLabel(item assetInputItem) string {
	typ:= strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain)!= "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL)!= "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP)!= "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP)!= "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(unknown)"
	}
	if typ!= "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain string `json:"domain"`
	ICP string `json:"icp"`
	RecordType string `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP string `json:"ip"`
	BoundDomains []string `json:"bound_domains"`
	OpenPorts []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName string `json:"app_name"`
	BundleID string `json:"bundle_id"`
	Category string `json:"category"`
	Description string `json:"description"`
	AppICP string `json:"app_icp"`
	CompanyID *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL string `json:"url"`
	Technologies []string `json:"technologies"`
	StatusCode *int `json:"status_code"`
	ContentLength *int64 `json:"content_length"`
	PageTitle string `json:"page_title"`
	FaviconMMH3 string `json:"favicon_mmh3"`
	Auth []map[string]any `json:"auth"`
	ServiceName string `json:"service_name"`
	ServiceIP string `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port int `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"Batch registration of newly discovered assets, can mix multiple types (type see enum).\n"+
			"Required fields for each type: root_domain→domain; ip→ip (Must be IPv4/IPv6, not a hostname); subdomain→domain; app→app_name; service(HTTP)→url; service(Non-HTTP)→service_name+port (ip/domain At least one); endpoint→url+method. Other field meanings see individual descriptions.\n"+
			"auth/technologies/params For appending merge(append), Do not overwrite original value.\n"+
			"Return:{results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id Do not expose to the model: workerwhich task it belongs to is authoritatively assigned by the program via SetTaskID(See handler).
			"assets": map[string]any{
				"type": "array",
				"description": "Asset array, each element corresponds to an asset record.",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type": "string",
						"enum": []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "Asset Type,",
					},
					// root_domain / subdomain
					"domain": str("Root domain or subdomain (root_domain/subdomain required)"),
					"icp": str("ICP Record Filing Number (Optional)"),
					"record_type": str("DNS Parsing types: A/AAAA/CNAME/MX etc. (subdomain optional)"),
					"record_value": map[string]any{
						"type": "array",
						"items": map[string]any{"type": "string"},
						"description": "DNS Parse value list (subdomain is optional, e.g., [\"1.2.3.4\",\"2.3.4.5\"])",
					},
					// ip
					"ip": str("IP The address must be an IPv4/IPv6 address; do not enter a hostname (use type=subdomain domain field); IP type required; service/endpoint type optional for associating with IP."),
					"bound_domains": map[string]any{
						"type": "array",
						"items": map[string]any{"type": "string"},
						"description": "List of domains bound to this IP (ip type optional)",
					},
					"open_ports": map[string]any{
						"type": "array",
						"description": "Open port list (IP type is optional).",
						"items": obj(map[string]any{
							"port": intp("Port number"),
							"service": str("Service name, such as http/ssh/mysql, etc. (optional)"),
						}, "port"),
					},
					// app
					"app_name": str("Application name (app type required),"),
					"bundle_id": str("Bundle ID (app Type optional)"),
					"category": str("Application category (optional)"),
					"description": str("Application description (optional)"),
					"app_icp": str("Apply ICP filing (optional)"),
					"company_id": intp("Ownership enterprise id (app type is optional; app cannot be automatically attributed to scope, must be explicitly specified. ID returned by add_company_scope)"),
					// service (http)
					"url": str("Full URL, including protocol and port (HTTP services must be filled; service_type is automatically set to http)"),
					"status_code": intp("HTTP Response status code, such as 200/301/403/404 (optional)"),
					"content_length": map[string]any{
						"type": "integer",
						"description": "HTTP Response body byte count (optional)",
					},
					"page_title": str("Page <title> Content (optional)"),
					"favicon_mmh3": str("favicon MMH3 Hash (optional)"),
					"technologies": map[string]any{
						"type": "array",
						"items": map[string]any{"type": "string"},
						"description": "Fingerprint/Technology stack list, such as: [\"Nginx\",\"Vue\",\"Bootstrap\"] (Optional)",
					},
					"auth": map[string]any{
						"type": "array",
						"description": "A list of discovered authentication information, each containing type/username/password fields (optional, appended without overwriting).",
						"items": map[string]any{"type": "object"},
					},
					// service (other, Non-HTTP)
					"service_name": str("Service name, like ssh/mysql/redis (service non-HTTP requires filling)."),
					"port": intp("Port number (required when service is not HTTP)."),
					// endpoint
					"method": str("HTTP Method: GET/POST/PUT/PATCH/DELETE etc. (endpoint is required)"),
					"params": map[string]any{
						"type": "array",
						"description": "List of request parameters, each containing location.(query/body/header/path)/name/value/type (Optional, append without overwriting)",
						"items": map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets Not enabled: AssetStore Not initialized"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err:= json.Unmarshal(in, &a); err!= nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id Assigned by program authority.(worker: SetTaskID), Do not accept model input——Avoid modelmissed/mis-delivered
			// Causing assets to be assigned to the wrong task. Calls without task context(auto/pentest/chat)Its t.taskID=0.
			taskID:= t.taskID

			type result struct {
				Index int `json:"index"`
				ID int64 `json:"id"`
				Type string `json:"type"`
			}
			type errEntry struct {
				Index int `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// Load asset gate rules once; skip judgment if read fails (do not block insertion).
			// Intercept Rules, = global ∪ Task-level block; allow rules = Task-level allow.
			blockRules, _:= t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err:= t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item:= range a.Assets {
				// Asset gate: Intercept first, then allow. Assets rejected are prohibited from being inserted (skip Upsert and subsequent side effects).
				domains, ips, urls:= assetInterceptCandidates(item)
				if d:= db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls);!d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("Asset %s %s, insertion has been prohibited", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ:= strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP: item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP: item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts: item.OpenPorts,
						TaskID: taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain: item.Domain,
						RecordType: item.RecordType,
						RecordValue: item.RecordValue,
						ICP: item.ICP,
						TaskID: taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name: item.AppName,
						BundleID: item.BundleID,
						Category: item.Category,
						Description: item.Description,
						ICP: item.AppICP,
						CompanyID: item.CompanyID,
						TaskID: taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL!= "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP:= item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL: item.URL,
							Technologies: item.Technologies,
							StatusCode: item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle: item.PageTitle,
							FaviconMMH3: item.FaviconMMH3,
							Auth: item.Auth,
							IP: svcIP,
							TaskID: taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain: item.Domain,
							IP: item.IP,
							Port: item.Port,
							ServiceName: item.ServiceName,
							Auth: item.Auth,
							TaskID: taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL: item.URL,
						Method: item.Method,
						Params: item.Params,
						IP: item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err!= nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID:= t.ownerNode
						sourceNodeID = &nodeID
					}
					summary:= "Agent Registered through insert_assets"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("Worker Intent #%d Registered through insert_assets", t.ownerNode)
					}
					_ = t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID)
				}
				// Automatically included in test range(source='auto'): Only for items explicitly inserted by the worker at the top level, based on its
				// Add conservative scope; assets derived from side-effect do not pass through here, so the scope is not blindly expanded. taskID=0 No operation for now.
				// Independent of coverage switch: task_scope is the boundary of the task's scope.(list/Query Filter Benchmark),
				// Coverage switch only decides whether to use it as a denominator for metrics, not whether to accumulate the range itself.
				{
					svcIP:= item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors": errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"Add domain/IP/CIDR/ICP filing/enterprise keywords to a company's [asset scope].——Domain, network, and ICP will automatically claim the hit assets; keywords only provide range hints to Agent.\n"+
			"Company name unique: create a new company if it doesn't exist, reuse if it does.(Only include the scope.). \n"+
			"scope One per line, system automatically identifies: root domain / URL / single IP / CIDR subnet / ICP filing / enterprise keywords.\n"+
			"Must provide a reason to explain the attribution basis.(whois/Certificates/ASN etc.). \n"+
			"Barrier: Reject naked TLDs and overly broad subnets(IPv4Prefix must be /16-/32, IPv6 prefix must be /32-/128), Illegal lines are skipped and returned in errors.",
		obj(map[string]any{
			"company": str("Company name(If it doesn't exist, create it; if it does, reuse it; name must be unique)"),
			"scope": str("Asset range, one per line: domain / URL / IP / CIDR / ICP filing / company keywords."),
			"reason": str("Basis for attribution(Evidence/source), Must be filled in"),
			"logo": str("Company icon URL(Optional; only effective when creating a new company.)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope Not enabled: CompanyStore Not initialized"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope string `json:"scope"`
				Reason string `json:"reason"`
				Logo string `json:"logo"`
			}
			if err:= json.Unmarshal(in, &a); err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company cannot be empty"), nil
			}
			companyID, _, err:= t.cs.UpsertCompany(a.Company, a.Logo)
			if err!= nil {
				return actool.Errorf("Failed to create/get company.: " + err.Error()), nil
			}
			lines:= splitLines(a.Scope)
			added, skipped, invalid, errMsgs:= t.cs.AddScope(companyID, lines, a.Reason)
			out:= map[string]any{
				"company_id": companyID,
				"added": added,
				"skipped": skipped,
				"invalid": invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"Add the test scope to [This Task]——This is the authorization boundary for this task, also serving as the denominator for asset test coverage.\n"+
			"kind Supports: company(Assets under the entire company name) / root_domain(The entire root domain, including all subdomains) / subdomain(Single precise subdomain) / ip / cidr / icp / keyword. \n"+
			"Note: Each worker adds the main machine it encounters to the scope automatically.(precise subdomain); This tool is used for [active expansion]——include the entire root domain / entire company, or supplement a specified subdomain/IP.\n"+
			"value: company Pass company name or id(Company must already exist); root_domain/subdomain Pass domain; ip/cidr pass IP or subnet; icp/keyword passICP filing number or enterprise keywords.\n"+
			"Must provide a reason for the action.(Auditable). Multiple entries in an array.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "Batch:[{kind, value}]. kind∈company/root_domain/subdomain/ip/cidr/icp/keyword. ", "items": map[string]any{"type": "object"}},
			"kind": str("[single] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value": str("[single] Company name or ID / domain / IP / CIDR / ICP / keyword"),
			"reason": str("Add Evidence(For auditing purposes), Must be filled in"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope Not enabled: AssetStore Not initialized"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope Task context is required(No current task)"), nil
			}
			type scopeEntry struct {
				Kind string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries []scopeEntry `json:"entries"`
				scopeEntry // Single mode
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items:= a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs:= map[string]string{}
			for i, e:= range items {
				ts, err:= t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err!= nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out:= map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"Query for assets within the scope of this task and directly related tasks that haven't been covered by fact anchors (the association range is read-only, allowing you to judge whether further testing is needed without substituting your decision)\n"+
			"Optionally filter by asset type: root_domain/subdomain/service/app/endpoint/ip.\n"+
			"Pagination: page starts from 1, page_size defaults to 10. Return {assets:[{id,type,label}], total, page, page_size}. Only available in task context.",
		obj(map[string]any{
			"type": str("Asset type filter (optional): root_domain/subdomain/service/app/endpoint/ip"),
			"page": intp("Page number, starting from 1 (default 1)."),
			"page_size": intp("Number per page (default 10)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets Not enabled: AssetStore Not initialized"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets Task context is required"), nil
			}
			var a struct {
				Type string `json:"type"`
				Page int `json:"page"`
				PageSize int `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset:= (a.Page - 1) * a.PageSize
			assets, total, err:= t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err!= nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"Query asset library: DSL expression search or direct retrieval by id/ids; supports pagination. Only returns assets within the test scope of this task and directly associated tasks.\n"+
			"DSL: field=value fuzzy(ILIKE) | field==value precise | field!=value Exclude | Numeric field support > >= < <= | Raw word=Full text fuzzy search; AND/OR combinations.(AND High priority), Use parentheses for grouping. Asset types use an independent type parameter and are not written into the DSL.\n"+
			"When id/ids are not passed, dsl must be non-empty (not allowed for unconditional full query).\n"+
			"Available fields: domain(Root/child/service domain), root_domain, ip, url, page_title, icp, service_name, app_name, method(Such as GET/POST), service_type(http|other), record_type(Like A/CNAME), technology(Array,=fuzzy ==precise), port/status_code/company_id(integer). \n"+
			"Example: status_code,>=400 AND technology=shiro; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl": str(`DSL Query expression (syntax/fields see tool description). Must be non-empty when id/ids are not passed.`),
			"type": str("Asset type filter: root_domain.|ip|subdomain|app|service|endpoint (Independent field, can stack with dsl; separate type is insufficient for query, still need dsl)"),
			"id": intp("Directly retrieve by single asset id (optional, mutually exclusive with dsl/type)"),
			"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Get multiple asset ids directly (optional, mutually exclusive with dsl/type)"},
			"limit": intp("Return limit, default 10 (optional)"),
			"offset": intp("Pagination offset, default 0 (optional)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets Not enabled: AssetStore Not initialized"), nil
			}
			var a struct {
				DSL string `json:"dsl"`
				Type string `json:"type"`
				ID int64 `json:"id"`
				IDs []int64 `json:"ids"`
				Limit int `json:"limit"`
				Offset int `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL!= "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("When id/ids are not provided, the DSL cannot be empty: No unconditional query of all assets is allowed; please provide query conditions."), nil
			}
			if err!= nil {
				return actool.Errorf("DSL error: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count": len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (enterprise) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"List 'enterprise/companies' in the asset library along with their asset scope(scope)Compared with the number of assets already assigned. Used to view which companies,"+
			"Get the company_id (used for filtering when calling list_assets with company_id)."+
			"Optional search by company name fuzzy filtering(Case Insensitive), Leave empty to return all.",
		obj(map[string]any{
			"search": str("Fuzzy filter by company name(Optional, case-insensitive); Leave empty to return all"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies Not enabled: CompanyStore Not initialized"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err:= t.cs.ListCompanies()
			if err!= nil {
				return actool.Errorf("Query company failed: " + err.Error()), nil
			}
			q:= strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID int64 `json:"id"`
				Name string `json:"name"`
				AssetCount int `json:"asset_count"`
				Scope []string `json:"scope"`
			}
			out:= make([]companyOut, 0, len(cos))
			for _, c:= range cos {
				if q!= "" &&!strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope:= make([]string, 0, len(c.Scope))
				for _, r:= range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line:= range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line!= "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings Retain: check for confirmed vulnerabilities in this task before reporting to avoid duplicate reports of the same vulnerability.
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally).
		// add_company_scope Do not assign blocked. At this time, worker is usually already in the settlement phase and has written the result back.
		t.insertAssets(), t.listAssets(),
		// Cross work review: Workers can also reuse observations from other works to avoid duplicate labor.
		// search_all_worker_traces: Do not need to know intent_id beforehand; search globally for matching steps by keyword;
		// get_worker_trace: Lock a work item and list steps/perform local search/take full content.
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail: worker After obtaining intent_id/node id, you can check the complete details of that node (in conjunction with the above replay).
		t.nodeDetail(),
		// The following tools still **do not give** to worker, only leaving planner/main (reading context and cross-work retrospection is the planning responsibility,
		// worker Only executes and writes back a single intent: list_facts / list_companies / list_worker_traces.
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work: A person can interact with a running intent.(work)Real-time injection of correction instructions (without interruption, no progress loss).
		t.steerWorkTool(),
		// set_goals: A person can add a new final target to this task at runtime (planner uses this to rejudge if the goal is achieved).
		t.setGoals(),
		// set_constraints: A person can add or modify operation constraints (allow/deny) at runtime for this task, constraining the planner/worker's exploration boundaries.
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets: Query undetected assets within this task scope as needed.(type+pagination), Decide to retest.
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen:= map[string]bool{}
	var out []actool.CoreTool
	all:= append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool:= range all {
		if!seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
