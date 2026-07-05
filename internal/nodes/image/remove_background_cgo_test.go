//go:build cgo

package image

import (
	"image"
	"testing"
)

func TestFlattenOntoColor(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))

	if _, err := flattenOntoColor(img, "#ffffff"); err != nil {
		t.Fatalf("6-digit hex: unexpected error: %v", err)
	}
	if _, err := flattenOntoColor(img, "fff"); err != nil {
		t.Fatalf("3-digit hex: unexpected error: %v", err)
	}
	if _, err := flattenOntoColor(img, "zzzzzz"); err == nil {
		t.Fatal("invalid hex digits: expected error, got nil")
	}
	if _, err := flattenOntoColor(img, "ff"); err == nil {
		t.Fatal("wrong length: expected error, got nil")
	}
}
