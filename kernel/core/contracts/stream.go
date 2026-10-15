package contracts

import (
	"iter"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// ChunkStream is the canonical model stream shared by runtime ports.
type ChunkStream = iter.Seq2[protocol.StreamChunk, error]
