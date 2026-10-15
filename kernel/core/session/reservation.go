package session

import "github.com/RussellLuo/qi/kernel/core/protocol"

type reservation struct {
	source string
	id     protocol.SessionID
	begin  func() (func(), error)
	close  func() error
}

func (r *reservation) SessionID() protocol.SessionID { return r.id }

func (r *reservation) BeginRun() (func(), error) { return r.begin() }

func (r *reservation) Close() error { return r.close() }

func (r *reservation) CreationSource() string { return r.source }
