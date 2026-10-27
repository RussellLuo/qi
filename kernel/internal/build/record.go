package build

import (
	"crypto/sha256"
	"encoding/hex"
)

// These envelopes preserve the Qi record schemas, independently of the public
// engine's record format. All module, input and command facts come from Gordis.
type commandRecord struct {
	Args     []string `json:"args"`
	Log      string   `json:"log"`
	ExitCode int      `json:"exitCode"`
}

type moduleInfo struct {
	Path      string      `json:"Path"`
	Version   string      `json:"Version,omitempty"`
	Main      bool        `json:"Main,omitempty"`
	Dir       string      `json:"Dir,omitempty"`
	GoMod     string      `json:"GoMod,omitempty"`
	Sum       string      `json:"Sum,omitempty"`
	GoModSum  string      `json:"GoModSum,omitempty"`
	GoVersion string      `json:"GoVersion,omitempty"`
	Replace   *moduleInfo `json:"Replace,omitempty"`
}

type sourceRecord struct {
	Module        string            `json:"module"`
	Directory     string            `json:"directory"`
	TargetVersion string            `json:"targetVersion"`
	Files         map[string]string `json:"files"`
	SHA256        string            `json:"sha256"`
}

type Record struct {
	APIVersion       string            `json:"apiVersion"`
	Manifest         Manifest          `json:"manifest"`
	ManifestSHA256   string            `json:"manifestSHA256"`
	LocalDevelopment bool              `json:"localDevelopment"`
	Sources          []sourceRecord    `json:"sources"`
	Modules          []moduleInfo      `json:"modules"`
	Environment      map[string]string `json:"environment"`
	GeneratedSHA256  map[string]string `json:"generatedSHA256"`
	BinarySHA256     string            `json:"binarySHA256"`
	Commands         []commandRecord   `json:"commands"`
}

// requestRecord uses a distinct API version for CLI query and resolution facts.
type requestRecord struct {
	APIVersion       string            `json:"apiVersion"`
	Request          *Request          `json:"request"`
	RequestSHA256    string            `json:"requestSHA256"`
	Resolved         Resolution        `json:"resolved"`
	LocalDevelopment bool              `json:"localDevelopment"`
	Sources          []sourceRecord    `json:"sources"`
	Modules          []moduleInfo      `json:"modules"`
	Environment      map[string]string `json:"environment"`
	GeneratedSHA256  map[string]string `json:"generatedSHA256"`
	BinarySHA256     string            `json:"binarySHA256"`
	Commands         []commandRecord   `json:"commands"`
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
