package configcodec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

func CheckVersion(version, expected, layer string) error {
	if version != "" && version != expected {
		return fmt.Errorf("%s.apiVersion: unsupported version; expected %s", layer, expected)
	}

	return nil
}

func CheckPlugin(plugin, path string) error {
	if strings.ContainsRune(plugin, 0) || (!IsPluginPath(plugin) && !ValidPluginID(plugin)) {
		return fmt.Errorf("%s.plugin: invalid identifier", path)
	}
	return nil
}

func CopyConfig(config map[string]any, path string) (map[string]any, error) {
	if config == nil {
		return nil, nil
	}

	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("%s: must contain JSON data", path)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%s: must contain JSON data", path)
	}

	return result, nil
}

// Node validation precedes typed decoding: yaml.v3 otherwise accepts null for
// pointers, custom tags as strings, and numbers as string fields.
func ParseDeclaration(raw []byte, layer string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML/JSON document", layer)
	}

	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: expected exactly one document", layer)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: declaration must be an object", layer)
	}

	root := document.Content[0]
	if layer == "bundle definition" {
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "id" && root.Content[i+1].Tag == "!!str" {
				layer = fmt.Sprintf("bundle %q", root.Content[i+1].Value)
			}
		}
	}

	if err := CheckDataNode(root, layer, map[*yaml.Node]bool{}); err != nil {
		return nil, err
	}

	return root, nil
}

func CheckDataNode(node *yaml.Node, path string, active map[*yaml.Node]bool) error {
	if active[node] {
		return fmt.Errorf("%s: cyclic YAML alias", path)
	}

	active[node] = true
	defer delete(active, node)

	if node.Kind == yaml.AliasNode {
		return CheckDataNode(node.Alias, path, active)
	}

	switch node.Tag {
	case "!!map", "!!seq", "!!str", "!!null", "!!bool", "!!int", "!!float":
	default:
		return fmt.Errorf("%s: unsupported YAML tag", path)
	}

	if node.Kind == yaml.MappingNode {
		field := path[strings.LastIndex(path, ".")+1:]
		if (strings.HasPrefix(field, "instances[") || strings.HasPrefix(field, "overrides[")) && strings.HasSuffix(field, "]") {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == "id" && node.Content[i+1].Tag == "!!str" {
					path += fmt.Sprintf(" instance %q", node.Content[i+1].Value)
				}
			}
		}

		seen := map[string]bool{}

		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%s: object keys must be strings", path)
			}
			if seen[key.Value] {
				return fmt.Errorf("%s.%s: duplicate key", path, key.Value)
			}

			seen[key.Value] = true
			if err := CheckDataNode(node.Content[i+1], path+"."+key.Value, active); err != nil {
				return err
			}
		}
	} else {
		for i, child := range node.Content {
			if err := CheckDataNode(child, fmt.Sprintf("%s[%d]", path, i), active); err != nil {
				return err
			}
		}
	}

	return nil
}

func CheckLayerNode(root *yaml.Node, layer string, bundle bool) error {
	fields := map[string]string{
		"apiVersion": "!!str",
		"instances":  "!!seq",
		"overrides":  "!!seq",
	}
	if bundle {
		fields["id"], fields["title"] = "!!str", "!!str"
	} else {
		fields["bundles"] = "!!seq"
	}

	if err := CheckFields(root, layer, fields); err != nil {
		return err
	}

	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, DereferenceNode(root.Content[i+1])
		if key == "bundles" {
			for index, item := range value.Content {
				if DereferenceNode(item).Tag != "!!str" {
					return fmt.Errorf("%s.bundles[%d]: must be a string", layer, index)
				}
			}
		}

		if key != "instances" && key != "overrides" {
			continue
		}

		for index, item := range value.Content {
			item = DereferenceNode(item)
			path := fmt.Sprintf("%s.%s[%d]", layer, key, index)
			if item.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: must be an object", path)
			}

			for j := 0; j < len(item.Content); j += 2 {
				if item.Content[j].Value == "id" && item.Content[j+1].Tag == "!!str" {
					path += fmt.Sprintf(" instance %q", item.Content[j+1].Value)
				}
			}

			allowed := map[string]string{
				"id":           "!!str",
				"plugin":       "!!str",
				"config":       "!!map",
				"restartToken": "!!str",
			}
			if key == "overrides" {
				allowed["disabled"] = "!!bool"
			}

			if err := CheckFields(item, path, allowed); err != nil {
				return err
			}
		}
	}

	return nil
}

func DereferenceNode(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}

	return node
}

func CheckFields(node *yaml.Node, path string, allowed map[string]string) error {
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, DereferenceNode(node.Content[i+1])
		tag, exists := allowed[key]
		if !exists {
			return fmt.Errorf("%s.%s: unknown field", path, key)
		}
		if value.Tag != tag {
			return fmt.Errorf("%s.%s: expected %s (null is not allowed)", path, key, tag)
		}
	}

	return nil
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

func CloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		// Preserve invalid programmatic values so the normalization path can
		// reject them. Returning nil here used to turn an invalid configuration
		// into an apparently valid empty object.
		cloned := make(map[string]any, len(value))

		for key, item := range value {
			cloned[key] = item
		}

		return cloned
	}

	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		cloned = make(map[string]any, len(value))

		for key, item := range value {
			cloned[key] = item
		}
	}

	return cloned
}

func ValidIdentifier(value string) bool { return identifierPattern.MatchString(value) }

// ValidPluginID accepts nonempty name segments separated by slashes. Instance
// and Bundle IDs use ValidIdentifier and remain single identifiers.
func ValidPluginID(value string) bool { return pluginIDPattern.MatchString(value) }

func Digest(raw []byte) string {
	value := sha256.Sum256(raw)
	return "sha256-" + hex.EncodeToString(value[:])
}

// NormalizeLayer validates JSON declaration data without depending on public
// Profile types. Typed callers preserve optional fields with map presence.
func NormalizeLayer(instances, overrides []map[string]any, layer string) ([]map[string]any, []map[string]any, error) {
	seen := map[string]bool{}
	result := make([]map[string]any, len(instances))
	for i, instance := range instances {
		id := instance["id"].(string)
		path := fmt.Sprintf("%s.instances[%d] instance %q", layer, i, id)
		if !ValidIdentifier(id) || seen[id] {
			return nil, nil, fmt.Errorf("%s.id: invalid or duplicate identifier", path)
		}
		seen[id] = true
		if err := CheckPlugin(instance["plugin"].(string), path); err != nil {
			return nil, nil, err
		}
		result[i] = copyFields(instance)
		config, err := CopyConfig(instance["config"].(map[string]any), path+".config")
		if err != nil {
			return nil, nil, err
		}
		result[i]["config"] = config
	}
	changes := make([]map[string]any, len(overrides))
	// Preserve cloneOverrides' original ordering: detach every override config
	// before admitting override IDs/plugins, then validate/copy the original.
	for i, override := range overrides {
		changes[i] = copyFields(override)
		if value, ok := override["config"]; ok {
			changes[i]["config"] = CloneMap(value.(map[string]any))
		}
	}
	seen = map[string]bool{}
	for i, override := range overrides {
		id := override["id"].(string)
		path := fmt.Sprintf("%s.overrides[%d] instance %q", layer, i, id)
		if !ValidIdentifier(id) || seen[id] {
			return nil, nil, fmt.Errorf("%s.id: invalid or duplicate identifier", path)
		}
		seen[id] = true
		if len(override) == 1 {
			return nil, nil, fmt.Errorf("%s: empty override", path)
		}
		if plugin, ok := override["plugin"]; ok {
			if err := CheckPlugin(plugin.(string), path); err != nil {
				return nil, nil, err
			}
		}
		if value, ok := override["config"]; ok {
			if value.(map[string]any) == nil {
				return nil, nil, fmt.Errorf("%s.config: must be an object, not null", path)
			}
			config, err := CopyConfig(value.(map[string]any), path+".config")
			if err != nil {
				return nil, nil, err
			}
			changes[i]["config"] = config
		}
	}
	return result, changes, nil
}
func copyFields(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

// IsPluginPath classifies explicit local paths before any cleaning.
func IsPluginPath(value string) bool {
	return strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || filepath.IsAbs(value)
}
