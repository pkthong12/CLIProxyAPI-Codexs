package common

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var geminiFunctionDeclarationFields = []string{"functionDeclarations", "function_declarations"}

// RemoveGeminiBuiltInToolsForFunctionCompatibility keeps custom functions when mixed tools are unsupported.
func RemoveGeminiBuiltInToolsForFunctionCompatibility(input []byte, toolsPath string) []byte {
	tools := gjson.GetBytes(input, toolsPath)
	if !hasMixedGeminiTools(tools) {
		return input
	}

	toolItems := make([][]byte, 0, len(tools.Array()))
	tools.ForEach(func(_, tool gjson.Result) bool {
		if hasGeminiFunctionDeclarations(tool) {
			toolItems = append(toolItems, removeGeminiBuiltInToolFields(tool))
		}
		return true
	})

	if len(toolItems) == 0 {
		return input
	}
	output, errSet := sjson.SetRawBytes(input, toolsPath, joinGeminiToolItems(toolItems))
	if errSet != nil {
		return input
	}
	return output
}

// removeGeminiBuiltInToolFields removes built-in tool fields from a function declaration tool.
func removeGeminiBuiltInToolFields(tool gjson.Result) []byte {
	output := []byte(tool.Raw)
	tool.ForEach(func(fieldName, _ gjson.Result) bool {
		name := fieldName.String()
		if name != "functionDeclarations" && name != "function_declarations" {
			output, _ = sjson.DeleteBytes(output, name)
		}
		return true
	})
	return output
}

// joinGeminiToolItems returns a JSON array containing raw tool objects.
func joinGeminiToolItems(toolItems [][]byte) []byte {
	output := []byte{'['}
	for index, tool := range toolItems {
		if index > 0 {
			output = append(output, ',')
		}
		output = append(output, tool...)
	}
	return append(output, ']')
}

// hasMixedGeminiTools reports whether a request combines function declarations and built-in tools.
func hasMixedGeminiTools(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}

	hasFunctionTool := false
	hasBuiltInTool := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		hasFunctionTool = hasFunctionTool || hasGeminiFunctionDeclarations(tool)
		hasBuiltInTool = hasBuiltInGeminiTool(tool)
		return !hasFunctionTool || !hasBuiltInTool
	})
	return hasFunctionTool && hasBuiltInTool
}

// hasGeminiFunctionDeclarations reports whether a tool contains one or more function declarations.
func hasGeminiFunctionDeclarations(tool gjson.Result) bool {
	for _, fieldName := range geminiFunctionDeclarationFields {
		if declarations := tool.Get(fieldName); declarations.IsArray() && len(declarations.Array()) > 0 {
			return true
		}
	}
	return false
}

// hasBuiltInGeminiTool reports whether a tool includes a non-function built-in tool field.
func hasBuiltInGeminiTool(tool gjson.Result) bool {
	hasBuiltInTool := false
	tool.ForEach(func(fieldName, value gjson.Result) bool {
		if fieldName.String() != "functionDeclarations" && fieldName.String() != "function_declarations" && value.Exists() {
			hasBuiltInTool = true
		}
		return !hasBuiltInTool
	})
	return hasBuiltInTool
}
