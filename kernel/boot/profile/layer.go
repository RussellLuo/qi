package profile

import "github.com/RussellLuo/qi/kernel/boot/internal/configcodec"

func normalizeLayer(instances []InstanceSpec, overrides []Override, layer string) ([]InstanceSpec, []Override, error) {
	values := make([]map[string]any, len(instances))
	for i, v := range instances {
		values[i] = map[string]any{"id": v.ID, "plugin": v.Plugin, "config": v.Config, "restartToken": v.RestartToken}
	}
	changes := make([]map[string]any, len(overrides))
	for i, v := range overrides {
		changes[i] = map[string]any{"id": v.ID}
		if v.Plugin != nil {
			changes[i]["plugin"] = *v.Plugin
		}
		if v.Config != nil {
			changes[i]["config"] = *v.Config
		}
		if v.RestartToken != nil {
			changes[i]["restartToken"] = *v.RestartToken
		}
		if v.Disabled != nil {
			changes[i]["disabled"] = *v.Disabled
		}
	}
	values, changes, err := configcodec.NormalizeLayer(values, changes, layer)
	if err != nil {
		return nil, nil, err
	}
	result := make([]InstanceSpec, len(values))
	for i, v := range values {
		result[i] = InstanceSpec{ID: v["id"].(string), Plugin: v["plugin"].(string), Config: v["config"].(map[string]any), RestartToken: v["restartToken"].(string)}
	}
	resultChanges := make([]Override, len(changes))
	for i, v := range changes {
		resultChanges[i].ID = v["id"].(string)
		if item, ok := v["plugin"]; ok {
			value := item.(string)
			resultChanges[i].Plugin = &value
		}
		if item, ok := v["config"]; ok {
			value := item.(map[string]any)
			resultChanges[i].Config = &value
		}
		if item, ok := v["restartToken"]; ok {
			value := item.(string)
			resultChanges[i].RestartToken = &value
		}
		if item, ok := v["disabled"]; ok {
			value := item.(bool)
			resultChanges[i].Disabled = &value
		}
	}
	return result, resultChanges, nil
}
