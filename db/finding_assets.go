package db

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Finding asset tree — the'A 'by asset' view of the global findings list.
//
// Level same as BuildCoverageGraph(company → root_domain/ip/app → subdomain →
// service → endpoint),But that is a 'force-directed graph' of assets within the scope of some task,This is
// 'Tree with assets discovered in the entire database:Only receive assets and their ancestor chain that have been discovered,Aggregate counts with subtree nodes.
// The parent-child priority rules must be consistent between the two.,Change one place, please refer to task_scope.go Change elsewhere.
// ---------------------------------------------------------------------------

// FindingUnassignedAsset The key for 'unlinked assets' nodes,Also acts as a filter sentinel for list interfaces.:
// Hits asset_ids empty or pointed assets deleted findings.
const FindingUnassignedAsset = "__none__"

// findingAssetTreeMaxNodes The upper limit of nodes returned to the frontend. Discard entire layers when exceeded.
// (endpoint Priority,Next service):Their counts have been added to the parent node,Lose nodes but not numbers.
const findingAssetTreeMaxNodes = 3000

// FindingAssetNode Is a node in the asset tree. Key is isomorphic with the coverage graph:Asset row is "a:<id>",
// The enterprise is "c:<id>", The root domain without asset rows is synthetic. "r:<domain>", Unassociated bucket is "__none__".
type FindingAssetNode struct {
	Key string `json:"key"`
	Parent string `json:"parent,omitempty"`
	Kind string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint|none
	Label string `json:"label"`
	AssetID int64 `json:"asset_id,omitempty"`
	CompanyID int64 `json:"company_id,omitempty"`
	// Self The number of findings directly attached to this asset;Total Include all descendants and deduplicate by finding.
	// (When a discovery is associated with multiple assets.,Only counted once on the common ancestor).
	Self int `json:"self"`
	Total int `json:"total"`
	Critical int `json:"critical"`
	High int `json:"high"`
	Medium int `json:"medium"`
	Low int `json:"low"`
	LastFoundAt time.Time `json:"last_found_at"`
}

// FindingAssetTree A single snapshot of the entire tree. Nodes are already sorted.:By discovery count under the same parent node
// Descending, label ascending,'Unrelated assets always last.
type FindingAssetTree struct {
	Nodes []FindingAssetNode `json:"nodes"`
	FindingTotal int `json:"finding_total"`
	// Truncated=true Represents control volume discard of hierarchy in DroppedKinds.
	Truncated bool `json:"truncated"`
	DroppedKinds []string `json:"dropped_kinds,omitempty"`
}

// assetRow A subset of asset fields required for the tree structure.
type assetRow struct {
	id int64
	kind string
	companyID int64
	domain string
	rootDomain string
	ip string
	url string
	port int
	serviceType string
	appName string
}

func (a *assetRow) coverageNode() CoverageGraphNode {
	return CoverageGraphNode{
		Kind: a.kind, Domain: a.domain, RootDomain: a.rootDomain, IP: a.ip,
		URL: a.url, Port: a.port, ServiceType: a.serviceType, AppName: a.appName,
	}
}

// label Reuse coverage graph label rules(URL > domain > ip > app_name > root_domain),But Without
// URL Services(SMB, Non-HTTP ports, etc.)Need to add the port:Otherwise, its label will be on the same line as the host IP/domain
// Exactly the same,The parent and child rows on the tree look completely repeated.
func (a *assetRow) label() string {
	if a.kind == "service" && a.url == "" {
		if host, port:= a.hostPort(); host!= "" && port > 0 {
			return host + ":" + strconv.Itoa(port)
		}
	}
	n:= a.coverageNode()
	n.Key = assetKey(a.id)
	return coverageNodeLabel(&n)
}

// hostPort Consistent with coverage graph:Prioritize domain,Next, the host in the URL.,Last IP.
func (a *assetRow) hostPort() (string, int) {
	n:= a.coverageNode()
	return hostPortOf(&n)
}

const findingAssetSelectCols = `a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,'')`

func scanAssetRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]*assetRow, error) {
	defer rows.Close()
	var out []*assetRow
	for rows.Next() {
		a:= &assetRow{}
		if err:= rows.Scan(&a.id, &a.kind, &a.companyID, &a.domain, &a.rootDomain,
			&a.ip, &a.url, &a.port, &a.serviceType, &a.appName); err!= nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// findingAssetHit This is the minimum information required for a finding during the tree construction phase.
type findingAssetHit struct {
	severity string
	ts time.Time
	assetIDs []int64
}

// BuildFindingAssetTree Build the asset tree based on the current filter. AssetScope itself does not participate.(Otherwise the tree will follow
// Selected node collapsed into a chain).
func (d *DB) BuildFindingAssetTree(f FindingFilter) (*FindingAssetTree, error) {
	return d.buildFindingAssetTree(f, findingAssetTreeMaxNodes)
}

// buildFindingAssetTree Internal implementation with node limits. maxNodes<=0 Indicates no truncation——Parse
// AssetScope Must use this pattern,Otherwise, the discarded endpoint will cause an incomplete set of child tree ids.
func (d *DB) buildFindingAssetTree(f FindingFilter, maxNodes int) (*FindingAssetTree, error) {
	f.AssetScope = ""
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	where, args:= f.where()

	rows, err:= d.Query(`SELECT COALESCE(f.severity,''), f.created_at,
       COALESCE(f.asset_ids::text,'[]')
FROM findings f LEFT JOIN tasks t ON f.task_id = t.id`+where, args...)
	if err!= nil {
		return nil, err
	}
	hits:= []findingAssetHit{}
	assetIDs:= map[int64]bool{}
	for rows.Next() {
		var h findingAssetHit
		var aidsJSON string
		if err:= rows.Scan(&h.severity, &h.ts, &aidsJSON); err!= nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &h.assetIDs)
		for _, id:= range h.assetIDs {
			if id > 0 {
				assetIDs[id] = true
			}
		}
		hits = append(hits, h)
	}
	if err:= rows.Err(); err!= nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	tree:= &FindingAssetTree{Nodes: []FindingAssetNode{}, FindingTotal: len(hits)}
	byID, err:= d.loadFindingAssetRows(assetIDs)
	if err!= nil {
		return nil, err
	}

	nodes, parentOf:= d.assembleFindingAssetNodes(byID)
	if err:= d.attachCompanyNodes(nodes, parentOf); err!= nil {
		return nil, err
	}

	// Count:A finding is propagated up the ancestor chain of each asset along it.,Collect the deduplicated set of keys and process them one by one. +1,
	// So parent nodes won't be counted multiple times because a single finding can hang multiple child assets.
	unassigned:= &FindingAssetNode{Key: FindingUnassignedAsset, Kind: "none", Label: "Unassociated asset"}
	touched:= map[string]bool{}
	for _, h:= range hits {
		clear(touched)
		var direct []*FindingAssetNode
		for _, id:= range h.assetIDs {
			node:= nodes[assetKey(id)]
			if node == nil {
				continue
			}
			direct = append(direct, node)
			for key:= node.Key; key!= ""; key = parentOf[key] {
				touched[key] = true
			}
		}
		if len(direct) == 0 {
			countFinding(unassigned, h)
			unassigned.Self++
			continue
		}
		for _, node:= range direct {
			node.Self++
		}
		for key:= range touched {
			countFinding(nodes[key], h)
		}
	}

	for _, node:= range nodes {
		if node.Total > 0 {
			tree.Nodes = append(tree.Nodes, *node)
		}
	}
	if unassigned.Total > 0 {
		tree.Nodes = append(tree.Nodes, *unassigned)
	}
	sortFindingAssetNodes(tree.Nodes)
	truncateFindingAssetTree(tree, maxNodes)
	return tree, nil
}

// countFinding Accumulate one finding to a node.(Total / severity buckets / most recent discovery time).
func countFinding(n *FindingAssetNode, h findingAssetHit) {
	if n == nil {
		return
	}
	n.Total++
	switch h.severity {
	case "critical":
		n.Critical++
	case "high":
		n.High++
	case "medium":
		n.Medium++
	case "low":
		n.Low++
	}
	if h.ts.After(n.LastFoundAt) {
		n.LastFoundAt = h.ts
	}
}

// loadFindingAssetRows Read the hit asset line,Completes ancestors per round(service The host domain/IP,
// The root domain of a subdomain). The ancestor itself may have no findings.,But the tree needs them to take shape.
func (d *DB) loadFindingAssetRows(ids map[int64]bool) (map[int64]*assetRow, error) {
	byID:= map[int64]*assetRow{}
	if len(ids) == 0 {
		return byID, nil
	}
	idList:= make([]int64, 0, len(ids))
	for id:= range ids {
		idList = append(idList, id)
	}
	rows, err:= d.Query(`SELECT `+findingAssetSelectCols+` FROM assets a WHERE a.id = ANY($1::bigint[])`, idList)
	if err!= nil {
		return nil, err
	}
	found, err:= scanAssetRows(rows)
	if err!= nil {
		return nil, err
	}
	for _, a:= range found {
		byID[a.id] = a
	}

	// Each round finds the host identifier that still lacks a parent node.,Batch add one layer;Fixed level(endpoint→service→
	// subdomain/ip→root_domain),4 Round has converged enough.
	for range 4 {
		want:= missingParents(byID)
		if want.empty() {
			break
		}
		added, err:= d.loadAssetsByHost(want, byID)
		if err!= nil {
			return nil, err
		}
		if added == 0 {
			break
		}
	}
	return byID, nil
}

// missingHosts This is the host identifier to be found in the database for a round of completion.,Separated by target asset type.
type missingHosts struct {
	services []string // endpoint The host of(Find the 'service' line.)
	domains []string // service/endpoint The host domain of(Find subdomain lines)
	ips []string // service/endpoint Host IP of(Find IP line)
	roots []string // The root domain of a subdomain(Find the root_domain line)
}

func (m missingHosts) empty() bool {
	return len(m.services) == 0 && len(m.domains) == 0 && len(m.ips) == 0 && len(m.roots) == 0
}

// missingParents Summarize hosts that haven't been loaded yet:service(For endpoint attachment), Subdomain/IP(provide
// service Attached to the endpoint.)with root domain(For subdomain hosting.).
func missingParents(byID map[int64]*assetRow) missingHosts {
	haveService:= map[string]bool{}
	haveDomain:= map[string]bool{}
	haveIP:= map[string]bool{}
	haveRoot:= map[string]bool{}
	for _, a:= range byID {
		switch a.kind {
		case "service":
			if host, _:= a.hostPort(); host!= "" {
				haveService[host] = true
			}
		case "subdomain":
			haveDomain[a.domain] = true
		case "ip":
			haveIP[a.ip] = true
		case "root_domain":
			haveRoot[a.domain] = true
		}
	}
	wantService:= map[string]bool{}
	wantDomain:= map[string]bool{}
	wantIP:= map[string]bool{}
	wantRoot:= map[string]bool{}
	for _, a:= range byID {
		switch a.kind {
		case "service", "endpoint":
			host, _:= a.hostPort()
			// endpoint First find co-host service.;Services with port mismatches will be marked as Total=0
			// Filtered out in the end,Will not pollute the tree.
			if a.kind == "endpoint" && host!= "" &&!haveService[host] {
				wantService[host] = true
			}
			if host!= "" &&!haveDomain[host] &&!haveIP[host] &&!haveRoot[host] {
				if isIPLiteral(host) {
					wantIP[host] = true
				} else {
					wantDomain[host] = true
				}
			}
			if a.ip!= "" &&!haveIP[a.ip] {
				wantIP[a.ip] = true
			}
		case "subdomain":
			if a.rootDomain!= "" &&!haveRoot[a.rootDomain] {
				wantRoot[a.rootDomain] = true
			}
		}
	}
	return missingHosts{
		services: keysOf(wantService),
		domains: keysOf(wantDomain),
		ips: keysOf(wantIP),
		roots: keysOf(wantRoot),
	}
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out:= make([]string, 0, len(m))
	for k:= range m {
		out = append(out, k)
	}
	return out
}

// isIPLiteral Roughly determine if a host is an IP literal(Used to decide whether to look in ip or subdomain table for host).
func isIPLiteral(host string) bool {
	if strings.Contains(host, ":") {
		return true // IPv6
	}
	if host == "" {
		return false
	}
	for _, part:= range strings.Split(host, ".") {
		if part == "" {
			return false
		}
		if _, err:= strconv.Atoi(part); err!= nil {
			return false
		}
	}
	return strings.Count(host, ".") == 3
}

// loadAssetsByHost Batch complete asset lines by host identifier,Return the number of new lines added in this round.
func (d *DB) loadAssetsByHost(want missingHosts, byID map[int64]*assetRow) (int, error) {
	added:= 0
	load:= func(q string, arg []string) error {
		if len(arg) == 0 {
			return nil
		}
		rows, err:= d.Query(q, arg)
		if err!= nil {
			return err
		}
		found, err:= scanAssetRows(rows)
		if err!= nil {
			return err
		}
		for _, a:= range found {
			if _, ok:= byID[a.id]; ok {
				continue
			}
			byID[a.id] = a
			added++
		}
		return nil
	}
	if err:= load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='service' AND (a.domain = ANY($1::text[]) OR a.ip = ANY($1::text[]))`, want.services); err!= nil {
		return 0, err
	}
	if err:= load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='subdomain' AND a.domain = ANY($1::text[])`, want.domains); err!= nil {
		return 0, err
	}
	if err:= load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='ip' AND a.ip = ANY($1::text[])`, want.ips); err!= nil {
		return 0, err
	}
	if err:= load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='root_domain' AND a.domain = ANY($1::text[])`, want.roots); err!= nil {
		return 0, err
	}
	return added, nil
}

// assembleFindingAssetNodes Convert asset rows to nodes and connect parent-child relationships. When parent node is missing(In the library
// There is no root domain asset with that name)Synthesize "r:<domain>" Placeholder node,Consistent with handling the overlay graph.
func (d *DB) assembleFindingAssetNodes(byID map[int64]*assetRow) (map[string]*FindingAssetNode, map[string]string) {
	nodes:= map[string]*FindingAssetNode{}
	parentOf:= map[string]string{}
	rootByDomain:= map[string]string{}
	subByDomain:= map[string]string{}
	ipByAddr:= map[string]string{}
	svcByHost:= map[string]string{}
	svcByHostPort:= map[string]string{}

	for _, a:= range byID {
		key:= assetKey(a.id)
		nodes[key] = &FindingAssetNode{
			Key: key, Kind: a.kind, Label: a.label(),
			AssetID: a.id, CompanyID: a.companyID,
		}
		switch a.kind {
		case "root_domain":
			if a.domain!= "" {
				rootByDomain[a.domain] = key
			}
		case "subdomain":
			if a.domain!= "" {
				subByDomain[a.domain] = key
			}
		case "ip":
			if a.ip!= "" {
				ipByAddr[a.ip] = key
			}
		case "service":
			if host, port:= a.hostPort(); host!= "" {
				svcByHost[host] = key
				svcByHostPort[host+"|"+strconv.Itoa(port)] = key
			}
		}
	}

	// When the root domain of a subdomain is not in the asset list in the database,,Synthesize a Placeholder Root,To prevent subdomains from scattering at the top level
	for _, a:= range byID {
		if a.kind!= "subdomain" || a.rootDomain == "" {
			continue
		}
		if _, ok:= rootByDomain[a.rootDomain]; ok {
			continue
		}
		key:= "r:" + a.rootDomain
		nodes[key] = &FindingAssetNode{Key: key, Kind: "root_domain", Label: a.rootDomain}
		rootByDomain[a.rootDomain] = key
	}

	firstOf:= func(keys ...string) string {
		for _, k:= range keys {
			if k!= "" {
				if _, ok:= nodes[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for _, a:= range byID {
		key:= assetKey(a.id)
		var parent string
		switch a.kind {
		case "subdomain":
			parent = firstOf(rootByDomain[a.rootDomain])
		case "service":
			host, _:= a.hostPort()
			parent = firstOf(subByDomain[a.domain], subByDomain[host],
				ipByAddr[a.ip], ipByAddr[host], rootByDomain[a.rootDomain], rootByDomain[host])
		case "endpoint":
			host, port:= a.hostPort()
			parent = firstOf(svcByHostPort[host+"|"+strconv.Itoa(port)], svcByHost[host],
				subByDomain[host], subByDomain[a.domain], ipByAddr[host], ipByAddr[a.ip],
				rootByDomain[a.rootDomain], rootByDomain[host])
		}
		if parent!= "" && parent!= key {
			parentOf[key] = parent
			nodes[key].Parent = parent
		}
	}
	return nodes, parentOf
}

// attachCompanyNodes Give top-level asset(Root domain / IP / Application)Supplement enterprise parent node——Only if the asset is indeed.
// Enterprise layer appears only if it belongs to an enterprise,Unassigned assets are still considered top-level.
func (d *DB) attachCompanyNodes(nodes map[string]*FindingAssetNode, parentOf map[string]string) error {
	want:= map[int64]bool{}
	for _, n:= range nodes {
		if n.Parent!= "" || n.CompanyID <= 0 {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			want[n.CompanyID] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	ids:= make([]int64, 0, len(want))
	for id:= range want {
		ids = append(ids, id)
	}
	rows, err:= d.Query(`SELECT id, COALESCE(name,'') FROM companies WHERE id = ANY($1::bigint[])`, ids)
	if err!= nil {
		return err
	}
	defer rows.Close()
	names:= map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err:= rows.Scan(&id, &name); err!= nil {
			return err
		}
		names[id] = name
	}
	if err:= rows.Err(); err!= nil {
		return err
	}
	for id, name:= range names {
		key:= companyKey(id)
		if _, ok:= nodes[key]; ok {
			continue
		}
		if name == "" {
			name = "enterprise #" + strconv.FormatInt(id, 10)
		}
		nodes[key] = &FindingAssetNode{Key: key, Kind: "company", Label: name, CompanyID: id}
	}
	for _, n:= range nodes {
		if n.Parent!= "" || n.CompanyID <= 0 || n.Kind == "company" {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			key:= companyKey(n.CompanyID)
			if _, ok:= nodes[key];!ok {
				continue
			}
			n.Parent = key
			parentOf[n.Key] = key
		}
	}
	return nil
}

// sortFindingAssetNodes Sort:More found first,Same number by label;'Unrelated assets always last.
// Frontend attaches child nodes in array order.,So long as the relative order under the same parent node is correct.
func sortFindingAssetNodes(nodes []FindingAssetNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b:= nodes[i], nodes[j]
		if (a.Kind == "none")!= (b.Kind == "none") {
			return b.Kind == "none"
		}
		if a.Total!= b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
}

// truncateFindingAssetTree Discard entire layers when there are too many nodes(First endpoint, then service.). Count already
// Add to parent node,Just discard the expandable detail levels.
func truncateFindingAssetTree(tree *FindingAssetTree, maxNodes int) {
	if maxNodes <= 0 || len(tree.Nodes) <= maxNodes {
		return
	}
	for _, kind:= range []string{"endpoint", "service"} {
		kept:= tree.Nodes[:0]
		for _, n:= range tree.Nodes {
			if n.Kind == kind {
				continue
			}
			kept = append(kept, n)
		}
		tree.Nodes = kept
		tree.Truncated = true
		tree.DroppedKinds = append(tree.DroppedKinds, kind)
		if len(tree.Nodes) <= maxNodes {
			return
		}
	}
}

// applyAssetScope Add AssetScope(Node key)Parse into a set of asset IDs usable for SQL. Select
// One node equals the entire subtree selected,So, build the tree first before collecting descendants.
func (d *DB) applyAssetScope(f FindingFilter) (FindingFilter, error) {
	scope:= strings.TrimSpace(f.AssetScope)
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	if scope == "" {
		return f, nil
	}
	if scope == FindingUnassignedAsset {
		f.assetNone = true
		return f, nil
	}
	// No Truncation:The endpoint that was dropped should also participate in ID collection.,Otherwise, the list will be missing data.
	tree, err:= d.buildFindingAssetTree(f, 0)
	if err!= nil {
		return f, err
	}
	children:= map[string][]FindingAssetNode{}
	byKey:= map[string]FindingAssetNode{}
	for _, n:= range tree.Nodes {
		byKey[n.Key] = n
		children[n.Parent] = append(children[n.Parent], n)
	}
	if _, ok:= byKey[scope];!ok {
		// The selected node no longer exists under the current filter.,The result should be empty rather than degrading into no filtering.
		f.assetMiss = true
		return f, nil
	}
	seen:= map[string]bool{scope: true}
	queue:= []string{scope}
	for len(queue) > 0 {
		key:= queue[0]
		queue = queue[1:]
		if id:= byKey[key].AssetID; id > 0 {
			f.assetIDs = append(f.assetIDs, id)
		}
		for _, child:= range children[key] {
			if seen[child.Key] {
				continue
			}
			seen[child.Key] = true
			queue = append(queue, child.Key)
		}
	}
	if len(f.assetIDs) == 0 {
		f.assetMiss = true
	}
	return f, nil
}

// assetIDContainments Convert asset id to jsonb containing the right-hand side operand set for judgment,Coordinate
// idx_findings_asset_ids(GIN jsonb_path_ops)Use.
func assetIDContainments(ids []int64) []string {
	out:= make([]string, 0, len(ids))
	for _, id:= range ids {
		out = append(out, "["+strconv.FormatInt(id, 10)+"]")
	}
	return out
}
