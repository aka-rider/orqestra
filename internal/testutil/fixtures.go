package testutil

import (
	_ "embed"
	"encoding/json"

	"github.com/xiii/orqestra/internal/types"
)

//go:embed fixtures.json
var FixturesJSON []byte

type Fixtures struct {
	Prompts   map[string]string            `json:"prompts"`
	Responses map[string]map[string]interface{} `json:"responses"`
	Spec      types.Specification          `json:"spec"`
	LLMMock   interface{}                  `json:"llm_mock"`
}

var Data Fixtures

func init() {
	if err := json.Unmarshal(FixturesJSON, &Data); err != nil {
		panic("invalid fixtures.json: " + err.Error())
	}
}

func MockSpec() types.Specification {
	return Data.Spec
}
