package imageproc

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"

	// Register decoders beyond the stdlib trio so sources in these formats
	// can be optimized too. Output is always jpeg/png/gif.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Result is a finished transformation.
type Result struct {
	Data        []byte
	ContentType string
}

// ErrUnsupported wraps decode failures so callers can answer 415 instead
// of a generic error.
type ErrUnsupported struct{ err error }

func (e ErrUnsupported) Error() string { return "unsupported image: " + e.err.Error() }

// Transform decodes src, applies p and re-encodes. Metadata (EXIF, color
// profiles) is dropped by re-encoding, which alone often shrinks files
// substantially.
//
// Animated GIFs are passed through untouched: transforming them would
// either freeze the animation or require re-encoding every frame, so the
// original bytes are returned as-is.
func Transform(src []byte, p Params) (*Result, error) {
	cfg, srcFormat, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, ErrUnsupported{err}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxSrcPixels {
		return nil, fmt.Errorf("source dimensions %dx%d exceed limits", cfg.Width, cfg.Height)
	}

	if srcFormat == "gif" && isAnimated(src) {
		return &Result{Data: src, ContentType: "image/gif"}, nil
	}

	decoded, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, ErrUnsupported{err}
	}

	img := toNRGBA(decoded)
	img = resize(img, p)
	if p.FlipH || p.FlipV {
		img = flip(img, p.FlipH, p.FlipV)
	}
	if p.Gray {
		grayscale(img)
	}
	if p.Blur > 0 {
		boxBlur(img, int(p.Blur+0.5))
	}
	return encode(img, p)
}

func isAnimated(src []byte) bool {
	g, err := gif.DecodeAll(bytes.NewReader(src))
	return err == nil && len(g.Image) > 1
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// defaultMaxDim caps the longest side when the caller requests no explicit
// width or height. Without it, "just recompress" on an untouched camera or
// stock-photo original (routinely 6000px+, 20+ megapixels) barely shrinks
// at all: JPEG size at that resolution is dominated by pixel count, not the
// quality setting, so a quality-only re-encode can still come back several
// megabytes. Every mainstream image CDN applies a default cap for exactly
// this reason. An explicit w= or h= always overrides it.
const defaultMaxDim = 2048

// resize maps img into the box requested by p. Zero width and height means
// scale down to defaultMaxDim on the longest side, if the source is larger.
func resize(img *image.NRGBA, p Params) *image.NRGBA {
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	reqW := scaleDim(p.Width, p.DPR)
	reqH := scaleDim(p.Height, p.DPR)
	if reqW == 0 && reqH == 0 {
		if sw <= defaultMaxDim && sh <= defaultMaxDim {
			return img
		}
		if sw >= sh {
			reqW = defaultMaxDim
		} else {
			reqH = defaultMaxDim
		}
	}
	// Remember which dimensions the caller actually asked for: the ratio
	// below must come from those, not from a derived (integer-rounded)
	// counterpart, or w=400 can yield a 399-wide image.
	askedW, askedH := reqW, reqH

	// A single dimension keeps the aspect ratio.
	if reqW == 0 {
		reqW = max(1, sw*reqH/sh)
	}
	if reqH == 0 {
		reqH = max(1, sh*reqW/sw)
	}

	switch p.Fit {
	case FitFill:
		return scaleTo(img, img.Bounds(), reqW, reqH)
	case FitCover:
		// Crop the source to the target aspect ratio, centered, then
		// scale the crop directly into the destination.
		cropW, cropH := sw, sh
		if sw*reqH > sh*reqW {
			cropW = max(1, sh*reqW/reqH)
		} else {
			cropH = max(1, sw*reqH/reqW)
		}
		x0 := img.Bounds().Min.X + (sw-cropW)/2
		y0 := img.Bounds().Min.Y + (sh-cropH)/2
		return scaleTo(img, image.Rect(x0, y0, x0+cropW, y0+cropH), reqW, reqH)
	default: // FitScaleDown, FitContain: fit inside the box
		var ratio float64
		switch {
		case askedW > 0 && askedH > 0:
			ratio = math.Min(float64(askedW)/float64(sw), float64(askedH)/float64(sh))
		case askedW > 0:
			ratio = float64(askedW) / float64(sw)
		default:
			ratio = float64(askedH) / float64(sh)
		}
		if p.Fit == FitScaleDown && ratio >= 1 {
			return img
		}
		tw := max(1, int(float64(sw)*ratio+0.5))
		th := max(1, int(float64(sh)*ratio+0.5))
		return scaleTo(img, img.Bounds(), tw, th)
	}
}

func scaleDim(dim int, dpr float64) int {
	if dim == 0 {
		return 0
	}
	scaled := int(float64(dim)*dpr + 0.5)
	return min(scaled, MaxDim)
}

func scaleTo(img *image.NRGBA, srcRect image.Rectangle, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, srcRect, draw.Src, nil)
	return dst
}

func flip(img *image.NRGBA, horizontal, vertical bool) *image.NRGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := y
		if vertical {
			sy = h - 1 - y
		}
		for x := 0; x < w; x++ {
			sx := x
			if horizontal {
				sx = w - 1 - x
			}
			si := img.PixOffset(b.Min.X+sx, b.Min.Y+sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return dst
}

// grayscale converts in place using the Rec. 601 luma weights, preserving
// alpha.
func grayscale(img *image.NRGBA) {
	pix := img.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		l := uint8((299*int(pix[i]) + 587*int(pix[i+1]) + 114*int(pix[i+2])) / 1000)
		pix[i], pix[i+1], pix[i+2] = l, l, l
	}
}

// boxBlur applies two horizontal+vertical box-blur passes with the given
// radius - a close, cheap approximation of a gaussian blur. All four
// channels are blurred with a sliding window, so each pass is O(w*h)
// regardless of radius.
func boxBlur(img *image.NRGBA, radius int) {
	if radius < 1 {
		return
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	for pass := 0; pass < 2; pass++ {
		blurAxis(img.Pix, w, h, radius, true)
		blurAxis(img.Pix, w, h, radius, false)
	}
}

func blurAxis(pix []byte, w, h, radius int, horizontal bool) {
	outer, inner := h, w
	if !horizontal {
		outer, inner = w, h
	}
	idx := func(o, i int) int {
		if horizontal {
			return (o*w + i) * 4
		}
		return (i*w + o) * 4
	}
	line := make([]byte, inner*4)
	window := 2*radius + 1
	for o := 0; o < outer; o++ {
		for i := 0; i < inner; i++ {
			copy(line[i*4:i*4+4], pix[idx(o, i):idx(o, i)+4])
		}
		var sum [4]int
		for i := -radius; i <= radius; i++ {
			ci := clampInt(i, 0, inner-1)
			for c := 0; c < 4; c++ {
				sum[c] += int(line[ci*4+c])
			}
		}
		for i := 0; i < inner; i++ {
			base := idx(o, i)
			for c := 0; c < 4; c++ {
				pix[base+c] = uint8(sum[c] / window)
			}
			enter := clampInt(i+radius+1, 0, inner-1)
			exitI := clampInt(i-radius, 0, inner-1)
			for c := 0; c < 4; c++ {
				sum[c] += int(line[enter*4+c]) - int(line[exitI*4+c])
			}
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func encode(img *image.NRGBA, p Params) (*Result, error) {
	format := p.Format
	if format == FormatAuto {
		if hasAlpha(img) {
			format = FormatPNG
		} else {
			format = FormatJPEG
		}
	}
	var buf bytes.Buffer
	switch format {
	case FormatJPEG:
		// JPEG has no alpha channel: composite transparent images over
		// white instead of leaking undefined color values.
		flat := img
		if hasAlpha(img) {
			flat = image.NewNRGBA(img.Bounds())
			draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
			draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
		}
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: p.Quality}); err != nil {
			return nil, err
		}
		return &Result{Data: buf.Bytes(), ContentType: "image/jpeg"}, nil
	case FormatPNG:
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
		return &Result{Data: buf.Bytes(), ContentType: "image/png"}, nil
	case FormatGIF:
		if err := gif.Encode(&buf, img, &gif.Options{NumColors: 256}); err != nil {
			return nil, err
		}
		return &Result{Data: buf.Bytes(), ContentType: "image/gif"}, nil
	}
	return nil, fmt.Errorf("unhandled format %d", format)
}

func hasAlpha(img *image.NRGBA) bool {
	pix := img.Pix
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0xff {
			return true
		}
	}
	return false
}
