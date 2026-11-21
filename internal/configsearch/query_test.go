package configsearch

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse_FieldTermsAndFreeText(t *testing.T) {
	q, err := Parse("image:node:18 env:DATABASE_URL port:8080 nginx")
	if err != nil {
		t.Fatal(err)
	}
	want := []Term{
		{Field: "image", Value: "node:18"},
		{Field: "env", Value: "DATABASE_URL"},
		{Field: "port", Value: "8080"},
		{Field: "", Value: "nginx"},
	}
	if !reflect.DeepEqual(q.Terms, want) {
		t.Fatalf("terms = %+v, want %+v", q.Terms, want)
	}
}

func TestParse_UnknownPrefixIsFreeText(t *testing.T) {
	for _, in := range []string{"nginx:1.27", "foo:bar", "node:18"} {
		q, err := Parse(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		want := []Term{{Value: in}}
		if !reflect.DeepEqual(q.Terms, want) {
			t.Errorf("%q: terms = %+v, want %+v", in, q.Terms, want)
		}
	}
}

func TestParse_AliasesNegationAndQuotes(t *testing.T) {
	cases := map[string]Term{
		"-secret:API_KEY": {Field: "secret", Value: "API_KEY", Negate: true},
		"log:/ecs/core":   {Field: "loggroup", Value: "/ecs/core"},
		"log-group:x1":    {Field: "loggroup", Value: "x1"},
		"task-role:abc":   {Field: "taskrole", Value: "abc"},
		"exec-role:abc":   {Field: "execrole", Value: "abc"},
		"executionrole:a": {Field: "execrole", Value: "a"},
		"secrets:DB":      {Field: "secret", Value: "DB"},
		"ports:8080":      {Field: "port", Value: "8080"},
		`env:"PORT"`:      {Field: "env", Value: "PORT", Exact: true},
		"ENV:Path":        {Field: "env", Value: "Path"},
	}
	for in, want := range cases {
		q, err := Parse(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if len(q.Terms) != 1 || q.Terms[0] != want {
			t.Errorf("%q: terms = %+v, want %+v", in, q.Terms, want)
		}
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct{ in, msg string }{
		{strings.Repeat("a", 501), "Searches can be up to 500 characters."},
		{"aa bb cc dd ee ff gg hh ii jj kk", "Use at most 10 search terms."},
		{"a", "Search terms need at least 2 characters."},
		{"port:http", "port: needs a number from 1 to 65535."},
		{"port:70000", "port: needs a number from 1 to 65535."},
		{"port:0", "port: needs a number from 1 to 65535."},
		{"env:", "Add a value after env:"},
		{"log:", "Add a value after loggroup:"},
		{"task-role:", "Add a value after taskrole:"},
		{`env:"PORT`, `Close the quote in env:"PORT.`},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("%q: err = %v, want *ParseError", c.in, err)
		}
		if pe.Message != c.msg {
			t.Errorf("%q: message = %q, want %q", c.in, pe.Message, c.msg)
		}
	}
}

func TestParse_EmptyQuery(t *testing.T) {
	q, err := Parse("   ")
	if err != nil || !q.Empty() {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}

func TestQueryString_RoundTrips(t *testing.T) {
	for _, in := range []string{
		"image:node:18 env:DATABASE_URL port:8080 nginx",
		"nginx:1.27 foo:bar",
		"-secret:API_KEY log:/ecs/core env:\"PORT\"",
		"  role:arn   taskrole:x ",
	} {
		q, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Parse(q.String())
		if err != nil {
			t.Fatalf("%q -> %q: %v", in, q.String(), err)
		}
		if !reflect.DeepEqual(q, again) {
			t.Errorf("%q -> %q: %+v != %+v", in, q.String(), q, again)
		}
	}
}
