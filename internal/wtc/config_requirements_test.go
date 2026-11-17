package wtc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func requirementsFixture(t *testing.T) (RequirementSchema, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/config_requirements/web.env.example")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := ParseConfigRequirements("web.env.example", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return schema, string(data)
}
func requirementItem(t *testing.T, items []RequirementItem, name string) RequirementItem {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("missing inventory item %s", name)
	return RequirementItem{}
}
func TestConfigRequirementsConnectionMatrix(t *testing.T) {
	schema, _ := requirementsFixture(t)
	for _, test := range []struct{ name, selector, token, app, state string }{
		{"web only remote", "remote", "", "applicable", "missing"},
		{"local selected", "local", "", "inapplicable", "not-required"},
		{"API checkout with remote selected", "remote", "", "applicable", "missing"},
		{"unresolved selection", "", "", "unknown", "unverified"},
		{"invalid selection", "garbage", "", "unknown", "unverified"},
		{"placeholder", "remote", "replace-me", "applicable", "placeholder"},
		{"present is not authentication proof", "remote", "synthetic-canary", "applicable", "present"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			if test.selector != "" {
				values["WEB_CONNECTION"] = test.selector
			}
			if test.token != "" {
				values["WEB_TEST_TOKEN"] = test.token
			}
			inputs := []RequirementInput{{Source: schema.Sources.Files[0], Values: values, State: "inspected"}, {Source: schema.Sources.Files[1], State: "missing"}}
			items := ConfigRequirementsInventory(schema, inputs, map[string]string{}, nil)
			got := requirementItem(t, items, "WEB_TEST_TOKEN")
			if got.Applicability != test.app || got.State != test.state {
				t.Fatalf("got %s/%s", got.Applicability, got.State)
			}
			if requirementItem(t, items, "certs/test.pem").State != "unverified" {
				t.Fatal("file readiness invented")
			}
			if requirementItem(t, items, "WEB_TRACE").Required != "optional" {
				t.Fatal("lost optional classification")
			}
			data, _ := json.Marshal(items)
			if strings.Contains(string(data), "synthetic-canary") {
				t.Fatal("value leaked")
			}
		})
	}
}
func TestConfigRequirementsPrecedenceAndOwnedInputs(t *testing.T) {
	schema, _ := requirementsFixture(t)
	inputs := []RequirementInput{
		{Source: schema.Sources.Files[1], Values: map[string]string{"WEB_CONNECTION": "remote", "WEB_TEST_TOKEN": ""}, State: "inspected"},
		{Source: schema.Sources.Files[0], Values: map[string]string{"WEB_CONNECTION": "local", "WEB_TEST_TOKEN": "synthetic-canary", "TYPO": "value-canary"}, State: "inspected"},
	}
	items := ConfigRequirementsInventory(schema, inputs, map[string]string{"HOME": "ambient-canary", "WEB_TEST_UNKNOWN": "value-canary"}, map[string]string{"certs/test.pem": "present"})
	token := requirementItem(t, items, "WEB_TEST_TOKEN")
	if token.State != "empty" || token.Scope != "collection" || token.Source != "web.local.env" {
		t.Fatalf("empty override ignored: %+v", token)
	}
	for _, n := range []string{"TYPO", "WEB_TEST_UNKNOWN"} {
		if requirementItem(t, items, n).State != "undocumented" {
			t.Fatal("unknown owned input lost")
		}
	}
	if requirementItem(t, items, "certs/test.pem").State != "present" {
		t.Fatal("presence lost")
	}
	for _, item := range items {
		if item.Name == "HOME" {
			t.Fatal("ambient global input scanned")
		}
	}
	data, _ := json.Marshal(items)
	for _, s := range []string{"synthetic-canary", "ambient-canary", "value-canary"} {
		if strings.Contains(string(data), s) {
			t.Fatal("value leak")
		}
	}
	// Process precedence only applies to explicitly owned names.
	items = ConfigRequirementsInventory(schema, inputs, map[string]string{"WEB_CONNECTION": "local", "WEB_TRACE": "true"}, nil)
	if requirementItem(t, items, "WEB_TEST_TOKEN").State != "not-required" {
		t.Fatal("process selector not applied")
	}
	if requirementItem(t, items, "WEB_TRACE").State != "missing" {
		t.Fatal("undeclared environment ownership accepted")
	}
}
func TestConfigRequirementsUnknownSources(t *testing.T) {
	schema, _ := requirementsFixture(t)
	inputs := []RequirementInput{{Source: schema.Sources.Files[0], Values: map[string]string{"WEB_CONNECTION": "remote", "WEB_TEST_TOKEN": "present"}, State: "inspected"}}
	items := ConfigRequirementsInventory(schema, inputs, map[string]string{}, nil)
	if requirementItem(t, items, "WEB_TEST_TOKEN").State != "unverified" {
		t.Fatal("unknown override passed")
	}
	inputs = append(inputs, RequirementInput{Source: schema.Sources.Files[1], State: "missing"})
	items = ConfigRequirementsInventory(schema, inputs, nil, nil)
	if requirementItem(t, items, "WEB_TEST_TOKEN").State != "unverified" {
		t.Fatal("uninspected process input passed")
	}
}
func TestConfigRequirementsGrammarRejectsAmbiguity(t *testing.T) {
	_, fixture := requirementsFixture(t)
	for _, test := range []struct{ name, body string }{
		{"unknown version", strings.Replace(fixture, "requirements v1", "requirements v2", 1)},
		{"unknown field", strings.Replace(fixture, `"kind":"secret"`, `"kind":"secret","unexpected":"secret-canary"`, 1)},
		{"duplicate JSON key", strings.Replace(fixture, `"kind":"secret"`, `"kind":"secret","kind":"config"`, 1)},
		{"undeclared example key", fixture + "UNDECLARED=secret-canary\n"},
		{"duplicate requirement", fixture + strings.Split(fixture, "# taba:require ")[1]},
		{"source escape", strings.Replace(fixture, `"path":"web.env"`, `"path":"../web.env"`, 1)},
		{"unsupported source", strings.Replace(fixture, `"format":"dotenv"`, `"format":"yaml"`, 1)},
		{"secret default", strings.Replace(fixture, `"kind":"config"`, `"kind":"secret"`, 1)},
		{"missing condition", strings.Replace(fixture, `"required":"required"`, `"required":"conditional"`, 1)},
		{"unknown selector", strings.Replace(fixture, `"key":"WEB_CONNECTION"`, `"key":"UNDECLARED"`, 1)},
		{"secret selector", strings.Replace(fixture, `"key":"WEB_CONNECTION"`, `"key":"WEB_TEST_TOKEN"`, 1)},
		{"unsupported annotation", fixture + "# taba:execute secret-canary\n"},
		{"oversized", strings.Repeat("secret-canary", 30000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseConfigRequirements("web.env.example", strings.NewReader(test.body))
			if err == nil {
				t.Fatal("invalid declaration passed")
			}
			if strings.Contains(err.Error(), "secret-canary") {
				t.Fatal("diagnostic leaked source")
			}
		})
	}
	for _, filename := range []string{"web.yaml.example", "../web.env.example", ".example", "web.env"} {
		if _, err := ParseConfigRequirements(filename, strings.NewReader(fixture)); err == nil {
			t.Fatalf("unsupported path %s passed", filename)
		}
	}
}
func TestConfigRequirementsDotfilesAndNoExampleDefaults(t *testing.T) {
	_, fixture := requirementsFixture(t)
	for _, name := range []string{".env", ".env.local"} {
		body := strings.ReplaceAll(fixture, "web.env", name)
		schema, err := ParseConfigRequirements(name+".example", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var inputs []RequirementInput
		for _, src := range schema.Sources.Files {
			inputs = append(inputs, RequirementInput{Source: src, State: "missing"})
		}
		items := ConfigRequirementsInventory(schema, inputs, map[string]string{}, nil)
		if requirementItem(t, items, "WEB_CONNECTION").State != "missing" {
			t.Fatal("example default used as actual input")
		}
	}
}
func TestConfigRequirementsInputIsLiteralAndRedacted(t *testing.T) {
	marker := "$(touch /synthetic-never-executed)"
	values, err := ParseRequirementInput(strings.NewReader("KEY='" + marker + "'\nEMPTY=\n# comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	if values["KEY"] != marker || values["EMPTY"] != "" {
		t.Fatal("literal value changed")
	}
	for _, s := range []string{"KEY=secret-canary\nKEY=duplicate\n", "BAD secret-canary\n", "KEY='secret-canary\n", strings.Repeat("x", requirementMaxLine+1), strings.Repeat("x", requirementMaxBytes+1)} {
		values, err = ParseRequirementInput(strings.NewReader(s))
		if err == nil || values != nil {
			t.Fatal("malformed input partly accepted")
		}
		if strings.Contains(err.Error(), "secret-canary") {
			t.Fatal("error leaked input")
		}
	}
}
