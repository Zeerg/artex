package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel A universal Webhook adapter: user-defined URL, method, request headers, and JSON template.
// Its existence allows this feature to avoid writing separate implementations for Slack / Mattermost / Discord / custom systems.——
// Any platform can be covered by a configurable template',
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// The generic Webhook has no official restrictions, returning 0 means default no rate limiting, determined by the user based on the peer's capability.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Masked URL and headers: The target address itself often carries a token, custom headers usually contain authorization credentials,
// Both will appear in the interface echo, so both need to be blocked.
// Cost is editing when you want to change one of the headers; need to fill the entire group of headers again (mask values will be interpreted as 'keep original value').——
// This trade-off was intentional: rather than fill it once more, do not echo the credential to the browser.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// The destination is a URL. When changing the URL, you must restate headers —— Otherwise, original Authorization header.
// Sent verbatim to new address, which is the main path for mask bypass.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate Fallback request body for unpopulated templates: a straightforward JSON structure,
// Cover most self-built receivers that accept one JSON entry per request.
const webhookDefaultTemplate = `{
 "title": {{json .Title}},
 "batch": {{.Batch}},
 "count": {{.Count}},
 "items": [
{{- range $i, $it:= .Items}}
{{- if $i}},{{end}}
 {
 "finding_id": {{$it.FindingID}},
 "name": {{json $it.Name}},
 "vulnclass": {{json $it.VulnClass}},
 "severity": {{json $it.Severity}},
 "summary": {{json $it.Summary}},
 "assets": {{json $it.Assets}},
 "detail_url": {{json $it.DetailURL}}
 }
{{- end}}
 ]
}`

// webhookTemplateData Exposed to the user as a template context.
type webhookTemplateData struct {
	Title string
	Batch bool
	Count int
	Items []webhookItem
	HomeURL string
	// SentAt Current delivery time (RFC3339), for the receiver to record.
	SentAt string
}

type webhookItem struct {
	FindingID int64
	Name string
	VulnClass string
	Severity string
	SeverityLabel string
	Summary string
	Assets []string
	DetailURL string
	FromStatus string
	ToStatus string
	// StatusLabel Readable description of status change, like 'Pending', → 'Fixed'; empty when not a state change.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw:= cfgString(cfg, "url")
	if raw == "" {
		return errors.New("Missing target URL")
	}
	if err:= validateHTTPURL(raw); err!= nil {
		return fmt.Errorf("Invalid target URL.: %w", err)
	}
	if m:= strings.ToUpper(cfgString(cfg, "method")); m!= "" && m!= http.MethodGet && m!= http.MethodPost && m!= http.MethodPut && m!= http.MethodPatch {
		return fmt.Errorf("Unsupported method %s (Supports GET/POST/PUT/PATCH.", m)
	}
	if tpl:= cfgString(cfg, "body_template"); tpl!= "" {
		if _, err:= parseWebhookTemplate(tpl); err!= nil {
			return fmt.Errorf("Request body template syntax error: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	method:= strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET Without request body: Putting content into query exceeds template capabilities, and does not conform to GET semantics,
	// So GET is suitable for 'trigger hooks on hit' type receivers.
	var payload any
	if method!= http.MethodGet {
		body, err:= renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err!= nil {
			return 0, Permanent(err)
		}
		// Template rendering produces a string form of JSON, here converted to json.RawMessage Sent as is,
		// Avoid double escaping to put user-constructed structures inside a JSON string.
		if!json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("The request body template rendering result is not valid JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers:= cfgMap(cfg, "headers")
	if ct:= cfgString(cfg, "content_type"); ct!= "" {
		// Allow overriding but apply after headers, ensuring explicit configuration takes precedence.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err:= doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err!= nil {
		return 0, err
	}
	// General webhook does not truncate the body (the receiving end is the user's own service, size determined by body_template),
	// Therefore, the entire batch is considered delivered.
	return len(m.Items), nil
}

// renderWebhookBody Render the request body using a user template (or default template).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err:= parseWebhookTemplate(tpl)
	if err!= nil {
		return "", fmt.Errorf("Request body template syntax error: %w", err)
	}
	var buf bytes.Buffer
	if err:= t.Execute(&buf, newWebhookTemplateData(m)); err!= nil {
		return "", fmt.Errorf("Failed to render request body template: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate Parsing template.
//
// missingkey=zero Render missing map keys as zero values instead of errors——But the context of this file is a struct,
// Main purpose is to let .Items Range does not error when empty. The real defense is against .Items Is nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs Helper function exposed to templates.
var webhookTemplateFuncs = template.FuncMap{
	// json Serialize any value into JSON.
	//
	// This function is not just an embellishment but essential: without it, users can only write {{.Title}} Direct interpolation,
	// Any quotes or newlines in the vulnerability title make the entire request body no longer valid JSON.——The receiving end will
	// Rejected and reports an error message pointing to 'JSON parsing failed', completely unable to imagine there's a quote in the title.
	"json": func(v any) (string, error) {
		raw, err:= json.Marshal(v)
		if err!= nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons To embed a JSON snippet into another JSON string value (doing one layer of string escaping).
	"jsons": func(v any) (string, error) {
		raw, err:= json.Marshal(v)
		if err!= nil {
			return "", err
		}
		quoted, err:= json.Marshal(string(raw))
		if err!= nil {
			return "", err
		}
		// Remove outer quotes: the caller decides whether to add quotes.
		return string(quoted[1: len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d:= webhookTemplateData{
		Title: markdownTitle(m),
		Batch: m.Batch,
		Count: len(m.Items),
		HomeURL: m.HomeURL,
		SentAt: time.Now().Format(time.RFC3339),
		Items: make([]webhookItem, 0, len(m.Items)),
	}
	for _, it:= range m.Items {
		wi:= webhookItem{
			FindingID: it.FindingID,
			Name: it.Name,
			VulnClass: it.VulnClass,
			Severity: it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary: it.Summary,
			Assets: append([]string{}, it.Assets...),
			DetailURL: it.DetailURL,
			FromStatus: it.FromStatus,
			ToStatus: it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
