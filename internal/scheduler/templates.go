package scheduler

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

var baseTemplate = template.Must(template.ParseFS(templateFiles, "templates/*.tmpl"))

func RenderPrompt(data PromptData) string {
	var buf bytes.Buffer
	if err := baseTemplate.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("RenderPrompt execution failed: %v", err))
	}
	return buf.String()
}
