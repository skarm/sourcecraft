package main

import "testing"

func TestPinnedSpecificationMatchesGeneratedSDK(t *testing.T) {
	if err := run("../../api/sourcecraft.swagger.json", "../..", true); err != nil {
		t.Fatal(err)
	}
	if err := run("../../api/appsec.openapi.json", "../..", true); err != nil {
		t.Fatal(err)
	}
}
