# Security and format support notes

## Decode limits for untrusted uploads

For user-uploaded or otherwise untrusted images, prefer decoding with explicit limits:

```go
img, err := imaging.Decode(r,
    imaging.MaxEncodedBytes(20<<20),
    imaging.MaxPixels(50_000_000),
    imaging.MaxWidth(12_000),
    imaging.MaxHeight(12_000),
)
```

These options reject oversized encoded data or decoded dimensions before full processing whenever possible. `MaxEncodedBytes` is especially important for non-seekable streams, because the decoder may otherwise need to buffer input while checking image metadata.

## WebP

WebP decoding is registered through `golang.org/x/image/webp`, so `Decode` and `Open` can read WebP images supported by that decoder.

WebP encoding is not implemented in this package. `FormatFromExtension(".webp")` returns `WEBP`, but `Encode(..., WEBP)` returns `ErrUnsupportedFormat`.

## JPEG AutoOrientation scope

`AutoOrientation(true)` reads JPEG EXIF orientation. It does not implement HEIC/HEIF metadata parsing. HEIC/HEIF orientation support requires a separate metadata parser and image decoder stack.

## Animated GIF

`ResizeGIF` is provided as a helper for animated GIFs. It expands each frame to the GIF canvas, resizes the canvas-sized frame, re-palettizes the result, and preserves delays, disposal entries, loop count, and background index.

GIF disposal semantics can be complex for highly optimized animations. For mission-critical GIF processing, compare output visually against representative real files before enabling it in production.
