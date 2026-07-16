package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testImage builds a w×h opaque image with a red left half and blue right
// half, so crops and flips are verifiable.
func testImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.NRGBA{B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func decodeDims(t *testing.T, data []byte) (int, int, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return cfg.Width, cfg.Height, format
}

func TestParse(t *testing.T) {
	p, err := Parse("w=800,h=600,fit=cover,q=75,f=png,gray=1,blur=4,flip=hv,dpr=2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 800 || p.Height != 600 || p.Fit != FitCover || p.Quality != 75 ||
		p.Format != FormatPNG || !p.Gray || p.Blur != 4 || !p.FlipH || !p.FlipV || p.DPR != 2 {
		t.Fatalf("bad parse: %+v", p)
	}
	if _, err := Parse("_"); err != nil {
		t.Fatalf("underscore must mean defaults: %v", err)
	}
	for _, bad := range []string{"nope=1", "w=0", "w=99999", "q=101", "fit=zoom", "f=bmp", "flip=x", "w"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestResizeWidthKeepsAspect(t *testing.T) {
	src := encodePNG(t, testImage(400, 200))
	p := Defaults()
	p.Width = 100
	res, err := Transform(src, p)
	if err != nil {
		t.Fatal(err)
	}
	w, h, _ := decodeDims(t, res.Data)
	if w != 100 || h != 50 {
		t.Fatalf("got %dx%d, want 100x50", w, h)
	}
}

func TestScaleDownNeverUpscales(t *testing.T) {
	src := encodePNG(t, testImage(100, 50))
	p := Defaults()
	p.Width = 800
	res, err := Transform(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if w, h, _ := decodeDims(t, res.Data); w != 100 || h != 50 {
		t.Fatalf("scaledown upscaled to %dx%d", w, h)
	}
}

func TestFitModes(t *testing.T) {
	src := encodePNG(t, testImage(400, 200))
	for _, tt := range []struct {
		fit          Fit
		wantW, wantH int
	}{
		{FitCover, 100, 100},
		{FitFill, 100, 100},
		{FitContain, 100, 50},
	} {
		p := Defaults()
		p.Width, p.Height, p.Fit = 100, 100, tt.fit
		res, err := Transform(src, p)
		if err != nil {
			t.Fatal(err)
		}
		if w, h, _ := decodeDims(t, res.Data); w != tt.wantW || h != tt.wantH {
			t.Errorf("fit %v: got %dx%d, want %dx%d", tt.fit, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestAutoFormat(t *testing.T) {
	// Opaque source → jpeg.
	res, err := Transform(encodePNG(t, testImage(50, 50)), Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "image/jpeg" {
		t.Fatalf("opaque auto: got %s, want image/jpeg", res.ContentType)
	}
	// Transparent source → png.
	tr := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	tr.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	res, err = Transform(encodePNG(t, tr), Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != "image/png" {
		t.Fatalf("transparent auto: got %s, want image/png", res.ContentType)
	}
}

func TestGrayscale(t *testing.T) {
	p := Defaults()
	p.Gray = true
	p.Format = FormatPNG
	res, err := Transform(encodePNG(t, testImage(10, 10)), p)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(2, 2).RGBA()
	if r != g || g != b {
		t.Fatalf("pixel not gray: %d %d %d", r, g, b)
	}
}

func TestFlipHorizontal(t *testing.T) {
	p := Defaults()
	p.FlipH = true
	p.Format = FormatPNG
	res, err := Transform(encodePNG(t, testImage(10, 10)), p)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(res.Data))
	// Original left half is red; after horizontal flip the left is blue.
	_, _, b, _ := img.At(1, 5).RGBA()
	if b < 0x8000 {
		t.Fatal("left edge not blue after horizontal flip")
	}
}

func TestJPEGQualityAffectsSize(t *testing.T) {
	src := encodePNG(t, testImage(300, 300))
	small, _ := Transform(src, Params{DPR: 1, Quality: 10, Format: FormatJPEG})
	big, _ := Transform(src, Params{DPR: 1, Quality: 95, Format: FormatJPEG})
	if len(small.Data) >= len(big.Data) {
		t.Fatalf("q=10 (%d bytes) not smaller than q=95 (%d bytes)", len(small.Data), len(big.Data))
	}
}

func TestAnimatedGIFPassthrough(t *testing.T) {
	var buf bytes.Buffer
	frames := &gif.GIF{}
	for i := 0; i < 2; i++ {
		pal := image.NewPaletted(image.Rect(0, 0, 20, 20), []color.Color{color.Black, color.White})
		frames.Image = append(frames.Image, pal)
		frames.Delay = append(frames.Delay, 10)
	}
	if err := gif.EncodeAll(&buf, frames); err != nil {
		t.Fatal(err)
	}
	src := buf.Bytes()

	p := Defaults()
	p.Width = 10
	res, err := Transform(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Data, src) {
		t.Fatal("animated gif was not passed through untouched")
	}
	if res.ContentType != "image/gif" {
		t.Fatalf("content type %s", res.ContentType)
	}
}

func TestRejectsNonImage(t *testing.T) {
	_, err := Transform([]byte("definitely not an image"), Defaults())
	if err == nil {
		t.Fatal("garbage input accepted")
	}
	var unsupported ErrUnsupported
	if !errorsAs(err, &unsupported) {
		t.Fatalf("want ErrUnsupported, got %T", err)
	}
}

func TestJPEGWithAlphaFlattens(t *testing.T) {
	tr := image.NewNRGBA(image.Rect(0, 0, 10, 10)) // fully transparent
	p := Defaults()
	p.Format = FormatJPEG
	res, err := Transform(encodePNG(t, tr), p)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := img.At(5, 5).RGBA()
	if r < 0xf000 {
		t.Fatalf("transparent pixels should flatten to white, got r=%d", r)
	}
}

func errorsAs(err error, target *ErrUnsupported) bool {
	for err != nil {
		if e, ok := err.(ErrUnsupported); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
