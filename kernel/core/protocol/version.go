package protocol

import "errors"

const APIVersion = "qi.profile/v1alpha1"

var (
	ErrInvalidProtocol    = errors.New("invalid qi protocol value")
	ErrUnsupportedVersion = errors.New("unsupported qi protocol version")
)
