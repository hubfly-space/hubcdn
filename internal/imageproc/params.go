// Package imageproc implements hubCDN's image optimization pipeline: it
// parses transformation options from the /img/ URL segment and applies them
// to raw image bytes. Everything is pure Go (stdlib + golang.org/x/image),
// so the pipeline runs unchanged in the static distroless container.
package imageproc

import (
	"fmt"
	"strconv"
	"strings"
)

// Hard limits protecting the node from decompression bombs and absurd
// output sizes.
const (
	// MaxDim caps requested output width/height (after dpr).
	MaxDim = 4096
	// maxSrcPixels rejects sources larger than ~50 megapixels before the
	// full decode allocates memory for them.
	maxSrcPixels = 50 << 20
)

// Fit controls how an image is mapped into a requested width×height box.
type Fit int

const (
	// FitScaleDown fits within the box but never upscales (default).
	FitScaleDown Fit = iota
	// FitContain fits within the box, upscaling if needed.
	FitContain
	// FitCover fills the box completely, cropping the center.
	FitCover
	// FitFill matches the box exactly, distorting aspect ratio.
	FitFill
)

// Format selects the output encoding.
type Format int

const (
	// FormatAuto picks JPEG for opaque images and PNG when the image has
	// transparency.
	FormatAuto Format = iota
	FormatJPEG
	FormatPNG
	FormatGIF
)

// Params are the parsed transformation options for one request.
type Params struct {
	Width   int
	Height  int
	DPR     float64
	Fit     Fit
	Quality int
	Format  Format
	Gray    bool
	Blur    float64
	FlipH   bool
	FlipV   bool
}

// Defaults returns the params applied when no options are given: re-encode
// at quality 80 with metadata stripped, no resizing.
func Defaults() Params {
	return Params{DPR: 1, Fit: FitScaleDown, Quality: 80, Format: FormatAuto}
}

// Parse reads a comma-separated option string like
//
//	"w=800,h=600,fit=cover,q=75,f=jpeg,gray=1,blur=4"
//
// "_" (or empty) means defaults only. Unknown keys and out-of-range values
// are errors: these URLs are an API surface and failing fast beats silently
// serving the wrong rendition.
func Parse(s string) (Params, error) {
	p := Defaults()
	if s == "" || s == "_" {
		return p, nil
	}
	for _, pair := range strings.Split(s, ",") {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			return p, fmt.Errorf("option %q is not key=value", pair)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.ToLower(strings.TrimSpace(val))
		var err error
		switch key {
		case "w", "width":
			p.Width, err = parseDim(val)
		case "h", "height":
			p.Height, err = parseDim(val)
		case "dpr":
			p.DPR, err = parseRange(val, 0.5, 3)
		case "fit":
			p.Fit, err = parseFit(val)
		case "q", "quality":
			var n int
			n, err = strconv.Atoi(val)
			if err == nil && (n < 1 || n > 100) {
				err = fmt.Errorf("out of range 1-100")
			}
			p.Quality = n
		case "f", "format":
			p.Format, err = parseFormat(val)
		case "gray", "grayscale":
			p.Gray, err = parseBool(val)
		case "blur":
			p.Blur, err = parseRange(val, 0, 50)
		case "flip":
			switch val {
			case "h":
				p.FlipH = true
			case "v":
				p.FlipV = true
			case "hv", "vh":
				p.FlipH, p.FlipV = true, true
			default:
				err = fmt.Errorf("must be h, v or hv")
			}
		default:
			return p, fmt.Errorf("unknown option %q", key)
		}
		if err != nil {
			return p, fmt.Errorf("option %q: %v", key, err)
		}
	}
	return p, nil
}

func parseDim(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > MaxDim {
		return 0, fmt.Errorf("out of range 1-%d", MaxDim)
	}
	return n, nil
}

func parseRange(v string, lo, hi float64) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, err
	}
	if f < lo || f > hi {
		return 0, fmt.Errorf("out of range %g-%g", lo, hi)
	}
	return f, nil
}

func parseFit(v string) (Fit, error) {
	switch v {
	case "scaledown", "scale-down":
		return FitScaleDown, nil
	case "contain":
		return FitContain, nil
	case "cover":
		return FitCover, nil
	case "fill":
		return FitFill, nil
	}
	return 0, fmt.Errorf("must be scaledown, contain, cover or fill")
}

func parseFormat(v string) (Format, error) {
	switch v {
	case "auto":
		return FormatAuto, nil
	case "jpeg", "jpg":
		return FormatJPEG, nil
	case "png":
		return FormatPNG, nil
	case "gif":
		return FormatGIF, nil
	}
	return 0, fmt.Errorf("must be auto, jpeg, png or gif")
}

func parseBool(v string) (bool, error) {
	switch v {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	}
	return false, fmt.Errorf("must be on or off")
}
