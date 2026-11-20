package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got:= MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("Mask value leaked full credentials: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("Mask value should not expose the main body of the address: %q", got)
	}
	// Last 6 digits must be retained for users to recognize which bot it is.
	if!strings.HasSuffix(got, "123456") {
		t.Fatalf("The last 6 digits should be retained as an identification hint: %q", got)
	}
	if!IsMasked(got) {
		t.Fatalf("The mask value must be recognizable by IsMasked: %q", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// Short credentials exposing the last 6 digits also expose the entire credential.
	for _, s:= range []string{"abc", "abcdef", ""} {
		got:= MaskedValue(s)
		if got!= MaskedPrefix {
			t.Fatalf("Length %d Credentials should not give tail prompts, getting %q", len(s), got)
		}
		if s!= "" && strings.Contains(got, s) {
			t.Fatalf("Mask value contains original value: %q", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg:= map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret": "SECtest123456",
		"port": float64(587),
		"host": "smtp.example.com",
	}
	masked:= MaskConfig(KindDingTalk, cfg)
	for _, k:= range []string{"webhook", "secret"} {
		s, _:= masked[k].(string)
		if!IsMasked(s) {
			t.Errorf("%s Should be masked, got %q", k, s)
		}
	}
	// Non-credential fields must be retained as is, otherwise the UI cannot display them.
	if masked["port"]!= float64(587) {
		t.Errorf("Non-credential field port should not be changed.: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// When channel type cannot be identified, prefer showing an empty configuration in the UI rather than returning potentially credential-containing original content.
	got:= MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got)!= 0 {
		t.Fatalf("Unknown channel type should return empty configuration, resulting in %v", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// Masking is behavior at the presentation layer; it should not reverse and alter true values in the database.
	cfg:= map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig Changing the input parameters will cause real credentials to be overwritten by masked values.")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored:= map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// User only changed method, browser submitted masked value.+New method.
	incoming:= map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret": MaskedValue("REALSECRET"),
		"method": "PUT",
	}
	got:= MergeConfig(stored, incoming)
	if got["webhook"]!= "https://real/hook" || got["secret"]!= "REALSECRET" {
		t.Fatalf("Masked fields should retain the original value from the database, resulting in %v", got)
	}
	if got["method"]!= "PUT" {
		t.Fatalf("Modified fields should take effect, got %v", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored:= map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got:= MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok:= got["secret"]; ok {
		t.Fatalf("An empty string should clear this field, resulting in %v", got)
	}
	// Retain unspecified fields (partial update semantics).
	if got["webhook"]!= "https://real/hook" {
		t.Fatalf("Unmentioned fields should be retained, resulting in %v", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored:= map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got:= MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"]!= "smtp.example.com" || got["password"]!= "pw" {
		t.Fatalf("Unmentioned fields should be retained, resulting in %v", got)
	}
	if got["port"]!= float64(465) {
		t.Fatalf("Fields mentioned should be updated, resulting in %v", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap This is one of the most important security invariants in this package:
// **Changing the target address should not bring over old credentials.**.
//
// These use cases employ the attack shape's input (only change address, avoid credentials),
// Instead of 'correct input for defense logic'——If only the latter is tested, defense will not take effect and everything will still be green.
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases:= []struct {
		name string
		kind string
		stored map[string]any
		incoming map[string]any
		// wantMissing It is the expected credential key to be called out.
		wantMissing string
	}{
		{
			name: "Generic Webhook changing address wants to reuse Authorization header",
			kind: KindWebhook,
			stored: map[string]any{
				"url": "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming: map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name: "Telegram Change base_url to send Bot Token to your own endpoint.",
			kind: KindTelegram,
			stored: map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name: "Changing SMTP host for email requires submitting a password.",
			kind: KindEmail,
			stored: map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming: map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name: "Email turning off TLS must also restate the password.",
			kind: KindEmail,
			stored: map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming: map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// Masked Value = 'Reusing old credentials', in the context of address changes, must also be refused.
			name: "Return masked credentials + New Address",
			kind: KindTelegram,
			stored: map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name: "The DingTalk webhook wants to continue using the signature key.",
			kind: KindDingTalk,
			stored: map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming: map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err:= PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("Changing the address without re-attesting credentials should be rejected; get configuration %v", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if!errors.As(err, &target) {
				t.Fatalf("Should return a specific error type for the interface to provide actionable prompts, resulting in %T: %v", err, err)
			}
			found:= false
			for _, m:= range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if!found {
				t.Fatalf("Credential key missing from the list of credentials to be used. %q, Got %v", tc.wantMissing, target.Missing)
			}
			// Error messages should guide the operator on how to fix it.
			if!strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("Error Message Should Mention %q: %v", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits Reverse case: normal editing should not be mistakenly blocked,
// Otherwise, this protection will be bypassed or deleted due to being too annoying.
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases:= []struct {
		name string
		kind string
		stored map[string]any
		incoming map[string]any
	}{
		{
			name: "Only change the name (original configuration returned).",
			kind: KindWebhook,
			stored: map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name: "Only change the request method, address, and credentials remain unchanged",
			kind: KindWebhook,
			stored: map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name: "Change address and**At the same time**Provide New Credentials",
			kind: KindWebhook,
			stored: map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name: "Change address and explicitly declare no longer need credentials.",
			kind: KindWebhook,
			stored: map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name: "Telegram Change chat_id (not the destination)",
			kind: KindTelegram,
			stored: map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name: "Change the recipient of the email (not the destination).",
			kind: KindEmail,
			stored: map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err:= PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err!= nil {
				t.Fatalf("Legal edits are mistakenly blocked: %v", err)
			}
			if merged == nil {
				t.Fatal("Should return merged results")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance Cover an easily misjudged detail:
// Frontend submitted port is JSON number (float64), read back as float64,
// But the types of the two values may be different (e.g., int vs float64). Use == Comparison will treat 'unchanged' as 'changed'.
// Thus, for users who only changed their names, it prompts 'Please re-enter password'——False alarms can make people lose trust in this protection.
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored:= map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// The same port submitted as an int.
	if _, err:= PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err!= nil {
		t.Fatalf("Ports with the same value (but different types) should not be considered an address change.: %v", err)
	}
	// If the port really changes, it must be blocked.
	if _, err:= PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("Port changes should be intercepted")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination Override 'leave empty'
// The 'destination field' path: Telegram's base_url left empty means using the official API address.
//
// Previously, channels would permanently fail from the second save onwards:
//
//	When creating, save the base_url in the database.:"" (Create path directly storing frontend submitted config, not using MergeConfig)
//	→ First save, MergeConfig treats an empty string as explicit clearing and deletes that key
//	→ Second save, incoming is still "", And stored already doesn't have this key, judged as 'address changed'.
//	→ bot_token Masked echo value → 400'Target address has changed, please re-enter credential fields simultaneously
//
// The user made no changes, but they can no longer save unless they re-paste the Bot Token.
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored:= map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// Frontend buildConfig() Submit a value for each field of the channel: Credential fill mask,
	// Submit an empty string for an empty text box. Here, I reproduce its full output rather than just submitting 'changed keys'.
	submit:= func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id": "-100",
			"base_url": "",
		}
	}

	// First save: Only changed the channel name, config returned unchanged.
	merged, err:= PrepareConfigUpdate(KindTelegram, stored, submit())
	if err!= nil {
		t.Fatalf("First save was mistakenly blocked: %v", err)
	}
	if _, ok:= merged["base_url"]; ok {
		t.Fatal("Premise changed: empty string should be deleted by MergeConfig——This test case covers exactly the step where a key disappears.")
	}

	// Second save: Submitted content identical to previous submission, user made no changes.
	merged2, err:= PrepareConfigUpdate(KindTelegram, merged, submit())
	if err!= nil {
		t.Fatalf("The second save was mistakenly blocked (user made no changes): %v", err)
	}
	// Third time, confirm it's not 'just a one-time mistake' but stable and savable.
	if _, err:= PrepareConfigUpdate(KindTelegram, merged2, submit()); err!= nil {
		t.Fatalf("Third save mistakenly blocked: %v", err)
	}
	// Credentials must be carried throughout without being inadvertently cleared by empty string logic.
	if got:= merged2["bot_token"]; got!= "123:ABC" {
		t.Fatalf("Bot Token Should use original value, getting %v", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges Pair with the previous test case
// Assertion: Treat empty strings and 'key not found' as equivalent,**Can't**Accompanying true address change.
// Both directions are real credential exfiltration paths.——Telegram Bot Token walks in the URL path,
// Changing base_url is equivalent to giving the Token to a new address.
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// Direction one: From 'empty' (official address) to self-hosted address.
	official:= map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err:= PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url": "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("When switching from the official address to a self-built address, you must re-enter the Token.")
	}

	// Direction 2: Clear the self-built address (= Switch back to official API) is also an address change.
	proxied:= map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err:= PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url": "",
	}); err == nil {
		t.Fatal("Clearing custom addresses (switching back to official API) is also an address change, requiring re-entering the Token")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	// Similar to SecretKeys: if the channel forgets to declare the destination key, PrepareConfigUpdate won't protect it.
	for kind, ch:= range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("Channel %s Destination key not declared; changing the address does not protect it from credential theft.", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("Channel %s Credential key not declared", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// The compiler has already enforced each channel to implement SecretKeys, confirming once more that 'no channels are masking'
	// Give up——A channel returning an empty slice means its credentials will be plaintext echoed in the browser.
	expect:= map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch:= range registry {
		if!expect[kind] {
			t.Errorf("Channel %s Mask expectation not registered in testing.", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("Channel %s No credential fields are declared, so their configuration will be echoed in plain text.", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer Cover an audit point pointed out by the audit log:
// Insert Mask Sentinel**non-string**Structure (like webhook.headers When it's an object),
// MergeConfig Only recognizes 'string with prefix' as a mask, so literal value. "__masked__" Will be treated as
// Real header values stored in the database——Subsequent authentication quiet failure, and no error messages.
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored:= map[string]any{
		"url": "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// Object contains embedded mask sentinel.
	incoming:= map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err:= PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("Masked sentinels within struct bodies should be rejected (otherwise literals would be stored in the database).")
	}
	// The entire submission object (actual new value) is accepted as usual.
	ok:= map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err:= PrepareConfigUpdate(KindWebhook, stored, ok); err!= nil {
		t.Fatalf("Normal submission of new request headers should not be blocked.: %v", err)
	}
}
