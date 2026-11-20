package db

import (
	"strconv"
	"strings"
	"testing"
)

// cleanupTreeFixtures Delete an asset and discovery created by a test case. Must be registered with defer(Rather than
// t.Cleanup):t.Cleanup Run after the test function returns.,At that time defer d.Close() Already closed the connection,
// Cleanup will silently fail and leave dirty data in the shared development library.
func cleanupTreeFixtures(d *DB, taskID int64, rootDomains ...string) {
	d.Exec(`DELETE FROM assets WHERE root_domain = ANY($1::text[])`, rootDomains) //nolint:errcheck
	d.DeleteFindingsByTask(taskID) //nolint:errcheck
}

// seedTreeAsset inserts one asset row.
func seedTreeAsset(t *testing.T, d *DB, kind string, cols map[string]any) int64 {
	t.Helper()
	names:= []string{"type"}
	values:= []any{kind}
	placeholders:= []string{"$1"}
	for k, v:= range cols {
		values = append(values, v)
		names = append(names, k)
		placeholders = append(placeholders, "$"+strconv.Itoa(len(values)))
	}
	q:= "INSERT INTO assets(" + strings.Join(names, ",") + ") VALUES (" +
		strings.Join(placeholders, ",") + ") RETURNING id"
	var id int64
	if err:= d.QueryRow(q, values...).Scan(&id); err!= nil {
		t.Fatalf("seed %s asset: %v", kind, err)
	}
	return id
}

func nodeByKey(tree *FindingAssetTree, key string) *FindingAssetNode {
	for i:= range tree.Nodes {
		if tree.Nodes[i].Key == key {
			return &tree.Nodes[i]
		}
	}
	return nil
}

// TestBuildFindingAssetTree covers the whole shape of the'By asset 'tree': the
// root→subdomain→service→endpoint chain gets rebuilt from a finding that only
// points at the leaf, ancestors aggregate their subtree, assets without any
// finding stay out, and a finding whose asset row is gone lands in the
// unassigned bucket.
func TestBuildFindingAssetTree(t *testing.T) {
	d, err:= Open(testDSN(t))
	if err!= nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err:= d.CreateTask("Asset tree test", "Target", nil, 0, 0)
	if err!= nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	const root = "tree-test.example"
	const sub = "api.tree-test.example"
	defer cleanupTreeFixtures(d, tk.ID, root)
	rootID:= seedTreeAsset(t, d, "root_domain", map[string]any{"domain": root, "root_domain": root})
	subID:= seedTreeAsset(t, d, "subdomain", map[string]any{"domain": sub, "root_domain": root})
	svcID:= seedTreeAsset(t, d, "service", map[string]any{
		"domain": sub, "root_domain": root, "url": "https://" + sub, "port": 443, "service_type": "http",
	})
	epID:= seedTreeAsset(t, d, "endpoint", map[string]any{
		"domain": sub, "root_domain": root, "url": "https://" + sub + "/admin", "port": 443, "method": "GET",
	})
	// Another service under the same domain.,Does not attach any findings. —— Should not appear in the tree.
	seedTreeAsset(t, d, "service", map[string]any{
		"domain": sub, "root_domain": root, "url": "http://" + sub + ":8080", "port": 8080, "service_type": "http",
	})

	// Only attach findings to the deepest endpoint.,The ancestor chain needs to be supplemented by the tree structure itself.
	if _, err:= d.AddFinding(tk.ID, 0, "XSS", "Reflected XSS", "high", "s", "e", "w", []int64{epID}); err!= nil {
		t.Fatal(err)
	}
	// A direct line hanging on a service,To verify the difference between Self and Total.
	if _, err:= d.AddFinding(tk.ID, 0, "Info", "Information Leakage", "low", "s", "e", "w", []int64{svcID}); err!= nil {
		t.Fatal(err)
	}
	// Asset line does not exist(Deleted asset)→ Unrelated bucket.
	if _, err:= d.AddFinding(tk.ID, 0, "Misc", "Orphan", "medium", "s", "e", "w", []int64{999000111}); err!= nil {
		t.Fatal(err)
	}

	tree, err:= d.BuildFindingAssetTree(FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10)})
	if err!= nil {
		t.Fatal(err)
	}
	if tree.FindingTotal!= 3 {
		t.Fatalf("finding_total: want 3, got %d", tree.FindingTotal)
	}

	rootNode:= nodeByKey(tree, assetKey(rootID))
	subNode:= nodeByKey(tree, assetKey(subID))
	svcNode:= nodeByKey(tree, assetKey(svcID))
	epNode:= nodeByKey(tree, assetKey(epID))
	for name, n:= range map[string]*FindingAssetNode{
		"root": rootNode, "subdomain": subNode, "service": svcNode, "endpoint": epNode,
	} {
		if n == nil {
			t.Fatalf("%s node missing from tree", name)
		}
	}

	// Parent-child chain:endpoint → service → subdomain → root_domain.
	if epNode.Parent!= svcNode.Key {
		t.Errorf("endpoint parent: want %s, got %s", svcNode.Key, epNode.Parent)
	}
	if svcNode.Parent!= subNode.Key {
		t.Errorf("service parent: want %s, got %s", subNode.Key, svcNode.Parent)
	}
	if subNode.Parent!= rootNode.Key {
		t.Errorf("subdomain parent: want %s, got %s", rootNode.Key, subNode.Parent)
	}
	if rootNode.Parent!= "" {
		t.Errorf("root parent: want top level, got %s", rootNode.Parent)
	}

	// Aggregate:Root domain two lines(endpoint High level + service The low),service One for itself, two for the subtree.
	if rootNode.Total!= 2 || rootNode.High!= 1 || rootNode.Low!= 1 {
		t.Errorf("root totals: want 2/high1/low1, got %d/high%d/low%d", rootNode.Total, rootNode.High, rootNode.Low)
	}
	if rootNode.Self!= 0 {
		t.Errorf("root self: want 0 (Just ancestors), got %d", rootNode.Self)
	}
	if svcNode.Total!= 2 || svcNode.Self!= 1 {
		t.Errorf("service total/self: want 2/1, got %d/%d", svcNode.Total, svcNode.Self)
	}
	if epNode.Total!= 1 || epNode.Self!= 1 {
		t.Errorf("endpoint total/self: want 1/1, got %d/%d", epNode.Total, epNode.Self)
	}

	// Sibling services not entered into the tree if not discovered.
	for _, n:= range tree.Nodes {
		if n.Label == "http://"+sub+":8080" {
			t.Errorf("asset without findings should be hidden: %+v", n)
		}
	}

	// Unrelated bucket receipts that point to deleted assets should be ignored.
	none:= nodeByKey(tree, FindingUnassignedAsset)
	if none == nil || none.Total!= 1 || none.Medium!= 1 {
		t.Fatalf("unassigned bucket: want 1 medium, got %+v", none)
	}
}

// TestFindingAssetScopeFilter verifiesSelecting a node narrows the findings list to
// that node's whole subtree, and that the unassigned sentinel works too.
func TestFindingAssetScopeFilter(t *testing.T) {
	d, err:= Open(testDSN(t))
	if err!= nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err:= d.CreateTask("Asset filtering test", "Target", nil, 0, 0)
	if err!= nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	const root = "scope-test.example"
	const sub = "api.scope-test.example"
	const other = "other-scope-test.example"
	defer cleanupTreeFixtures(d, tk.ID, root, other)
	rootID:= seedTreeAsset(t, d, "root_domain", map[string]any{"domain": root, "root_domain": root})
	subID:= seedTreeAsset(t, d, "subdomain", map[string]any{"domain": sub, "root_domain": root})
	otherID:= seedTreeAsset(t, d, "root_domain", map[string]any{"domain": other, "root_domain": other})

	if _, err:= d.AddFinding(tk.ID, 0, "A", "On subdomains", "high", "s", "e", "w", []int64{subID}); err!= nil {
		t.Fatal(err)
	}
	if _, err:= d.AddFinding(tk.ID, 0, "B", "On other root domains", "high", "s", "e", "w", []int64{otherID}); err!= nil {
		t.Fatal(err)
	}
	if _, err:= d.AddFinding(tk.ID, 0, "C", "No assets found", "high", "s", "e", "w", nil); err!= nil {
		t.Fatal(err)
	}
	// Discoveries pointing to deleted assets.,The same as when asset_ids are empty, it belongs to 'unassociated'——The bucket takes it in.,
	// List filtering must also retrieve them,Two inconsistent scopes will make the number on the bucket greater than the number after clicking.
	if _, err:= d.AddFinding(tk.ID, 0, "D", "Asset deleted", "high", "s", "e", "w", []int64{999000333}); err!= nil {
		t.Fatal(err)
	}

	base:= FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10)}
	cases:= []struct {
		name string
		scope string
		want int
	}{
		{"entire subtree", assetKey(rootID), 1}, // Only under the subdomain that's under the root domain.
		{"Leaf node", assetKey(subID), 1}, // Subdomain itself
		{"Another Tree", assetKey(otherID), 1}, // No interference
		{"Not associated", FindingUnassignedAsset, 2}, // asset_ids Empty + Pointing to Deleted Assets
		{"Non-existent node", "a:999000222", 0}, // No such node under the current filter → Empty Result,Do not filter out
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			f:= base
			f.AssetScope = tc.scope
			items, total, err:= d.ListFindingsPage(f, 1, 50)
			if err!= nil {
				t.Fatal(err)
			}
			if total!= tc.want || len(items)!= tc.want {
				t.Fatalf("scope %s: want %d findings, got total=%d items=%d", tc.scope, tc.want, total, len(items))
			}
		})
	}

	// When no scope, all four are present.
	if _, total, err:= d.ListFindingsPage(base, 1, 50); err!= nil || total!= 4 {
		t.Fatalf("unscoped: want 4, got %d (%v)", total, err)
	}

	// The count of buckets not associated with the tree must match the number found when opened. —— This is the assertion that will break when the twothe two measures diverge.
	tree, err:= d.BuildFindingAssetTree(base)
	if err!= nil {
		t.Fatal(err)
	}
	none:= nodeByKey(tree, FindingUnassignedAsset)
	if none == nil || none.Total!= 2 {
		t.Fatalf("unassigned bucket count: want 2, got %+v", none)
	}
}
