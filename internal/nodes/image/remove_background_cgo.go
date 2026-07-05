//go:build cgo

package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
	ort "github.com/yalue/onnxruntime_go"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ── constants ──────────────────────────────────────────────────────────────────

const (
	// u2net input/output dimensions
	u2netSize = 320

	// ORT version bundled with yalue/onnxruntime_go v1.27.0 (API version 24)
	ortVersion = "1.24.1"

	// Model download URLs — same files rembg uses
	u2netModelURL = "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net.onnx"

	// ImageNet normalization (rembg base.py)
	meanR, meanG, meanB = 0.485, 0.456, 0.406
	stdR, stdG, stdB    = 0.229, 0.224, 0.225
)

// ortLibURLs returns the download URL for the ORT shared library for the current platform.
// Archive format: tgz containing lib/libonnxruntime.so.x.y.z (Linux) or
// lib/libonnxruntime.x.y.z.dylib (macOS).
func ortLibURL() (archiveURL, libGlob string, err error) {
	base := "https://github.com/microsoft/onnxruntime/releases/download/v" + ortVersion
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return base + "/onnxruntime-osx-arm64-" + ortVersion + ".tgz",
				"libonnxruntime*.dylib", nil
		}
		return base + "/onnxruntime-osx-x86_64-" + ortVersion + ".tgz",
			"libonnxruntime*.dylib", nil
	case "linux":
		return base + "/onnxruntime-linux-x64-" + ortVersion + ".tgz",
			"libonnxruntime.so*", nil
	default:
		return "", "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// ── ORT & model singletons ─────────────────────────────────────────────────────

var (
	ortInitOnce sync.Once
	ortInitErr  error

	u2netSession  *ort.AdvancedSession
	sessionMu     sync.Mutex
	sessionInput  *ort.Tensor[float32]
	sessionOutput *ort.Tensor[float32]
)

// modelsDir returns ~/.monoagent/models
func modelsDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".monoagent", "models")
	}
	return filepath.Join(os.TempDir(), "monoagent", "models")
}

// libDir returns ~/.monoagent/lib
func libDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".monoagent", "lib")
	}
	return filepath.Join(os.TempDir(), "monoagent", "lib")
}

// ensureORT downloads the ONNX Runtime shared library if not present and
// sets the shared library path for yalue/onnxruntime_go.
func ensureORT() error {
	dir := libDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create lib dir: %w", err)
	}

	// Check if library already present
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".dylib") || strings.HasSuffix(e.Name(), ".so") ||
			strings.Contains(e.Name(), "libonnxruntime")) {
			ort.SetSharedLibraryPath(filepath.Join(dir, e.Name()))
			return nil
		}
	}

	archURL, libGlob, err := ortLibURL()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[image.remove_background] Downloading ONNX Runtime %s...\n", ortVersion)
	tgzPath := filepath.Join(dir, "ort.tgz")
	if err := downloadFile(archURL, tgzPath); err != nil {
		return fmt.Errorf("download ORT: %w", err)
	}
	defer os.Remove(tgzPath)

	libPath, err := extractGlobFromTGZ(tgzPath, libGlob, dir)
	if err != nil {
		return fmt.Errorf("extract ORT library: %w", err)
	}

	ort.SetSharedLibraryPath(libPath)
	return nil
}

// ensureModel downloads u2net.onnx if not present and returns its path.
func ensureModel() (string, error) {
	dir := modelsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create models dir: %w", err)
	}

	dst := filepath.Join(dir, "u2net.onnx")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}

	fmt.Fprintf(os.Stderr, "[image.remove_background] Downloading u2net.onnx (~176 MB)...\n")
	if err := downloadFile(u2netModelURL, dst); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("download u2net.onnx: %w", err)
	}
	return dst, nil
}

// initORT is called once per process to set up the ORT environment and create
// the shared session + tensors.
func initORT(modelPath string) error {
	ortInitOnce.Do(func() {
		if err := ensureORT(); err != nil {
			ortInitErr = fmt.Errorf("ORT setup: %w", err)
			return
		}
		if err := ort.InitializeEnvironment(); err != nil {
			ortInitErr = fmt.Errorf("ORT init: %w", err)
			return
		}

		// Pre-allocate input/output tensors (reused every call under sessionMu)
		inputShape := ort.NewShape(1, 3, u2netSize, u2netSize)
		inputData := make([]float32, 1*3*u2netSize*u2netSize)
		var err error
		sessionInput, err = ort.NewTensor(inputShape, inputData)
		if err != nil {
			ortInitErr = fmt.Errorf("create input tensor: %w", err)
			return
		}

		outputShape := ort.NewShape(1, 1, u2netSize, u2netSize)
		outputData := make([]float32, 1*1*u2netSize*u2netSize)
		sessionOutput, err = ort.NewTensor(outputShape, outputData)
		if err != nil {
			ortInitErr = fmt.Errorf("create output tensor: %w", err)
			return
		}

		// Tensor names from the u2net.onnx model (inspected via GetInputOutputInfo):
		//   input:  "input.1"  [1,3,320,320] float32
		//   output: "1959"     [1,1,320,320] float32  (first/finest sigmoid — same as rembg ort_outs[0])
		u2netSession, err = ort.NewAdvancedSession(
			modelPath,
			[]string{"input.1"},
			[]string{"1959"},
			[]ort.Value{sessionInput},
			[]ort.Value{sessionOutput},
			nil,
		)
		if err != nil {
			ortInitErr = fmt.Errorf("create ORT session: %w", err)
		}
	})
	return ortInitErr
}

// ── preprocessing ──────────────────────────────────────────────────────────────

// preprocess converts img to the [1,3,320,320] float32 tensor expected by u2net.
// Matches rembg's base.py preprocessing exactly:
//  1. Resize to 320×320 with Lanczos
//  2. Convert to RGB float32 [0,1] then normalise per-channel
//  3. Lay out as NCHW
func preprocess(img image.Image, dst []float32) {
	resized := imaging.Resize(img, u2netSize, u2netSize, imaging.Lanczos)

	// Find max pixel value for safe normalization (rembg: divide by max)
	maxVal := 1.0
	bounds := resized.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := resized.At(x, y).RGBA()
			if v := float64(r >> 8); v > maxVal {
				maxVal = v
			}
			if v := float64(g >> 8); v > maxVal {
				maxVal = v
			}
			if v := float64(b >> 8); v > maxVal {
				maxVal = v
			}
		}
	}
	if maxVal < 1e-6 {
		maxVal = 1e-6
	}

	h := bounds.Max.Y - bounds.Min.Y
	w := bounds.Max.X - bounds.Min.X

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := resized.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			rf := float64(r>>8) / maxVal
			gf := float64(g>>8) / maxVal
			bf := float64(b>>8) / maxVal

			dst[0*h*w+y*w+x] = float32((rf - meanR) / stdR)
			dst[1*h*w+y*w+x] = float32((gf - meanG) / stdG)
			dst[2*h*w+y*w+x] = float32((bf - meanB) / stdB)
		}
	}
}

// ── post-processing ────────────────────────────────────────────────────────────

// postprocess builds the alpha mask from the raw model output.
// Matches rembg:
//  1. Normalize to [0,1] via (v - min) / (max - min)
//  2. Clip to [0,1]
//  3. Return as float32 slice sized [320×320]
func postprocess(raw []float32) []float32 {
	n := len(raw)
	minV, maxV := raw[0], raw[0]
	for _, v := range raw[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span < 1e-8 {
		span = 1e-8
	}
	out := make([]float32, n)
	for i, v := range raw {
		norm := (v - minV) / span
		if norm < 0 {
			norm = 0
		}
		if norm > 1 {
			norm = 1
		}
		out[i] = norm
	}
	return out
}

// applyMask composites the original image with the mask (resized to original
// dimensions) to produce an RGBA image with transparent background.
func applyMask(orig image.Image, maskFloats []float32, origW, origH int) *image.NRGBA {
	// Build a grayscale mask image at 320×320
	maskImg := image.NewGray(image.Rect(0, 0, u2netSize, u2netSize))
	for i, v := range maskFloats {
		y := i / u2netSize
		x := i % u2netSize
		maskImg.SetGray(x, y, color.Gray{Y: uint8(math.Round(float64(v) * 255))})
	}

	// Resize mask back to original dimensions; imaging returns *image.NRGBA
	resizedMask := imaging.Resize(maskImg, origW, origH, imaging.Lanczos)

	// Composite — use the red channel of the NRGBA mask as alpha (gray images
	// are stored with R==G==B after imaging.Resize, so any channel works)
	out := image.NewNRGBA(image.Rect(0, 0, origW, origH))
	for y := 0; y < origH; y++ {
		for x := 0; x < origW; x++ {
			r, g, b, _ := orig.At(x, y).RGBA()
			mc := resizedMask.NRGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: mc.R, // R==G==B for a grayscale source
			})
		}
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────────────────

func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractGlobFromTGZ extracts the first file matching globPat from a .tgz
// archive into destDir, returning the full path to the extracted file.
func extractGlobFromTGZ(tgzPath, globPat, destDir string) (string, error) {
	f, err := os.Open(tgzPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		matched, _ := filepath.Match(globPat, base)
		if !matched {
			continue
		}
		outPath := filepath.Join(destDir, base)
		out, err := os.Create(outPath)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("no file matching %q found in archive", globPat)
}

// ── node ───────────────────────────────────────────────────────────────────────

func (n *RemoveBackgroundNode) Execute(_ context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	bgColor, _ := config["bgcolor"].(string)

	if outputField == "" {
		outputField = "image_path"
	}
	if outputDir != "" {
		outputDir = expandHome(outputDir)
	}

	// Ensure ORT + model are ready (idempotent after first call).
	modelPath, err := ensureModel()
	if err != nil {
		return nil, fmt.Errorf("image.remove_background: %w", err)
	}
	if err := initORT(modelPath); err != nil {
		return nil, fmt.Errorf("image.remove_background: %w", err)
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		srcPath := expandHome(resolveImageField(item.JSON, field))
		if srcPath == "" {
			return nil, fmt.Errorf("image.remove_background: no image path in item (tried field=%q)", field)
		}

		result, err := removeBackground(srcPath, outputDir, bgColor)
		if err != nil {
			return nil, fmt.Errorf("image.remove_background: %w", err)
		}
		newJSON[outputField] = result

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// removeBackground runs the full pipeline for a single image and returns the
// output file path.
func removeBackground(srcPath, outputDir, bgColor string) (string, error) {
	// 1. Open source image
	f, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", srcPath, err)
	}
	origImg, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", srcPath, err)
	}

	bounds := origImg.Bounds()
	origW := bounds.Max.X - bounds.Min.X
	origH := bounds.Max.Y - bounds.Min.Y

	// 2. Preprocess
	sessionMu.Lock()
	defer sessionMu.Unlock()

	preprocess(origImg, sessionInput.GetData())

	// 3. Run inference
	if err := u2netSession.Run(); err != nil {
		return "", fmt.Errorf("ORT inference: %w", err)
	}

	// 4. Post-process mask
	maskFloats := postprocess(sessionOutput.GetData())

	// 5. Apply mask
	rgba := applyMask(origImg, maskFloats, origW, origH)

	// 6. Optional background colour flatten
	var finalImg image.Image = rgba
	if bgColor != "" {
		flattened, err := flattenOntoColor(rgba, bgColor)
		if err != nil {
			return "", fmt.Errorf("invalid bgColor: %w", err)
		}
		finalImg = flattened
	}

	// 7. Save
	outPath := buildOutputPath(srcPath, outputDir, "_nobg", ".png")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create output: %w", err)
	}
	defer out.Close()
	if err := png.Encode(out, finalImg); err != nil {
		return "", fmt.Errorf("encode PNG: %w", err)
	}
	return outPath, nil
}

// flattenOntoColor composites the RGBA image onto a solid background colour.
// hexColor is a 3- or 6-digit hex string (with or without leading #).
func flattenOntoColor(img *image.NRGBA, hexColor string) (*image.NRGBA, error) {
	hexColor = strings.TrimPrefix(hexColor, "#")
	if len(hexColor) == 3 {
		hexColor = string([]byte{hexColor[0], hexColor[0], hexColor[1], hexColor[1], hexColor[2], hexColor[2]})
	}
	if len(hexColor) != 6 {
		return nil, fmt.Errorf("hex color must be 3 or 6 digits, got %q", hexColor)
	}
	rgbBytes, err := hex.DecodeString(hexColor)
	if err != nil {
		return nil, fmt.Errorf("invalid hex color %q: %w", hexColor, err)
	}
	br, bg, bb := rgbBytes[0], rgbBytes[1], rgbBytes[2]
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			a := float64(c.A) / 255.0
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(float64(c.R)*a + float64(br)*(1-a)),
				G: uint8(float64(c.G)*a + float64(bg)*(1-a)),
				B: uint8(float64(c.B)*a + float64(bb)*(1-a)),
				A: 255,
			})
		}
	}
	return out, nil
}
