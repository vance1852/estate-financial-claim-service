package ids

import (
	"strings"
	"testing"
)

func TestCryptoGeneratorProducesUniquePrefixedIDs(t *testing.T) {
	generator := Crypto{}
	seen := make(map[string]bool)
	for index := 0; index < 100; index++ {
		id, err := generator.New("case")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "case_") || len(id) != len("case_")+24 {
			t.Fatalf("unexpected id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestGeneratorsRejectInvalidPrefixes(t *testing.T) {
	for _, prefix := range []string{"", "bad prefix", "bad/path", `bad\path`} {
		if _, err := (Crypto{}).New(prefix); err == nil {
			t.Errorf("prefix %q unexpectedly accepted", prefix)
		}
	}
	sequence := &Sequence{}
	if _, err := sequence.New(""); err == nil {
		t.Fatal("empty sequence prefix unexpectedly accepted")
	}
}

func TestSequenceGeneratorIsDeterministic(t *testing.T) {
	sequence := &Sequence{Next: 40}
	first, err := sequence.New("job")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sequence.New("job")
	if err != nil {
		t.Fatal(err)
	}
	if first != "job_000041" || second != "job_000042" {
		t.Fatalf("sequence = %q, %q", first, second)
	}
}
