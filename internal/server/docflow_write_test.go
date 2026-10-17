package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDocflowWrite(t *testing.T) {
	calls := map[string]map[string]any{}
	var order []string
	versionFails := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/odata/")
		if r.Method == http.MethodPost {
			var body map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			calls[p] = body
			order = append(order, p)
		}
		switch p {
		case "IElectronicDocuments(10)":
			json.NewEncoder(w).Encode(map[string]any{"Id": 10, "Name": "Положение о премировании"})
		case "RecordManagement/CreateAcquaintanceTask", "Docflow/CreateFreeApprovalTask", "Docflow/CreateApprovalTask":
			json.NewEncoder(w).Encode(map[string]any{"value": 501})
		case "Docflow/StartTask":
			w.WriteHeader(204)
		case "IMemoArticles":
			json.NewEncoder(w).Encode(map[string]any{"Id": 77, "Name": "Как выпустить релиз"})
		case "MemoRX/CreateMemoVersion":
			if versionFails {
				http.Error(w, `{"value":"нет лицензии"}`, 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Id": 1})
		case "MemoRX/DeleteEmptyArticleById", "MemoRX/UpdateArticleTitle":
			w.WriteHeader(204)
		case "IAssignments(9)":
			json.NewEncoder(w).Encode(map[string]any{"@odata.type": "#X.Docflow.IApprovalSigningAssignmentDto", "Id": 9, "Subject": "Подписать", "Status": "InProcess"})
		case "IAssignments(8)":
			json.NewEncoder(w).Encode(map[string]any{"@odata.type": "#X.Docflow.IApprovalAssignmentDto", "Id": 8, "Subject": "Согласовать", "Status": "InProcess"})
		case "Docflow/CompleteAssignment":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cs := connect(t, true, srv.URL+"/odata")
	call := func(tool string, args map[string]any) (string, bool) {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}

	out, isErr := call("rx_send_for_acquaintance", map[string]any{"document_id": 10, "performer_ids": []int{7, 8}, "deadline": "2026-10-20", "text": "прочитать"})
	a := calls["RecordManagement/CreateAcquaintanceTask"]
	if isErr || a["documentId"] != float64(10) || a["isElectronicAcquaintance"] != false || a["deadline"] == nil || len(a["performerIds"].([]any)) != 2 {
		t.Errorf("ознакомление: %s %v", out, a)
	}
	if calls["Docflow/StartTask"]["taskId"] != float64(501) || !strings.Contains(out, "задача #501") || !strings.Contains(out, "Положение о премировании") {
		t.Errorf("старт ознакомления: %s %v", out, calls["Docflow/StartTask"])
	}
	if _, isErr = call("rx_send_for_acquaintance", map[string]any{"document_id": 10, "performer_ids": []int{7}}); !isErr {
		t.Error("ознакомление без срока должно отклоняться")
	}

	out, isErr = call("rx_send_for_approval", map[string]any{"document_id": 10, "approver_ids": []int{7}, "deadline": "завтра"})
	f := calls["Docflow/CreateFreeApprovalTask"]
	if isErr || f["approverIds"] == nil || f["deadline"] == nil {
		t.Errorf("свободное согласование: %s %v", out, f)
	}
	out, isErr = call("rx_send_for_approval", map[string]any{"document_id": 10, "mode": "rule", "approver_ids": []int{8}, "signatory_id": 3})
	rr := calls["Docflow/CreateApprovalTask"]
	if isErr || rr["signatoryId"] != float64(3) || len(rr["addApproverIds"].([]any)) != 1 {
		t.Errorf("по регламенту: %s %v", out, rr)
	}
	if _, isErr = call("rx_send_for_approval", map[string]any{"document_id": 10}); !isErr {
		t.Error("свободное согласование без согласующих должно отклоняться")
	}

	out, isErr = call("rx_create_article", map[string]any{"title": "Как выпустить релиз", "text": "## Шаги\n1. Тег", "area_id": 115})
	ar := calls["IMemoArticles"]
	if isErr || ar["Name"] != "Как выпустить релиз" || !strings.Contains(out, "#77") {
		t.Errorf("статья: %s %v", out, ar)
	}
	if areas, _ := ar["Areas"].([]any); len(areas) != 1 {
		t.Errorf("область статьи: %v", ar)
	}
	if v := calls["MemoRX/CreateMemoVersion"]; v["memoId"] != float64(77) || v["content"] != "## Шаги\n1. Тег" {
		t.Errorf("текст статьи: %v", v)
	}
	versionFails = true
	delete(calls, "MemoRX/DeleteEmptyArticleById")
	if out, isErr = call("rx_create_article", map[string]any{"title": "x", "text": "y", "area_id": 115}); !isErr || !strings.Contains(out, "пустую статью удалил") || calls["MemoRX/DeleteEmptyArticleById"]["memoId"] != float64(77) {
		t.Errorf("уборка пустой статьи: %s", out)
	}
	versionFails = false
	if out, isErr = call("rx_update_article", map[string]any{"id": 77, "title": "Релиз", "text": "новый текст"}); isErr || calls["MemoRX/UpdateArticleTitle"]["title"] != "Релиз" {
		t.Errorf("правка статьи: %s", out)
	}

	if out, isErr = call("rx_complete_assignment", map[string]any{"id": 9}); !isErr || !strings.Contains(out, "подпись") {
		t.Errorf("подписание должно отклоняться: %s", out)
	}
	delete(calls, "Docflow/CompleteAssignment")
	if out, isErr = call("rx_complete_assignment", map[string]any{"id": 8, "result": "с замечаниями"}); isErr || calls["Docflow/CompleteAssignment"]["result"] != "WithSuggestions" {
		t.Errorf("согласование с замечаниями: %s %v", out, calls["Docflow/CompleteAssignment"])
	}

	ro := toolNames(t, connect(t, false, srv.URL+"/odata"))
	for _, n := range []string{"rx_send_for_acquaintance", "rx_send_for_approval", "rx_create_article", "rx_update_article"} {
		if _, ok := ro[n]; ok {
			t.Errorf("%s виден без RXMCP_ALLOW_WRITE", n)
		}
	}
}
