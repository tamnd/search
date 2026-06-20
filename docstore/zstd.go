package docstore

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

// The Zstd encoder and decoder are created once and shared. Their EncodeAll and
// DecodeAll methods are safe for concurrent use, so a single instance of each
// serves every block in the process without per-call setup cost.
var (
	encOnce sync.Once
	encInst *zstd.Encoder
	encErr  error

	decOnce sync.Once
	decInst *zstd.Decoder
	decErr  error
)

func getEncoder() (*zstd.Encoder, error) {
	encOnce.Do(func() {
		encInst, encErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	})
	return encInst, encErr
}

func getDecoder() (*zstd.Decoder, error) {
	decOnce.Do(func() {
		decInst, decErr = zstd.NewReader(nil)
	})
	return decInst, decErr
}
