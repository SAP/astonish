package routing

// npz.go — minimal pure-Go parser for numpy .npz (ZIP of .npy) weight files.
//
// npz is a ZIP archive where each entry is a .npy file.
// npy format:
//
//	magic:   \x93NUMPY  (6 bytes)
//	major:   1 byte
//	minor:   1 byte
//	hdrlen:  2 bytes (LE uint16) for v1; 4 bytes (LE uint32) for v2
//	header:  hdrlen bytes — Python dict literal with 'descr', 'fortran_order', 'shape'
//	data:    raw elements in C order
//
// We only need float32 ('<f4') and byte string ('|S...') dtypes.

import (
	"archive/zip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// npzWeights holds the float64 arrays parsed from a router_weights.npz file.
// All numeric arrays are stored as float64 for precision.
type npzWeights struct {
	layer1Weight [][]float64 // (64, 384)
	layer1Bias   []float64   // (64,)
	layer2Weight [][]float64 // (32, 64)
	layer2Bias   []float64   // (32,)
	layer3Weight []float64   // (32,)
	layer3Bias   float64     // scalar
	embeddingDim int
	modelDim     int
}

// parseNpz parses a .npz file at the given path and returns the routing weights.
func parseNpz(path string) (*npzWeights, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open npz: %w", err)
	}
	defer r.Close()

	arrays := make(map[string][]float32)
	shapes := make(map[string][]int)
	var configBytes []byte

	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, ".npy")
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open npy %s: %w", f.Name, err)
		}
		data, shape, isBytes, err := parseNpy(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("parse npy %s: %w", f.Name, err)
		}
		if isBytes {
			configBytes = make([]byte, len(data))
			// data is raw bytes interpreted as float32 bits — re-read as bytes
			for i, v := range data {
				if !math.IsNaN(float64(v)) {
					configBytes[i] = byte(math.Float32bits(v))
				}
			}
			// Actually for byte arrays, we stored them directly — see parseNpy
			_ = shape
			continue
		}
		arrays[name] = data
		shapes[name] = shape
	}

	// Re-parse config separately: re-open to get raw bytes
	for _, f := range r.File {
		if strings.TrimSuffix(f.Name, ".npy") == "config" {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open config npy: %w", err)
			}
			raw, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read config npy: %w", err)
			}
			configBytes, err = extractNpyBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("extract config bytes: %w", err)
			}
			break
		}
	}

	w := &npzWeights{}

	// layer1_weight: shape (64, 384)
	l1w, ok := arrays["layer1_weight"]
	if !ok {
		return nil, fmt.Errorf("missing layer1_weight")
	}
	l1shape := shapes["layer1_weight"]
	if len(l1shape) != 2 {
		return nil, fmt.Errorf("layer1_weight must be 2D, got %v", l1shape)
	}
	rows, cols := l1shape[0], l1shape[1]
	w.layer1Weight = make([][]float64, rows)
	for i := range w.layer1Weight {
		w.layer1Weight[i] = make([]float64, cols)
		for j := range w.layer1Weight[i] {
			w.layer1Weight[i][j] = float64(l1w[i*cols+j])
		}
	}

	// layer1_bias: shape (64,)
	l1b, ok := arrays["layer1_bias"]
	if !ok {
		return nil, fmt.Errorf("missing layer1_bias")
	}
	w.layer1Bias = float32ToFloat64(l1b)

	// layer2_weight: shape (32, 64)
	l2w, ok := arrays["layer2_weight"]
	if !ok {
		return nil, fmt.Errorf("missing layer2_weight")
	}
	l2shape := shapes["layer2_weight"]
	if len(l2shape) != 2 {
		return nil, fmt.Errorf("layer2_weight must be 2D, got %v", l2shape)
	}
	rows2, cols2 := l2shape[0], l2shape[1]
	w.layer2Weight = make([][]float64, rows2)
	for i := range w.layer2Weight {
		w.layer2Weight[i] = make([]float64, cols2)
		for j := range w.layer2Weight[i] {
			w.layer2Weight[i][j] = float64(l2w[i*cols2+j])
		}
	}

	// layer2_bias: shape (32,)
	l2b, ok := arrays["layer2_bias"]
	if !ok {
		return nil, fmt.Errorf("missing layer2_bias")
	}
	w.layer2Bias = float32ToFloat64(l2b)

	// layer3_weight: shape (32,) — stored as (32,)
	l3w, ok := arrays["layer3_weight"]
	if !ok {
		return nil, fmt.Errorf("missing layer3_weight")
	}
	w.layer3Weight = float32ToFloat64(l3w)

	// layer3_bias: shape (1,) — scalar
	l3b, ok := arrays["layer3_bias"]
	if !ok {
		return nil, fmt.Errorf("missing layer3_bias")
	}
	if len(l3b) < 1 {
		return nil, fmt.Errorf("layer3_bias is empty")
	}
	w.layer3Bias = float64(l3b[0])

	// config JSON
	if len(configBytes) > 0 {
		// Trim any null bytes
		s := strings.TrimRight(string(configBytes), "\x00")
		embDim, modelDim, err := parseConfigJSON(s)
		if err != nil {
			return nil, fmt.Errorf("parse config JSON: %w", err)
		}
		w.embeddingDim = embDim
		w.modelDim = modelDim
	} else {
		// Fallback defaults matching the trained model
		w.embeddingDim = 384
		w.modelDim = 64
	}

	return w, nil
}

// parseNpy parses a .npy v1/v2 array. Returns (data, shape, isBytes, error).
// isBytes=true means the array was a byte-string dtype ('|S...') — caller should
// use extractNpyBytes to get the raw content.
func parseNpy(r io.Reader) (data []float32, shape []int, isBytes bool, err error) {
	// Read magic
	magic := make([]byte, 6)
	if _, err = io.ReadFull(r, magic); err != nil {
		return nil, nil, false, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != "\x93NUMPY" {
		return nil, nil, false, fmt.Errorf("not a .npy file (bad magic)")
	}

	// Read version
	ver := make([]byte, 2)
	if _, err = io.ReadFull(r, ver); err != nil {
		return nil, nil, false, fmt.Errorf("read version: %w", err)
	}
	major := ver[0]

	// Read header length
	var hdrLen uint32
	if major == 1 {
		var h uint16
		if err = binary.Read(r, binary.LittleEndian, &h); err != nil {
			return nil, nil, false, fmt.Errorf("read hdrlen v1: %w", err)
		}
		hdrLen = uint32(h)
	} else {
		if err = binary.Read(r, binary.LittleEndian, &hdrLen); err != nil {
			return nil, nil, false, fmt.Errorf("read hdrlen v2: %w", err)
		}
	}

	// Read header
	hdrBytes := make([]byte, hdrLen)
	if _, err = io.ReadFull(r, hdrBytes); err != nil {
		return nil, nil, false, fmt.Errorf("read header: %w", err)
	}
	hdr := string(hdrBytes)

	// Parse dtype
	dtype, err := parseDtype(hdr)
	if err != nil {
		return nil, nil, false, err
	}

	// Check for bytes dtype
	if strings.HasPrefix(dtype, "|S") || strings.HasPrefix(dtype, "'|S") {
		// Byte-string array — return isBytes=true; caller will re-read
		return nil, nil, true, nil
	}
	if dtype != "<f4" && dtype != "float32" && dtype != ">f4" {
		return nil, nil, false, fmt.Errorf("unsupported dtype %q (expected <f4)", dtype)
	}

	// Parse shape
	shape, err = parseShape(hdr)
	if err != nil {
		return nil, nil, false, err
	}

	// Calculate total elements
	total := 1
	for _, s := range shape {
		total *= s
	}
	if total == 0 {
		return []float32{}, shape, false, nil
	}

	// Read raw float32 data
	raw := make([]byte, total*4)
	if _, err = io.ReadFull(r, raw); err != nil {
		return nil, nil, false, fmt.Errorf("read data: %w", err)
	}

	data = make([]float32, total)
	if dtype == ">f4" {
		for i := range data {
			bits := binary.BigEndian.Uint32(raw[i*4 : i*4+4])
			data[i] = math.Float32frombits(bits)
		}
	} else {
		for i := range data {
			bits := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
			data[i] = math.Float32frombits(bits)
		}
	}

	return data, shape, false, nil
}

// extractNpyBytes re-parses a raw .npy byte blob and returns the embedded byte string.
func extractNpyBytes(raw []byte) ([]byte, error) {
	if len(raw) < 10 {
		return nil, fmt.Errorf("npy too short")
	}
	// magic(6) + version(2) + hdrlen(2 or 4)
	magic := string(raw[:6])
	if magic != "\x93NUMPY" {
		return nil, fmt.Errorf("not a .npy file")
	}
	major := raw[6]
	var hdrLen int
	var offset int
	if major == 1 {
		hdrLen = int(binary.LittleEndian.Uint16(raw[8:10]))
		offset = 10
	} else {
		hdrLen = int(binary.LittleEndian.Uint32(raw[8:12]))
		offset = 12
	}
	if offset+hdrLen > len(raw) {
		return nil, fmt.Errorf("npy header overflows file")
	}
	// The rest after the header is the raw byte data
	data := raw[offset+hdrLen:]
	// Strip null padding
	end := len(data)
	for end > 0 && data[end-1] == 0 {
		end--
	}
	return data[:end], nil
}

// parseDtype extracts the dtype string from a npy header dict.
// Header looks like: {'descr': '<f4', 'fortran_order': False, 'shape': (64,), }
func parseDtype(hdr string) (string, error) {
	key := "'descr'"
	idx := strings.Index(hdr, key)
	if idx < 0 {
		return "", fmt.Errorf("descr not found in header")
	}
	rest := hdr[idx+len(key):]
	// Skip ': '
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", fmt.Errorf("no colon after descr")
	}
	rest = strings.TrimSpace(rest[colon+1:])
	// Value is quoted: '<f4' or '|S200'
	if len(rest) < 2 {
		return "", fmt.Errorf("descr value too short")
	}
	quote := rest[0]
	if quote != '\'' && quote != '"' {
		return "", fmt.Errorf("descr value not quoted: %q", rest[:min(20, len(rest))])
	}
	end := strings.IndexByte(rest[1:], quote)
	if end < 0 {
		return "", fmt.Errorf("descr value unterminated")
	}
	return rest[1 : end+1], nil
}

// parseShape extracts the shape tuple from a npy header dict.
func parseShape(hdr string) ([]int, error) {
	key := "'shape'"
	idx := strings.Index(hdr, key)
	if idx < 0 {
		return nil, fmt.Errorf("shape not found in header")
	}
	rest := hdr[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return nil, fmt.Errorf("no colon after shape")
	}
	rest = strings.TrimSpace(rest[colon+1:])
	// Shape is a Python tuple: (64,) or (32, 64) or ()
	open := strings.Index(rest, "(")
	close := strings.Index(rest, ")")
	if open < 0 || close < 0 || close <= open {
		return nil, fmt.Errorf("shape tuple not found: %q", rest[:min(40, len(rest))])
	}
	inner := strings.TrimSpace(rest[open+1 : close])
	if inner == "" {
		return []int{}, nil // scalar ()
	}
	parts := strings.Split(inner, ",")
	shape := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("shape component %q: %w", p, err)
		}
		shape = append(shape, n)
	}
	return shape, nil
}

// parseConfigJSON extracts embedding_dim and model_dim from the JSON config string.
// The config looks like: {"embedding_dim": 384, "model_dim": 64}
func parseConfigJSON(s string) (embeddingDim, modelDim int, err error) {
	// Minimal JSON parsing — just find the two integer fields
	embeddingDim = extractIntField(s, "embedding_dim", 384)
	modelDim = extractIntField(s, "model_dim", 64)
	return embeddingDim, modelDim, nil
}

func extractIntField(s, field string, defaultVal int) int {
	key := `"` + field + `"`
	idx := strings.Index(s, key)
	if idx < 0 {
		return defaultVal
	}
	rest := s[idx+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return defaultVal
	}
	rest = strings.TrimSpace(rest[colon+1:])
	// Read digits
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	if end == 0 {
		return defaultVal
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return defaultVal
	}
	return n
}

func float32ToFloat64(f32 []float32) []float64 {
	f64 := make([]float64, len(f32))
	for i, v := range f32 {
		f64[i] = float64(v)
	}
	return f64
}
