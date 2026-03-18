package gradual

import (
	"encoding/json"
	"os"
	"testing"
)

type hashVector struct {
	Input  string `json:"input"`
	Hash   int    `json:"hash"`
	Bucket int    `json:"bucket"`
}

type hashVectorsFile struct {
	Vectors []hashVector `json:"vectors"`
}

func loadHashVectors(t *testing.T) []hashVector {
	t.Helper()
	data, err := os.ReadFile("../gradual-sdk-spec/testdata/hash/hash-vectors.json")
	if err != nil {
		t.Fatalf("Failed to load hash vectors: %v", err)
	}
	var f hashVectorsFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("Failed to parse hash vectors: %v", err)
	}
	return f.Vectors
}

func TestHashString(t *testing.T) {
	vectors := loadHashVectors(t)
	for _, v := range vectors {
		t.Run(v.Input, func(t *testing.T) {
			got := HashString(v.Input)
			if got != v.Hash {
				t.Errorf("HashString(%q) = %d, want %d", v.Input, got, v.Hash)
			}
		})
	}
}

func TestBucketValue(t *testing.T) {
	vectors := loadHashVectors(t)
	for _, v := range vectors {
		t.Run(v.Input, func(t *testing.T) {
			got := HashString(v.Input) % 100_000
			if got != v.Bucket {
				t.Errorf("HashString(%q) %% 100000 = %d, want %d", v.Input, got, v.Bucket)
			}
		})
	}
}
