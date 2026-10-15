package protocol

type SessionID string
type RunID string
type InvocationID = RunID
type TurnID int64
type StepID int64
type MessageID string
type ToolCallID string

// Sequence is signed so an empty log can expose the watermark -1.
type Sequence int64

func (id SessionID) String() string { return string(id) }

func (id RunID) String() string { return string(id) }

func (id MessageID) String() string { return string(id) }

func (id ToolCallID) String() string { return string(id) }
