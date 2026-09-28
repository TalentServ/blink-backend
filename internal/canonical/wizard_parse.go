package canonical

import "encoding/json"

type wizardRoot map[string]any

func parseWizard(raw []byte) wizardRoot {
	var root wizardRoot
	if json.Unmarshal(raw, &root) != nil {
		return wizardRoot{}
	}
	return root
}

func wizardString(root wizardRoot, key string) string {
	v, _ := root[key].(string)
	return v
}

func wizardBool(root wizardRoot, key string) bool {
	v, _ := root[key].(bool)
	return v
}

func wizardArray(root wizardRoot, key string) []any {
	arr, _ := root[key].([]any)
	return arr
}
