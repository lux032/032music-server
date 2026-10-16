package analysis

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/lux032/032music-server/internal/storage"
)

// Vector sizes produced by analysis version effnet-discogs-1.
const (
	GenreDims      = 400
	MoodThemeDims  = 56
	InstrumentDims = 40
	EmbeddingDims  = 1280
)

// decodeF16 decodes a base64 float16 LE vector and checks its length and values.
func decodeF16(value string, dims int) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(raw) != dims*2 {
		return nil, fmt.Errorf("vector has %d bytes, want %d", len(raw), dims*2)
	}
	for i := 0; i < dims; i++ {
		if f := Float16ToFloat32(binary.LittleEndian.Uint16(raw[2*i:])); math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, errors.New("vector contains non-finite values")
		}
	}
	return raw, nil
}

// Float16ToFloat32 converts an IEEE 754 half-precision value.
func Float16ToFloat32(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch exp {
	case 0:
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		// subnormal: normalise
		e := uint32(127 - 15 + 1)
		for frac&0x400 == 0 {
			frac <<= 1
			e--
		}
		frac &= 0x3ff
		return math.Float32frombits(sign | e<<23 | frac<<13)
	case 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | frac<<13)
}

// Float16Vector decodes stored float16 LE bytes.
func Float16Vector(raw []byte) []float32 {
	out := make([]float32, len(raw)/2)
	for i := range out {
		out[i] = Float16ToFloat32(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return out
}

// regressionModel applies a stored AudioAVModel.
type regressionModel struct {
	id       int64
	kind     string
	features []string
	weights  []float32 // [dims][2]
	bias     [2]float64
}

func newRegressionModel(m storage.AudioAVModel) (*regressionModel, error) {
	if len(m.Weights) != m.Dims*8 {
		return nil, fmt.Errorf("model weights have %d bytes, want %d", len(m.Weights), m.Dims*8)
	}
	if m.Kind == "embedding" && m.Dims != EmbeddingDims {
		return nil, fmt.Errorf("embedding model has %d dims, want %d", m.Dims, EmbeddingDims)
	}
	if m.Kind == "heads" && len(m.Features) != m.Dims {
		return nil, fmt.Errorf("heads model has %d features for %d dims", len(m.Features), m.Dims)
	}
	w := make([]float32, m.Dims*2)
	for i := range w {
		w[i] = math.Float32frombits(binary.LittleEndian.Uint32(m.Weights[4*i:]))
	}
	return &regressionModel{id: m.ID, kind: m.Kind, features: m.Features, weights: w, bias: [2]float64{m.BiasValence, m.BiasArousal}}, nil
}

// predict returns (valence, arousal) clamped to the DEAM range 1–9.
func (r *regressionModel) predict(embedding []byte, headsJSON string) (float64, float64, error) {
	var x []float64
	if r.kind == "embedding" {
		if len(embedding) != EmbeddingDims*2 {
			return 0, 0, errors.New("embedding size mismatch")
		}
		for _, v := range Float16Vector(embedding) {
			x = append(x, float64(v))
		}
	} else {
		var heads map[string]float64
		if err := json.Unmarshal([]byte(headsJSON), &heads); err != nil {
			return 0, 0, err
		}
		for _, name := range r.features {
			v, ok := heads[name]
			if !ok {
				return 0, 0, fmt.Errorf("missing head %q", name)
			}
			x = append(x, v)
		}
	}
	valence, arousal := r.bias[0], r.bias[1]
	for i, v := range x {
		valence += v * float64(r.weights[2*i])
		arousal += v * float64(r.weights[2*i+1])
	}
	clamp := func(v float64) float64 { return math.Max(1, math.Min(9, v)) }
	return clamp(valence), clamp(arousal), nil
}
