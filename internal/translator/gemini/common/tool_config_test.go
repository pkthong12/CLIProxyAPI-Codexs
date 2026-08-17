package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRemoveGeminiBuiltInToolsForFunctionCompatibility(t *testing.T) {
	testCases := []struct {
		name             string
		inputJSON        string
		wantToolCount    int
		wantGoogleSearch bool
	}{
		{
			name: "separate function and built-in tools",
			inputJSON: `{
				"request": {
					"tools": [
						{"functionDeclarations": [{"name": "lookup"}]},
						{"googleSearch": {}}
					]
				}
			}`,
			wantToolCount:    1,
			wantGoogleSearch: false,
		},
		{
			name: "combined function and built-in tool",
			inputJSON: `{
				"request": {"tools": [{"functionDeclarations": [{"name": "lookup"}], "googleSearch": {}}]}
			}`,
			wantToolCount:    1,
			wantGoogleSearch: false,
		},
		{
			name: "built-in tools only",
			inputJSON: `{
				"request": {"tools": [{"googleSearch": {}}]}
			}`,
			wantToolCount:    1,
			wantGoogleSearch: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output := RemoveGeminiBuiltInToolsForFunctionCompatibility([]byte(testCase.inputJSON), "request.tools")
			tools := gjson.GetBytes(output, "request.tools").Array()
			if len(tools) != testCase.wantToolCount {
				t.Fatalf("tool count = %d, want %d. Output: %s", len(tools), testCase.wantToolCount, output)
			}
			hasGoogleSearch := gjson.GetBytes(output, "request.tools.#(googleSearch)").Exists()
			if hasGoogleSearch != testCase.wantGoogleSearch {
				t.Fatalf("googleSearch = %t, want %t. Output: %s", hasGoogleSearch, testCase.wantGoogleSearch, output)
			}
			if testCase.wantToolCount == 1 && !testCase.wantGoogleSearch && !gjson.GetBytes(output, "request.tools.0.functionDeclarations.0.name").Exists() {
				t.Fatalf("function declaration was removed. Output: %s", output)
			}
		})
	}
}
