package imaging

import (
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"math"
)

func gifCanvasSize(g *gif.GIF) (int, int) {
	w := g.Config.Width
	h := g.Config.Height
	for _, frame := range g.Image {
		if frame == nil {
			continue
		}
		b := frame.Bounds()
		if b.Max.X > w {
			w = b.Max.X
		}
		if b.Max.Y > h {
			h = b.Max.Y
		}
	}
	return w, h
}

func resizeGIFDimensions(srcW, srcH, width, height int) (int, int) {
	if srcW <= 0 || srcH <= 0 || width < 0 || height < 0 || (width == 0 && height == 0) {
		return 0, 0
	}
	if width == 0 {
		width = int(math.Max(1.0, math.Floor(float64(height)*float64(srcW)/float64(srcH)+0.5)))
	}
	if height == 0 {
		height = int(math.Max(1.0, math.Floor(float64(width)*float64(srcH)/float64(srcW)+0.5)))
	}
	return width, height
}

func gifPaletteFor(img image.Image, cfg encodeConfig) color.Palette {
	if cfg.gifNumColors < 1 {
		cfg.gifNumColors = 1
	}
	if cfg.gifNumColors > 256 {
		cfg.gifNumColors = 256
	}
	if cfg.gifQuantizer != nil {
		return cfg.gifQuantizer.Quantize(make(color.Palette, 0, cfg.gifNumColors), img)
	}
	return palette.Plan9
}

func drawGIFFrame(dst draw.Image, src image.Image, cfg encodeConfig) {
	drawer := cfg.gifDrawer
	if drawer == nil {
		drawer = draw.FloydSteinberg
	}
	drawer.Draw(dst, dst.Bounds(), src, src.Bounds().Min)
}

// ResizeGIF resizes all frames of an animated GIF and returns a new GIF.
// Frame delays, disposal methods, loop count and background index are preserved.
// Frames are expanded to the GIF canvas before resizing to keep frame alignment stable.
func ResizeGIF(src *gif.GIF, width, height int, filter ResampleFilter, opts ...EncodeOption) *gif.GIF {
	if src == nil || len(src.Image) == 0 {
		return &gif.GIF{}
	}

	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	srcW, srcH := gifCanvasSize(src)
	dstW, dstH := resizeGIFDimensions(srcW, srcH, width, height)
	if dstW <= 0 || dstH <= 0 {
		return &gif.GIF{}
	}

	out := &gif.GIF{
		Image:           make([]*image.Paletted, 0, len(src.Image)),
		Delay:           append([]int(nil), src.Delay...),
		LoopCount:       src.LoopCount,
		Disposal:        append([]byte(nil), src.Disposal...),
		Config:          image.Config{ColorModel: src.Config.ColorModel, Width: dstW, Height: dstH},
		BackgroundIndex: src.BackgroundIndex,
	}

	canvasRect := image.Rect(0, 0, srcW, srcH)
	for _, frame := range src.Image {
		if frame == nil {
			out.Image = append(out.Image, image.NewPaletted(image.Rect(0, 0, dstW, dstH), palette.Plan9))
			continue
		}

		canvas := image.NewNRGBA(canvasRect)
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Src)
		resized := Resize(canvas, dstW, dstH, filter)
		pal := gifPaletteFor(resized, cfg)
		paletted := image.NewPaletted(image.Rect(0, 0, dstW, dstH), pal)
		drawGIFFrame(paletted, resized, cfg)
		out.Image = append(out.Image, paletted)
	}

	for len(out.Delay) < len(out.Image) {
		out.Delay = append(out.Delay, 0)
	}
	for len(out.Disposal) < len(out.Image) {
		out.Disposal = append(out.Disposal, gif.DisposalNone)
	}

	return out
}
