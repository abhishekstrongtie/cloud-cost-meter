//go:build ignore

// gen.go draws the CostMeter app icon: a white cloud on a rounded blue square, at every size
// macOS wants. It is not part of the build; run it only to change the icon:
//
//	go run packaging/icon/gen.go packaging/icon/AppIcon.iconset
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type circle struct{ x, y, r float64 }

// The cloud in a 44 x 44 box, the same shape as the menu bar icon.
var (
	bumps       = []circle{{11.5, 27, 6}, {20.5, 19.5, 9.5}, {31.5, 24.5, 8.5}}
	cloudCenter = [2]float64{22.75, 21.5} // middle of the cloud's bounds
	cloudWidth  = 34.5                    // 40 - 5.5
)

func inCloud(x, y float64) bool {
	for _, c := range bumps {
		if math.Hypot(x-c.x, y-c.y) <= c.r {
			return true
		}
	}
	return x >= 11.5 && x <= 31.5 && y >= 27 && y <= 33
}

// inBody is a "squircle": the rounded square of the macOS icon grid (824 of 1024 across).
func inBody(nx, ny float64) bool {
	const half, n = 824.0 / 1024 / 2, 5.0
	return math.Pow(math.Abs(nx-0.5)/half, n)+math.Pow(math.Abs(ny-0.5)/half, n) <= 1
}

func lerp(a, b uint8, t float64) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }

func draw(size int) *image.NRGBA {
	const ss = 4 // samples per axis, for smooth edges
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 0.60 * float64(size) / cloudWidth // the cloud spans 60% of the icon
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var body, cloud int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if !inBody(x/float64(size), y/float64(size)) {
						continue
					}
					body++
					cx := cloudCenter[0] + (x-float64(size)/2)/scale
					cy := cloudCenter[1] + (y-float64(size)*0.5)/scale
					if inCloud(cx, cy) {
						cloud++
					}
				}
			}
			if body == 0 {
				continue
			}
			t := float64(py) / float64(size) // top (light blue) to bottom (deep blue)
			bg := color.NRGBA{lerp(0x4C, 0x0A, t), lerp(0xA0, 0x4B, t), lerp(0xFF, 0xC9, t), 255}
			w := float64(cloud) / float64(body) // how much of the covered area is cloud
			out := color.NRGBA{
				R: lerp(bg.R, 255, w), G: lerp(bg.G, 255, w), B: lerp(bg.B, 255, w),
				A: uint8(255 * body / (ss * ss)),
			}
			img.SetNRGBA(px, py, out)
		}
	}
	return img
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run gen.go <AppIcon.iconset directory>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	// iconutil wants each point size at 1x and 2x.
	files := map[string]int{
		"icon_16x16.png": 16, "icon_16x16@2x.png": 32,
		"icon_32x32.png": 32, "icon_32x32@2x.png": 64,
		"icon_128x128.png": 128, "icon_128x128@2x.png": 256,
		"icon_256x256.png": 256, "icon_256x256@2x.png": 512,
		"icon_512x512.png": 512, "icon_512x512@2x.png": 1024,
	}
	drawn := map[int]*image.NRGBA{}
	for name, size := range files {
		if drawn[size] == nil {
			drawn[size] = draw(size)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, drawn[size]); err != nil {
			panic(err)
		}
		f.Close()
	}
	fmt.Println("wrote", len(files), "files to", dir)
}
