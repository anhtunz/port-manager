//go:build ignore

// genicons — vẽ icon của Port Manager bằng hình học thuần Go, không cần cài
// tool ngoài (repo không phụ thuộc rasterizer nào). Sinh 2 file:
//
//	assets/appicon.png            1024×1024 — logo "lưới service" (đóng gói .app)
//	internal/tray/assets/radar.png  36×36   — template image cho status item menu bar
//
// Logo vẽ đúng SVG concept đã chốt (23/10/2026): nền slate bo góc vuông vức
// kiểu macOS, 4 khối service (3 xám + 1 gradient xanh), badge xanh lá "đang chạy".
// Radar là ảnh template (đen + alpha): macOS tự đổi màu theo sáng/tối của menu bar.
//
// Chạy: make icons
package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type rgb [3]float64 // 0..1

func C(hex uint32) rgb {
	return rgb{
		float64(hex>>16&0xff) / 255,
		float64(hex>>8&0xff) / 255,
		float64(hex&0xff) / 255,
	}
}

func mix(a, b rgb, t float64) rgb {
	return rgb{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// layer: mẫu phủ (boolean tại điểm lấy mẫu) + màu (tuỳ điểm để vẽ gradient).
type layer struct {
	test  func(x, y float64) bool
	color func(x, y float64) rgb
	alpha float64
}

func fill(test func(x, y float64) bool, c rgb, alpha float64) layer {
	return layer{test, func(float64, float64) rgb { return c }, alpha}
}

func gradFill(test func(x, y float64) bool, x0, y0, span float64, c0, c1 rgb, alpha float64) layer {
	return layer{test, func(x, y float64) rgb {
		return mix(c0, c1, clamp(((x-x0)+(y-y0))/span, 0, 1))
	}, alpha}
}

// --- Hình học: signed distance, âm là ở trong ---

func sdRoundRect(px, py, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(px-cx) - (hw - r)
	qy := math.Abs(py-cy) - (hh - r)
	ax, ay := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ax, ay) + math.Min(math.Max(qx, qy), 0) - r
}

func sdCircle(px, py, cx, cy, r float64) float64 { return math.Hypot(px-cx, py-cy) - r }

func sdSegment(px, py, ax, ay, bx, by float64) float64 {
	pax, pay := px-ax, py-ay
	bax, bay := bx-ax, by-ay
	h := clamp((pax*bax+pay*bay)/(bax*bax+bay*bay), 0, 1)
	return math.Hypot(pax-bax*h, pay-bay*h)
}

// overAll: phủ từng layer kiểu "src over dst" (alpha thẳng), trả RGBA tại điểm.
func overAll(layers []layer, x, y float64) (r, g, b, a float64) {
	for _, l := range layers {
		if !l.test(x, y) {
			continue
		}
		sc := l.color(x, y)
		sa := l.alpha
		oa := sa + a*(1-sa)
		if oa == 0 {
			r, g, b, a = 0, 0, 0, 0
			continue
		}
		r = (sc[0]*sa + r*a*(1-sa)) / oa
		g = (sc[1]*sa + g*a*(1-sa)) / oa
		b = (sc[2]*sa + b*a*(1-sa)) / oa
		a = oa
	}
	return r, g, b, a
}

// render: vẽ "space" đơn vị logic ra ảnh out×out, lấy ss mẫu con mỗi chiều
// mỗi pixel (supersampling) để viền mượt.
func render(space, out, ss int, layers []layer) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, out, out))
	scale := float64(out) / float64(space)
	n := float64(ss * ss)
	for py := 0; py < out; py++ {
		for px := 0; px < out; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/float64(ss)) / scale
					y := (float64(py) + (float64(sy)+0.5)/float64(ss)) / scale
					cr, cg, cb, ca := overAll(layers, x, y)
					r += cr * ca
					g += cg * ca
					b += cb * ca
					a += ca
				}
			}
			r, g, b, a = r/n, g/n, b/n, a/n
			i := img.PixOffset(px, py)
			img.Pix[i+0] = quant(r, a)
			img.Pix[i+1] = quant(g, a)
			img.Pix[i+2] = quant(b, a)
			img.Pix[i+3] = quant(a, 1)
		}
	}
	return img
}

func quant(prem, a float64) uint8 {
	if a <= 0 {
		return 0
	}
	v := prem / a * 255
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// --- Logo app: "lưới service" (toạ độ 512, khớp SVG concept) ---

func appIconLayers() []layer {
	var layers []layer
	// Nền tile slate bo góc (squircle ~22.5%) + viền mảnh.
	tile := func(x, y float64) bool { return sdRoundRect(x, y, 256, 256, 240, 240, 116) < 0 }
	layers = append(layers, gradFill(tile, 16, 16, 960, C(0x22304a), C(0x0f172a), 1))
	border := func(x, y float64) bool {
		return math.Abs(sdRoundRect(x, y, 256, 256, 240, 240, 116)) < 2
	}
	layers = append(layers, fill(border, C(0x94a3b8), 0.35))
	// 4 khối service 108×108: góc trái trên gradient xanh, còn lại xám.
	sq := func(cx, cy float64) func(x, y float64) bool {
		return func(x, y float64) bool { return sdRoundRect(x, y, cx, cy, 54, 54, 26) < 0 }
	}
	layers = append(layers, gradFill(sq(190, 190), 136, 136, 216, C(0x3b82f6), C(0x22d3ee), 1))
	layers = append(layers, fill(sq(322, 190), C(0x334155), 1))
	layers = append(layers, fill(sq(190, 322), C(0x334155), 1))
	layers = append(layers, fill(sq(322, 322), C(0x334155), 1))
	// Badge xanh lá "đang chạy" đè góc khối gradient, viền nền slate.
	badge := func(x, y float64) bool { return sdCircle(x, y, 252, 130, 42) < 0 }
	layers = append(layers, fill(badge, C(0x22c55e), 1))
	ring := func(x, y float64) bool { return math.Abs(sdCircle(x, y, 252, 130, 42)) < 9 }
	layers = append(layers, fill(ring, C(0x0f172a), 1))
	return layers
}

// --- Icon menu bar: radar (template đen + alpha, toạ độ 36) ---
// Cố tình tối giản 3 lớp (vòng ngoài, lát quét, chấm tâm) — icon chỉ hiển thị
// 18pt, chi tiết thừa sẽ dính thành cục mờ.

func radarLayers() []layer {
	const cx, cy, rOut = 18.0, 18.0, 11.0
	alpha := func(a float64) layer {
		return layer{color: func(float64, float64) rgb { return rgb{0, 0, 0} }, alpha: a}
	}
	atan2p := math.Atan2
	var layers []layer
	// Lát quét: quạt từ đỉnh (-90°) quét 60° theo chiều kim đồng hồ, mờ.
	layers = append(layers, func() layer {
		l := alpha(0.45)
		l.test = func(x, y float64) bool {
			d := math.Hypot(x-cx, y-cy)
			if d >= rOut-1 {
				return false
			}
			a := atan2p(y-cy, x-cx)
			return a >= -math.Pi/2 && a <= -math.Pi/3 // -90°..-60°
		}
		return l
	}())
	// Vòng quét ngoài.
	layers = append(layers, func() layer {
		l := alpha(1)
		l.test = func(x, y float64) bool {
			return math.Abs(math.Hypot(x-cx, y-cy)-rOut) < 1.6
		}
		return l
	}())
	// Chấm tâm.
	layers = append(layers, func() layer {
		l := alpha(1)
		l.test = func(x, y float64) bool { return math.Hypot(x-cx, y-cy) < 3.2 }
		return l
	}())
	return layers
}

func writePNG(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("đã vẽ", path)
}

func main() {
	writePNG("assets/appicon.png", render(512, 1024, 3, appIconLayers()))
	writePNG("internal/tray/assets/radar.png", render(36, 36, 8, radarLayers()))
}
