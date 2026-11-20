package db

import (
	"fmt"
	"testing"
	"time"
)

// InsertSettingIfAbsent Is auth.password_hash Fallback: the caller's GetSetting check may
// Invalidated due to database error, may also be preempted by concurrent requests (bcrypt takes several milliseconds), so"Only on first
// Configurable"The guarantee must be on the primary key constraint, not an upper-level if judgment.
func TestInsertSettingIfAbsentDoesNotOverwrite(t *testing.T) {
	d, err:= Open(testDSN(t))
	if err!= nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// Use a test-specific key; never touch the actual auth in the development database..password_hash.
	key:= fmt.Sprintf("test.insert_if_absent.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()

	inserted, err:= d.InsertSettingIfAbsent(key, "first")
	if err!= nil {
		t.Fatal(err)
	}
	if!inserted {
		t.Fatal("The first write should return inserted.=true")
	}

	inserted, err = d.InsertSettingIfAbsent(key, "second")
	if err!= nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("When the key already exists, return inserted.=false")
	}

	got, ok, err:= d.GetSetting(key)
	if err!= nil ||!ok {
		t.Fatalf("GetSetting: ok=%v err=%v", ok, err)
	}
	if got!= "first" {
		t.Fatalf("Value overwritten to %q, Should be kept %q", got, "first")
	}
}
