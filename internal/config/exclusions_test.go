package config

import (
	"reflect"
	"testing"
)

// TestLoadExcludedModels verifies pattern loading and the default that leaves
// all models eligible when no exclusions are configured.
func TestLoadExcludedModels(t *testing.T) {
	c, err := Load([]byte(withKey + "excluded-models: [glm-5.2, 'gpt-*', '*preview*']\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"glm-5.2", "gpt-*", "*preview*"}; !reflect.DeepEqual(c.ExcludedModels, want) {
		t.Fatalf("ExcludedModels = %v, want %v", c.ExcludedModels, want)
	}
	for _, yaml := range []string{"", "excluded-models: []\n"} {
		c, err := Load([]byte(withKey + yaml))
		if err != nil || len(c.ExcludedModels) != 0 {
			t.Fatalf("empty exclusions = %v, err = %v", c.ExcludedModels, err)
		}
	}
}

// TestLoadRejectsEmptyExcludedModel ensures blank patterns and invalid YAML
// structures produce configuration errors.
func TestLoadRejectsEmptyExcludedModel(t *testing.T) {
	for _, yaml := range []string{"excluded-models: ['']\n", "excluded-models: ['  ']\n"} {
		_, err := Load([]byte(withKey + yaml))
		requireErrContains(t, err, "excluded-models[0]: must not be empty")
	}
	for _, yaml := range []string{"excluded-models: glm-*\n", "excluded-models: [{}]\n"} {
		_, err := Load([]byte(withKey + yaml))
		requireErrContains(t, err, "decode config: invalid YAML structure")
	}
}
