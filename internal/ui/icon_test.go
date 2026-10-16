package ui

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconIsATemplateImage(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(cloudTemplate))
	if err != nil {
		t.Fatalf("the embedded icon is not a valid PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 20 || b.Dy() < 12 {
		t.Errorf("icon is %dx%d, too small for the menu bar at 2x", b.Dx(), b.Dy())
	}

	// A template image is drawn in the alpha channel only: transparent corners, opaque centre,
	// and nothing but black.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("the corner should be transparent")
	}
	r, g, bl, a := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	if a != 0xffff || r != 0 || g != 0 || bl != 0 {
		t.Errorf("the centre should be opaque black, got rgba(%d,%d,%d,%d)", r, g, bl, a)
	}
}
