package main

import (
	"bytes"
	"fmt"
	"go/token"
	"regexp"
	"strings"
	"unicode"
)

var (
	camelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

func goName(name string) string {
	name = strings.TrimPrefix(name, "v1.")
	name = acronymBoundary.ReplaceAllString(name, "${1}_${2}")
	name = camelBoundary.ReplaceAllString(name, "${1}_${2}")
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })

	var b strings.Builder

	for _, part := range parts {
		if initialism := initialismName(strings.ToLower(part)); initialism != "" {
			b.WriteString(initialism)
		} else {
			runes := []rune(part)
			runes[0] = unicode.ToUpper(runes[0])
			b.WriteString(string(runes))
		}
	}

	result := b.String()
	if result == "" {
		return "Value"
	}

	if !unicode.IsUpper([]rune(result)[0]) {
		return "Value" + result
	}

	return result
}

// encoding/json only honors a restricted set of struct-tag names. Reject names
// it would replace with the Go field name or split into tag options.
func jsonFieldName(name string) string {
	if name == "" {
		panic("empty JSON field name is unsupported")
	}

	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r) {
			panic(fmt.Sprintf("JSON field name %q cannot be represented by a struct tag", name))
		}
	}

	return name
}

func variableName(name string) string {
	parts := strings.Split(name, "_")

	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))

	for _, part := range parts[1:] {
		b.WriteString(goName(part))
	}

	return safeVariableName(b.String())
}

func safeVariableName(name string) string {
	runes := []rune(name)
	if len(runes) == 0 {
		panic("empty parameter identifier")
	}

	runes[0] = unicode.ToLower(runes[0])

	name = string(runes)
	if token.IsKeyword(name) {
		return name + "Value"
	}

	if !token.IsIdentifier(name) {
		panic("invalid parameter identifier: " + name)
	}

	if strings.Contains(sdkRuntimeNames, "\n"+name+"\n") {
		return name + "Value"
	}
	// Parameters must not shadow generated locals, imported packages or the
	// predeclared identifiers used by method bodies.
	switch name {
	case "c", "ctx", "err", namePath, nameBody, nameFile, "opts", "fields", nameRequestBody,
		"context", "json", "fmt", "http", "url", "strconv", "strings", "time",
		exprNil, "true", "false", typeString, typeBool, typeByte, typeInt32, typeInt64, typeUint64, "float32", typeFloat64, "any", "append":
		return name + "Value"
	}

	return name
}

func comment(b *bytes.Buffer, text string) {
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		fmt.Fprintln(b, "// "+strings.TrimSpace(line))
	}
}

func initialismName(name string) string {
	switch name {
	case "ai":
		return "AI"
	case "api":
		return "API"
	case "ci":
		return "CI"
	case "cd":
		return "CD"
	case "cpu":
		return "CPU"
	case "dns":
		return "DNS"
	case "http":
		return "HTTP"
	case "https":
		return "HTTPS"
	case "id":
		return "ID"
	case "ids":
		return "IDs"
	case "iam":
		return "IAM"
	case "ip":
		return "IP"
	case "json":
		return "JSON"
	case "pr":
		return "PR"
	case "prs":
		return "PRs"
	case "sha":
		return "SHA"
	case "ssh":
		return "SSH"
	case "sso":
		return "SSO"
	case "ui":
		return "UI"
	case "url":
		return "URL"
	case "urls":
		return "URLs"
	case formatUUID:
		return "UUID"
	case "utc":
		return "UTC"
	}

	return ""
}
