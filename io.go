package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

type fileSystem interface {
	Create(string) (io.WriteCloser, error)
	Open(string) (io.ReadCloser, error)
}

type localFS struct{}

func (localFS) Create(name string) (io.WriteCloser, error) { return os.Create(name) }
func (localFS) Open(name string) (io.ReadCloser, error)    { return os.Open(name) }

var fs fileSystem = localFS{}

type decodeConfig struct {
	autoOrientation bool
	maxPixels       int
	maxWidth        int
	maxHeight       int
	maxEncodedBytes int64
}

var defaultDecodeConfig = decodeConfig{
	autoOrientation: false,
}

// DecodeOption sets an optional parameter for the Decode and Open functions.
type DecodeOption func(*decodeConfig)

// AutoOrientation returns a DecodeOption that sets the auto-orientation mode.
// If auto-orientation is enabled, JPEG images will be transformed after decoding
// according to the EXIF orientation tag (if present). By default it's disabled.
func AutoOrientation(enabled bool) DecodeOption {
	return func(c *decodeConfig) {
		c.autoOrientation = enabled
	}
}

// MaxPixels returns a DecodeOption that rejects images whose decoded dimensions
// exceed max total pixels. Values less than or equal to zero disable this limit.
func MaxPixels(max int) DecodeOption {
	return func(c *decodeConfig) {
		c.maxPixels = max
	}
}

// MaxWidth returns a DecodeOption that rejects images wider than max pixels.
// Values less than or equal to zero disable this limit.
func MaxWidth(max int) DecodeOption {
	return func(c *decodeConfig) {
		c.maxWidth = max
	}
}

// MaxHeight returns a DecodeOption that rejects images taller than max pixels.
// Values less than or equal to zero disable this limit.
func MaxHeight(max int) DecodeOption {
	return func(c *decodeConfig) {
		c.maxHeight = max
	}
}

// MaxEncodedBytes returns a DecodeOption that rejects encoded data larger than max bytes.
// Values less than or equal to zero disable this limit.
func MaxEncodedBytes(max int64) DecodeOption {
	return func(c *decodeConfig) {
		c.maxEncodedBytes = max
	}
}

// ErrImageTooLarge means decoded image dimensions exceed configured limits.
var ErrImageTooLarge = errors.New("imaging: image exceeds configured decode limits")

// ErrEncodedImageTooLarge means encoded image data exceeds the configured byte limit.
var ErrEncodedImageTooLarge = errors.New("imaging: encoded image exceeds configured decode byte limit")

func hasDecodeLimits(c decodeConfig) bool {
	return c.maxPixels > 0 || c.maxWidth > 0 || c.maxHeight > 0 || c.maxEncodedBytes > 0
}

func validateDecodedConfig(meta image.Config, c decodeConfig) error {
	if c.maxWidth > 0 && meta.Width > c.maxWidth {
		return fmt.Errorf("%w: width %d exceeds max width %d", ErrImageTooLarge, meta.Width, c.maxWidth)
	}
	if c.maxHeight > 0 && meta.Height > c.maxHeight {
		return fmt.Errorf("%w: height %d exceeds max height %d", ErrImageTooLarge, meta.Height, c.maxHeight)
	}
	if c.maxPixels > 0 {
		pixels := int64(meta.Width) * int64(meta.Height)
		if pixels > int64(c.maxPixels) {
			return fmt.Errorf("%w: %d pixels exceeds max pixels %d", ErrImageTooLarge, pixels, c.maxPixels)
		}
	}
	return nil
}

func readAllWithLimit(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		return ioutil.ReadAll(r)
	}

	var buf bytes.Buffer
	limited := &io.LimitedReader{R: r, N: max + 1}
	if _, err := buf.ReadFrom(limited); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > max {
		return nil, ErrEncodedImageTooLarge
	}
	return buf.Bytes(), nil
}

func decodeBytes(data []byte, cfg decodeConfig) (image.Image, error) {
	meta, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := validateDecodedConfig(meta, cfg); err != nil {
		return nil, err
	}

	var orient orientation
	if cfg.autoOrientation {
		orient = readOrientation(bytes.NewReader(data))
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.autoOrientation {
		img = fixOrientation(img, orient)
	}
	return img, nil
}

func decodeWithLimits(r io.Reader, cfg decodeConfig) (image.Image, error) {
	if cfg.maxEncodedBytes > 0 {
		data, err := readAllWithLimit(r, cfg.maxEncodedBytes)
		if err != nil {
			return nil, err
		}
		return decodeBytes(data, cfg)
	}

	if rs, ok := r.(io.ReadSeeker); ok {
		pos, err := rs.Seek(0, io.SeekCurrent)
		if err == nil {
			meta, _, err := image.DecodeConfig(rs)
			_, seekErr := rs.Seek(pos, io.SeekStart)
			if err != nil {
				return nil, err
			}
			if seekErr != nil {
				return nil, seekErr
			}
			if err := validateDecodedConfig(meta, cfg); err != nil {
				return nil, err
			}

			var orient orientation
			if cfg.autoOrientation {
				orient = readOrientation(rs)
				if _, err := rs.Seek(pos, io.SeekStart); err != nil {
					return nil, err
				}
			}

			img, _, err := image.Decode(rs)
			if err != nil {
				return nil, err
			}
			if cfg.autoOrientation {
				img = fixOrientation(img, orient)
			}
			return img, nil
		}
	}

	data, err := readAllWithLimit(r, cfg.maxEncodedBytes)
	if err != nil {
		return nil, err
	}
	return decodeBytes(data, cfg)
}

// Decode reads an image from r.
func Decode(r io.Reader, opts ...DecodeOption) (image.Image, error) {
	cfg := defaultDecodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	if hasDecodeLimits(cfg) {
		return decodeWithLimits(r, cfg)
	}

	if !cfg.autoOrientation {
		img, _, err := image.Decode(r)
		return img, err
	}

	var orient orientation
	pr, pw := io.Pipe()
	r = io.TeeReader(r, pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		orient = readOrientation(pr)
		io.Copy(ioutil.Discard, pr)
	}()

	img, _, err := image.Decode(r)
	pw.Close()
	<-done
	if err != nil {
		return nil, err
	}

	return fixOrientation(img, orient), nil
}

// Open loads an image from file.
//
// Examples:
//
//	// Load an image from file.
//	img, err := imaging.Open("test.jpg")
//
//	// Load a JPEG image and transform it depending on the EXIF orientation tag (if present).
//	img, err := imaging.Open("test.jpg", imaging.AutoOrientation(true))
//
func Open(filename string, opts ...DecodeOption) (image.Image, error) {
	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Decode(file, opts...)
}

// Format is an image file format.
type Format int

// Image file formats.
const (
	JPEG Format = iota
	PNG
	GIF
	TIFF
	BMP
	WEBP
)

var formatExts = map[string]Format{
	"jpg":  JPEG,
	"jpeg": JPEG,
	"png":  PNG,
	"gif":  GIF,
	"tif":  TIFF,
	"tiff": TIFF,
	"bmp":  BMP,
	"webp": WEBP,
}

var formatNames = map[Format]string{
	JPEG: "JPEG",
	PNG:  "PNG",
	GIF:  "GIF",
	TIFF: "TIFF",
	BMP:  "BMP",
	WEBP: "WEBP",
}

func (f Format) String() string {
	return formatNames[f]
}

// ErrUnsupportedFormat means the given image format is not supported.
var ErrUnsupportedFormat = errors.New("imaging: unsupported image format")

// FormatFromExtension parses image format from filename extension:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff"), "bmp" and "webp" are supported.
func FormatFromExtension(ext string) (Format, error) {
	if f, ok := formatExts[strings.ToLower(strings.TrimPrefix(ext, "."))]; ok {
		return f, nil
	}
	return -1, ErrUnsupportedFormat
}

// FormatFromFilename parses image format from filename:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff"), "bmp" and "webp" are supported.
func FormatFromFilename(filename string) (Format, error) {
	ext := filepath.Ext(filename)
	return FormatFromExtension(ext)
}

type encodeConfig struct {
	jpegQuality         int
	gifNumColors        int
	gifQuantizer        draw.Quantizer
	gifDrawer           draw.Drawer
	pngCompressionLevel png.CompressionLevel
}

var defaultEncodeConfig = encodeConfig{
	jpegQuality:         95,
	gifNumColors:        256,
	gifQuantizer:        nil,
	gifDrawer:           nil,
	pngCompressionLevel: png.DefaultCompression,
}

// EncodeOption sets an optional parameter for the Encode and Save functions.
type EncodeOption func(*encodeConfig)

// JPEGQuality returns an EncodeOption that sets the output JPEG quality.
// Quality ranges from 1 to 100 inclusive, higher is better. Default is 95.
func JPEGQuality(quality int) EncodeOption {
	return func(c *encodeConfig) {
		c.jpegQuality = quality
	}
}

// GIFNumColors returns an EncodeOption that sets the maximum number of colors
// used in the GIF-encoded image. It ranges from 1 to 256.  Default is 256.
func GIFNumColors(numColors int) EncodeOption {
	return func(c *encodeConfig) {
		c.gifNumColors = numColors
	}
}

// GIFQuantizer returns an EncodeOption that sets the quantizer that is used to produce
// a palette of the GIF-encoded image.
func GIFQuantizer(quantizer draw.Quantizer) EncodeOption {
	return func(c *encodeConfig) {
		c.gifQuantizer = quantizer
	}
}

// GIFDrawer returns an EncodeOption that sets the drawer that is used to convert
// the source image to the desired palette of the GIF-encoded image.
func GIFDrawer(drawer draw.Drawer) EncodeOption {
	return func(c *encodeConfig) {
		c.gifDrawer = drawer
	}
}

// PNGCompressionLevel returns an EncodeOption that sets the compression level
// of the PNG-encoded image. Default is png.DefaultCompression.
func PNGCompressionLevel(level png.CompressionLevel) EncodeOption {
	return func(c *encodeConfig) {
		c.pngCompressionLevel = level
	}
}

// Encode writes the image img to w in the specified format (JPEG, PNG, GIF, TIFF or BMP).
// WEBP decoding is supported through Decode/Open, but WEBP encoding is not currently supported.
func Encode(w io.Writer, img image.Image, format Format, opts ...EncodeOption) error {
	cfg := defaultEncodeConfig
	for _, option := range opts {
		option(&cfg)
	}

	switch format {
	case JPEG:
		if nrgba, ok := img.(*image.NRGBA); ok && nrgba.Opaque() {
			rgba := &image.RGBA{
				Pix:    nrgba.Pix,
				Stride: nrgba.Stride,
				Rect:   nrgba.Rect,
			}
			return jpeg.Encode(w, rgba, &jpeg.Options{Quality: cfg.jpegQuality})
		}
		return jpeg.Encode(w, img, &jpeg.Options{Quality: cfg.jpegQuality})

	case PNG:
		encoder := png.Encoder{CompressionLevel: cfg.pngCompressionLevel}
		return encoder.Encode(w, img)

	case GIF:
		return gif.Encode(w, img, &gif.Options{
			NumColors: cfg.gifNumColors,
			Quantizer: cfg.gifQuantizer,
			Drawer:    cfg.gifDrawer,
		})

	case TIFF:
		return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate, Predictor: true})

	case BMP:
		return bmp.Encode(w, img)

	case WEBP:
		return ErrUnsupportedFormat
	}

	return ErrUnsupportedFormat
}

// Save saves the image to file with the specified filename.
// The format is determined from the filename extension:
// "jpg" (or "jpeg"), "png", "gif", "tif" (or "tiff") and "bmp" are supported for encoding.
// WEBP decoding is supported, but WEBP encoding is not currently supported.
//
// Examples:
//
//	// Save the image as PNG.
//	err := imaging.Save(img, "out.png")
//
//	// Save the image as JPEG with optional quality parameter set to 80.
//	err := imaging.Save(img, "out.jpg", imaging.JPEGQuality(80))
//
func Save(img image.Image, filename string, opts ...EncodeOption) (err error) {
	f, err := FormatFromFilename(filename)
	if err != nil {
		return err
	}
	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = Encode(file, img, f, opts...)
	errc := file.Close()
	if err == nil {
		err = errc
	}
	return err
}

// orientation is an EXIF flag that specifies the transformation
// that should be applied to image to display it correctly.
type orientation int

const (
	orientationUnspecified = 0
	orientationNormal      = 1
	orientationFlipH       = 2
	orientationRotate180   = 3
	orientationFlipV       = 4
	orientationTranspose   = 5
	orientationRotate270   = 6
	orientationTransverse  = 7
	orientationRotate90    = 8
)

// readOrientation tries to read the orientation EXIF flag from image data in r.
// It currently supports JPEG EXIF orientation. If the EXIF data block is not found,
// the orientation flag is not found, or any error occurs while reading the data, it
// returns orientationUnspecified (0).
func readOrientation(r io.Reader) orientation {
	const (
		markerSOI      = 0xffd8
		markerAPP1     = 0xffe1
		exifHeader     = 0x45786966
		byteOrderBE    = 0x4d4d
		byteOrderLE    = 0x4949
		orientationTag = 0x0112
	)

	// Check if JPEG SOI marker is present.
	var soi uint16
	if err := binary.Read(r, binary.BigEndian, &soi); err != nil {
		return orientationUnspecified
	}
	if soi != markerSOI {
		return orientationUnspecified // Missing JPEG SOI marker.
	}

	// Find JPEG APP1 marker.
	for {
		var marker, size uint16
		if err := binary.Read(r, binary.BigEndian, &marker); err != nil {
			return orientationUnspecified
		}
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			return orientationUnspecified
		}
		if marker>>8 != 0xff {
			return orientationUnspecified // Invalid JPEG marker.
		}
		if marker == markerAPP1 {
			break
		}
		if size < 2 {
			return orientationUnspecified // Invalid block size.
		}
		if _, err := io.CopyN(ioutil.Discard, r, int64(size-2)); err != nil {
			return orientationUnspecified
		}
	}

	// Check if EXIF header is present.
	var header uint32
	if err := binary.Read(r, binary.BigEndian, &header); err != nil {
		return orientationUnspecified
	}
	if header != exifHeader {
		return orientationUnspecified
	}
	if _, err := io.CopyN(ioutil.Discard, r, 2); err != nil {
		return orientationUnspecified
	}

	// Read byte order information.
	var (
		byteOrderTag uint16
		byteOrder    binary.ByteOrder
	)
	if err := binary.Read(r, binary.BigEndian, &byteOrderTag); err != nil {
		return orientationUnspecified
	}
	switch byteOrderTag {
	case byteOrderBE:
		byteOrder = binary.BigEndian
	case byteOrderLE:
		byteOrder = binary.LittleEndian
	default:
		return orientationUnspecified // Invalid byte order flag.
	}
	if _, err := io.CopyN(ioutil.Discard, r, 2); err != nil {
		return orientationUnspecified
	}

	// Skip the EXIF offset.
	var offset uint32
	if err := binary.Read(r, byteOrder, &offset); err != nil {
		return orientationUnspecified
	}
	if offset < 8 {
		return orientationUnspecified // Invalid offset value.
	}
	if _, err := io.CopyN(ioutil.Discard, r, int64(offset-8)); err != nil {
		return orientationUnspecified
	}

	// Read the number of tags.
	var numTags uint16
	if err := binary.Read(r, byteOrder, &numTags); err != nil {
		return orientationUnspecified
	}

	// Find the orientation tag.
	for i := 0; i < int(numTags); i++ {
		var tag uint16
		if err := binary.Read(r, byteOrder, &tag); err != nil {
			return orientationUnspecified
		}
		if tag != orientationTag {
			if _, err := io.CopyN(ioutil.Discard, r, 10); err != nil {
				return orientationUnspecified
			}
			continue
		}
		if _, err := io.CopyN(ioutil.Discard, r, 6); err != nil {
			return orientationUnspecified
		}
		var val uint16
		if err := binary.Read(r, byteOrder, &val); err != nil {
			return orientationUnspecified
		}
		if val < 1 || val > 8 {
			return orientationUnspecified // Invalid tag value.
		}
		return orientation(val)
	}
	return orientationUnspecified // Missing orientation tag.
}

// fixOrientation applies a transform to img corresponding to the given orientation flag.
func fixOrientation(img image.Image, o orientation) image.Image {
	switch o {
	case orientationNormal:
	case orientationFlipH:
		img = FlipH(img)
	case orientationFlipV:
		img = FlipV(img)
	case orientationRotate90:
		img = Rotate90(img)
	case orientationRotate180:
		img = Rotate180(img)
	case orientationRotate270:
		img = Rotate270(img)
	case orientationTranspose:
		img = Transpose(img)
	case orientationTransverse:
		img = Transverse(img)
	}
	return img
}
