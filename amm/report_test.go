package amm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReportSelfContained(t *testing.T) {
	res := RunScenario(successSpec())
	rep := BuildReport([]*ScenarioResult{res}, 30)
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if strings.Contains(html, dataPlaceholder) {
		t.Fatal("报告中仍残留未替换的占位符")
	}
	if strings.Contains(html, "src=\"http") || strings.Contains(html, "href=\"http") {
		t.Fatal("报告引用了外部网络资源，必须完全自包含")
	}

	// 提取内嵌 JSON 并验证可解析、字段齐备。
	start := strings.Index(html, `<script id="report-data" type="application/json">`)
	if start < 0 {
		t.Fatal("找不到数据脚本块")
	}
	body := html[start+len(`<script id="report-data" type="application/json">`):]
	end := strings.Index(body, "</script>")
	if end < 0 {
		t.Fatal("数据脚本块未闭合")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body[:end]), &parsed); err != nil {
		t.Fatalf("内嵌数据不是合法 JSON: %v", err)
	}
	if _, ok := parsed["scenarios"]; !ok {
		t.Fatal("内嵌数据缺少 scenarios")
	}
}
