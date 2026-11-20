package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file locks down the generic Webhook template.**Capability Boundary**.
//
// This is the only place in the entire package where 'user-provided strings are evaluated as code'; therefore, it must be clearly defined what it can do,
// What can't be done and using tests to fix these properties.——Otherwise, someone might casually add more context to the template later
// method, or add readFile in FuncMap, silently expanding the capability face while diff looks like
// It's just a harmless small function.

// TestTemplateContextHasNoMethods It is the most important one.
//
// text/template Will call export method ({{.Foo}} Can fetch fields as well as call methods). So the template context
// As long as it can contact**Any**Types with export methods equal to exposing those methods to template authors.
// The context of this feature is deliberately purely data (only export fields, zero methods).
//
// If this fails: It means someone added methods to webhookTemplateData / webhookItem.
// Before allowing something through, think clearly if that method can be used by a template to read things that should not be exposed.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v:= range []any{webhookTemplateData{}, webhookItem{}} {
		typ:= reflect.TypeOf(v)
		if n:= typ.NumMethod(); n!= 0 {
			var names []string
			for i:= 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s Exposed %d Methods (%s): text/template Can Call Them,"+
				"Equals opening up these methods' capabilities to template authors.", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal Lock down the set of functions exposed to templates.
//
// FuncMap Each additional function adds one capability. Currently only json / jsons, which serialize values.
// Convert to JSON snippet.——It cannot read files, send requests, or execute commands.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name:= range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want:= []string{"json", "jsons"}
	if!reflect.DeepEqual(got, want) {
		t.Fatalf("The template function set has changed: resulting in %v, Expectation %v. Confirm before adding a new function that it does not expand the scope of capabilities."+
			" (Cannot read/write files, cannot initiate network requests, cannot execute commands.", got, want)
	}
}

// TestTemplateCannotReachUnknownData Cover out-of-bounds access in the template:
// Accessing non-existent things must fail, not echoing anything; and failure messages should not leak internal data.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err:= renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("Accessing non-existent fields should result in an error.")
	}
	// Templates should not contain real content from the template context (vulnerability title/summary) in errors.
	for _, leak:= range []string{"SQLCompleted", "Parameter id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("Template error leaked message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently Template errors belong to configuration errors, retries will not self-heal.
// If deemed retryable, a bad template will cause each delivery to waste three rounds of backoff.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg:= map[string]any{
		"url": "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err:= (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("Template syntax errors should be blocked when saving")
	}
	// Even if bypassing validation and delivering directly, it must be judged as permanent failure rather than repeated retries.
	_, err:= (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil ||!IsPermanent(err) {
		t.Fatalf("Bad template should be judged permanent failure, got %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON Override the constraint that 'template rendering results must be valid JSON'.
// It also blocks usage like 'using a template to generate plain text to trigger other protocols'.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// Legal templates pass.
	ok:= map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err:= (webhookChannel{}).Validate(ok); err!= nil {
		t.Fatalf("Legal templates should pass validation.: %v", err)
	}
	// Must reject non-JSON content (not just forward it as-is).
	bad:= map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err:= (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil ||!IsPermanent(err) {
		t.Fatalf("Rendering non-JSON should be considered permanent failure, resulting in: %v", err)
	}
	if!strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("Error messages should indicate that it's a JSON issue. Received %v", err)
	}
}
