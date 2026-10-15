package prompt

import (
	"context"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type Context struct {
	AgentID   string
	SessionID protocol.SessionID
	Tools     []tools.Schema
}

type Section struct {
	ID     string
	Order  int
	Render func(context.Context, Context) (string, error)
}

type Diagnostic struct {
	SectionID string
	Bytes     int
}

type Snapshot struct {
	Revision    uint64
	SectionIDs  []string
	Text        string
	Diagnostics []Diagnostic
}

func (s Snapshot) Clone() Snapshot {
	cloned := s
	cloned.SectionIDs = append([]string(nil), s.SectionIDs...)
	cloned.Diagnostics = append([]Diagnostic(nil), s.Diagnostics...)
	return cloned
}
