package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func assertNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	fn()
}

func TestScannerDoesNotPanicOnOutOfRangePaletteIndex(t *testing.T) {
	img := &image.Paletted{
		Pix:     []uint8{70},
		Stride:  1,
		Rect:    image.Rect(0, 0, 1, 1),
		Palette: color.Palette{color.NRGBA{R: 1, G: 2, B: 3, A: 255}},
	}

	operations := []struct {
		name string
		fn   func(image.Image) image.Image
	}{
		{name: "Clone", fn: func(src image.Image) image.Image { return Clone(src) }},
		{name: "Grayscale", fn: func(src image.Image) image.Image { return Grayscale(src) }},
		{name: "Invert", fn: func(src image.Image) image.Image { return Invert(src) }},
		{name: "Resize", fn: func(src image.Image) image.Image { return Resize(src, 2, 2, Lanczos) }},
		{name: "Rotate90", fn: func(src image.Image) image.Image { return Rotate90(src) }},
		{name: "Blur", fn: func(src image.Image) image.Image { return Blur(src, 1.0) }},
	}

	for _, op := range operations {
		op := op
		t.Run(op.name, func(t *testing.T) {
			assertNotPanics(t, func() {
				_ = op.fn(img)
			})
		})
	}
}

func TestScannerDoesNotPanicOnShortYCbCrPlanes(t *testing.T) {
	img := &image.YCbCr{
		Y:              make([]uint8, 1),
		Cb:             nil,
		Cr:             nil,
		YStride:        288,
		CStride:        144,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, 288, 147),
	}

	assertNotPanics(t, func() {
		_ = Rotate90(img)
	})
}

func TestScannerDoesNotPanicOnShortConcreteImageBuffers(t *testing.T) {
	images := []struct {
		name string
		img  image.Image
	}{
		{name: "NRGBA", img: &image.NRGBA{Pix: nil, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}},
		{name: "NRGBA64", img: &image.NRGBA64{Pix: []uint8{1}, Stride: 8, Rect: image.Rect(0, 0, 1, 1)}},
		{name: "RGBA", img: &image.RGBA{Pix: []uint8{1}, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}},
		{name: "RGBA64", img: &image.RGBA64{Pix: []uint8{1}, Stride: 8, Rect: image.Rect(0, 0, 1, 1)}},
		{name: "Gray", img: &image.Gray{Pix: nil, Stride: 1, Rect: image.Rect(0, 0, 1, 1)}},
		{name: "Gray16", img: &image.Gray16{Pix: []uint8{1}, Stride: 2, Rect: image.Rect(0, 0, 1, 1)}},
	}

	for _, tc := range images {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assertNotPanics(t, func() {
				_ = Clone(tc.img)
			})
		})
	}
}

func TestPasteCropsLargerSourceInsteadOfReturningSourceClone(t *testing.T) {
	background := New(2, 2, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	foreground := New(4, 4, color.NRGBA{R: 200, G: 10, B: 20, A: 255})

	got := Paste(background, foreground, image.Pt(-1, -1))
	if got.Bounds() != background.Bounds() {
		t.Fatalf("Paste returned bounds %v, want %v", got.Bounds(), background.Bounds())
	}

	c := color.NRGBAModel.Convert(got.At(0, 0)).(color.NRGBA)
	if c.R != 200 || c.G != 10 || c.B != 20 || c.A != 255 {
		t.Fatalf("Paste did not copy the expected cropped source pixel: got %#v", c)
	}
}

func TestCompositePreservesBackgroundThroughTransparentSource(t *testing.T) {
	background := New(2, 2, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	transparent := image.NewNRGBA(image.Rect(0, 0, 2, 2))

	got := Composite(background, transparent, image.Pt(0, 0))
	c := color.NRGBAModel.Convert(got.At(0, 0)).(color.NRGBA)
	if c.R != 10 || c.G != 20 || c.B != 30 || c.A != 255 {
		t.Fatalf("transparent Composite changed the background pixel: got %#v", c)
	}
}

func TestCenteredHelpersDoNotPanicOnNilInputs(t *testing.T) {
	background := New(2, 2, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	assertNotPanics(t, func() { _ = PasteCenter(nil, nil) })
	assertNotPanics(t, func() { _ = OverlayCenter(nil, nil, 1) })
	assertNotPanics(t, func() { _ = PasteCenter(background, nil) })
	assertNotPanics(t, func() { _ = OverlayCenter(background, nil, 1) })
}

func TestDecodeRejectsImagesOverPixelLimit(t *testing.T) {
	var buf bytes.Buffer
	img := New(2, 2, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("failed to encode test png: %v", err)
	}

	_, err := Decode(bytes.NewReader(buf.Bytes()), MaxPixels(3))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("Decode error = %v, want ErrImageTooLarge", err)
	}
}

func TestDecodeRejectsEncodedDataOverByteLimit(t *testing.T) {
	_, err := Decode(bytes.NewReader([]byte{1, 2, 3, 4}), MaxEncodedBytes(3))
	if !errors.Is(err, ErrEncodedImageTooLarge) {
		t.Fatalf("Decode error = %v, want ErrEncodedImageTooLarge", err)
	}
}

func TestWebPFormatDetection(t *testing.T) {
	format, err := FormatFromExtension(".webp")
	if err != nil {
		t.Fatalf("FormatFromExtension returned error: %v", err)
	}
	if format != WEBP {
		t.Fatalf("FormatFromExtension returned %v, want WEBP", format)
	}

	if err := Encode(&bytes.Buffer{}, New(1, 1, color.NRGBA{}), WEBP); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Encode WEBP error = %v, want ErrUnsupportedFormat", err)
	}
}
