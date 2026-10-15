package session

import "github.com/RussellLuo/gordis"

var (
	PersistenceKey = gordis.NewKey[PersistenceProvider]("qi/session-persistence")
	StoreKey       = gordis.NewKey[Store]("qi/session")
)
