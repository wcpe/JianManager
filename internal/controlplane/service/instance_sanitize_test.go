package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeStartCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"无引号", `java -Xmx2G -jar paper.jar nogui`, `java -Xmx2G -jar paper.jar nogui`},
		{"外层双引号包裹", `"java -Xmx2G -jar paper.jar"`, `java -Xmx2G -jar paper.jar`},
		{"外层单引号包裹", `'java -Xmx2G -jar paper.jar'`, `java -Xmx2G -jar paper.jar`},
		{"路径含空格不误删", `"C:\Program Files\java.exe" -jar server.jar`, `"C:\Program Files\java.exe" -jar server.jar`},
		{"嵌套单双引号", `'"java -jar server.jar"'`, `"java -jar server.jar"`},
		{"仅一对引号无内容", `""`, ``},
		{"空字符串", ``, ``},
		{"前后空格", `  java -jar server.jar  `, `java -jar server.jar`},
		{"路径含空格无外层引号", `C:\Program Files\java.exe -jar server.jar`, `C:\Program Files\java.exe -jar server.jar`},
		{"外层单引号内含双引号", `'"C:\java.exe" -jar server.jar'`, `"C:\java.exe" -jar server.jar`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeStartCommand(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
