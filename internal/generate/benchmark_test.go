package main

import (
	"os"
	"testing"
)

func BenchmarkGenerate(b *testing.B) {
	data, err := os.ReadFile("../../api/sourcecraft.swagger.json")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		files, err := generate(data)
		if err != nil || len(files) != 2 {
			b.Fatalf("generate: %v", err)
		}
	}
}

func BenchmarkGenerateAppSec(b *testing.B) {
	data, err := os.ReadFile("../../api/appsec.openapi.json")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		files, err := generate(data)
		if err != nil || len(files) != 2 {
			b.Fatalf("generate AppSec: %v", err)
		}
	}
}
