package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testApplication() *application {
	app := newApplication("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	app.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	return app
}

func request(t *testing.T, handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("resposta de erro não é JSON válido: %v", err)
	}
	return payload.Error
}

func TestDeveCriarCobrancaAprovadaQuandoPayloadValido(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, esperado %d; corpo: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var got charge
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ChargeID, "chg_") || len(got.ChargeID) != 16 {
		t.Errorf("charge_id inválido: %q", got.ChargeID)
	}
	if got.ProductID != "23" || got.Value != 650 || got.Currency != "BRL" || got.Status != "approved" {
		t.Errorf("cobrança inesperada: %+v", got)
	}
	if got.CreatedAt != "2026-09-26T12:00:00Z" {
		t.Errorf("created_at = %q", got.CreatedAt)
	}
	if response.Header().Get("Content-Length") == "" {
		t.Error("Content-Length ausente")
	}
	if strings.Contains(response.Body.String(), "\n") {
		t.Error("JSON não está compacto")
	}
}

func TestDeveAceitarProductIDQuandoNumerico(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges", `{"product_id":23,"value":650}`, nil)
	var got charge
	_ = json.Unmarshal(response.Body.Bytes(), &got)
	if response.Code != http.StatusCreated || got.ProductID != "23" {
		t.Fatalf("status = %d, product_id = %q", response.Code, got.ProductID)
	}
}

func TestDeveRecusarPayloadQuandoInvalido(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"JSON quebrado", `{"product_id":`, 400, "invalid_json"},
		{"sem product_id", `{"value":650}`, 422, "invalid_product_id"},
		{"sem value", `{"product_id":"23"}`, 422, "invalid_value"},
		{"value decimal", `{"product_id":"23","value":6.50}`, 422, "invalid_value"},
		{"value texto", `{"product_id":"23","value":"650"}`, 422, "invalid_value"},
		{"value zero", `{"product_id":"23","value":0}`, 422, "invalid_value"},
		{"value negativo", `{"product_id":"23","value":-1}`, 422, "invalid_value"},
		{"value acima do limite", `{"product_id":"23","value":100001}`, 422, "invalid_value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges", test.body, nil)
			if response.Code != test.status || errorCode(t, response) != test.code {
				t.Fatalf("status = %d, código = %q, esperados %d e %q", response.Code, errorCode(t, response), test.status, test.code)
			}
		})
	}
}

func TestDeveRecusarCorpoQuandoExcedeQuatroKB(t *testing.T) {
	body := `{"product_id":"` + strings.Repeat("a", 4096) + `","value":650}`
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges", body, nil)
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "body_too_large" {
		t.Fatalf("status = %d, corpo = %s", response.Code, response.Body.String())
	}
}

func TestDeveSimularRecusaQuandoSolicitado(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges?simulate=declined", `{"product_id":"23","value":650}`, nil)
	var got charge
	_ = json.Unmarshal(response.Body.Bytes(), &got)
	if response.Code != http.StatusCreated || got.Status != "declined" || got.Reason != "card_declined" {
		t.Fatalf("resposta inesperada: %d %s", response.Code, response.Body.String())
	}
}

func TestDeveSimularErroQuandoSolicitado(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges?simulate=error", `{"product_id":"23","value":650}`, nil)
	if response.Code != http.StatusInternalServerError || errorCode(t, response) != "simulated_error" {
		t.Fatalf("resposta inesperada: %d %s", response.Code, response.Body.String())
	}
}

func TestDeveUsarDelayInjetadoQuandoDelayValido(t *testing.T) {
	app := testApplication()
	var received time.Duration
	app.sleep = func(duration time.Duration) { received = duration }
	response := request(t, app.routes(), http.MethodPost, "/v1/charges?delay_ms=1234", `{"product_id":"23","value":650}`, nil)
	if response.Code != http.StatusCreated || received != 1234*time.Millisecond {
		t.Fatalf("status = %d, duração = %s", response.Code, received)
	}
}

func TestDeveRecusarDelayQuandoAcimaDoLimite(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodPost, "/v1/charges?delay_ms=60001", `{"product_id":"23","value":650}`, nil)
	if response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != "invalid_delay" {
		t.Fatalf("resposta inesperada: %d %s", response.Code, response.Body.String())
	}
}

func TestDeveDevolverMesmaCobrancaQuandoChaveRepetida(t *testing.T) {
	handler := testApplication().routes()
	headers := map[string]string{"Idempotency-Key": "teste-123"}
	first := request(t, handler, http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, headers)
	second := request(t, handler, http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, headers)
	var firstCharge, secondCharge charge
	_ = json.Unmarshal(first.Body.Bytes(), &firstCharge)
	_ = json.Unmarshal(second.Body.Bytes(), &secondCharge)
	if first.Code != 201 || second.Code != 200 || firstCharge.ChargeID != secondCharge.ChargeID {
		t.Fatalf("primeira = %d/%q, segunda = %d/%q", first.Code, firstCharge.ChargeID, second.Code, secondCharge.ChargeID)
	}
}

func TestDeveExigirTokenQuandoConfigurado(t *testing.T) {
	app := testApplication()
	app.token = "segredo"
	handler := app.routes()
	unauthorized := request(t, handler, http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, nil)
	authorized := request(t, handler, http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, map[string]string{"Authorization": "Bearer segredo"})
	health := request(t, handler, http.MethodGet, "/health", "", nil)
	if unauthorized.Code != 401 || errorCode(t, unauthorized) != "unauthorized" {
		t.Fatalf("requisição sem token: %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	if authorized.Code != 201 || health.Code != 200 {
		t.Fatalf("com token = %d, health = %d", authorized.Code, health.Code)
	}
}

func TestDeveRegistrarCorpoQuandoAutenticacaoFalha(t *testing.T) {
	var logs bytes.Buffer
	app := newApplication("segredo", slog.New(slog.NewTextHandler(&logs, nil)))
	body := `{"product_id":"23","value":650}`
	response := request(t, app.routes(), http.MethodPost, "/v1/charges", body, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(logs.String(), `product_id`) || !strings.Contains(logs.String(), `value`) {
		t.Fatalf("corpo não encontrado no log: %s", logs.String())
	}
}

func TestDeveBuscarCobrancaPorIDQuandoExistente(t *testing.T) {
	handler := testApplication().routes()
	created := request(t, handler, http.MethodPost, "/v1/charges", `{"product_id":"23","value":650}`, nil)
	var createdCharge charge
	_ = json.Unmarshal(created.Body.Bytes(), &createdCharge)
	found := request(t, handler, http.MethodGet, "/v1/charges/"+createdCharge.ChargeID, "", nil)
	if found.Code != 200 || !strings.Contains(found.Body.String(), createdCharge.ChargeID) {
		t.Fatalf("resposta inesperada: %d %s", found.Code, found.Body.String())
	}
}

func TestDeveListarCobrancasComPaginacaoQuandoExistirem(t *testing.T) {
	handler := testApplication().routes()
	for _, productID := range []string{"1", "2", "3"} {
		body := `{"product_id":"` + productID + `","value":650}`
		created := request(t, handler, http.MethodPost, "/v1/charges", body, nil)
		if created.Code != http.StatusCreated {
			t.Fatalf("falha ao criar cobrança: %d %s", created.Code, created.Body.String())
		}
	}

	response := request(t, handler, http.MethodGet, "/v1/charges?page=2&page_size=2", "", nil)
	var got chargeList
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || got.Page != 2 || got.PageSize != 2 || got.Total != 3 || got.TotalPages != 2 {
		t.Fatalf("paginação inesperada: status=%d resposta=%+v", response.Code, got)
	}
	if len(got.Charges) != 1 || got.Charges[0].ProductID != "3" {
		t.Fatalf("cobranças inesperadas: %+v", got.Charges)
	}
}

func TestDeveListarArrayVazioQuandoNaoExistiremCobrancas(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodGet, "/v1/charges", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"charges":[]`) || !strings.Contains(response.Body.String(), `"page_size":20`) {
		t.Fatalf("resposta inesperada: %s", response.Body.String())
	}
}

func TestDeveRecusarPaginacaoQuandoInvalida(t *testing.T) {
	for _, target := range []string{
		"/v1/charges?page=0",
		"/v1/charges?page=texto",
		"/v1/charges?page_size=0",
		"/v1/charges?page_size=101",
	} {
		t.Run(target, func(t *testing.T) {
			response := request(t, testApplication().routes(), http.MethodGet, target, "", nil)
			if response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != "invalid_pagination" {
				t.Fatalf("resposta inesperada: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeveResponder404QuandoCobrancaNaoExiste(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodGet, "/v1/charges/chg_inexistente", "", nil)
	if response.Code != 404 || errorCode(t, response) != "not_found" || response.Header().Get("Content-Length") == "" {
		t.Fatalf("resposta inesperada: %d %s", response.Code, response.Body.String())
	}
}

func TestDeveServirSwaggerQuandoAcessarDocumentacao(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodGet, "/docs", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(response.Body.String(), "SwaggerUIBundle") {
		t.Fatalf("documentação Swagger inválida: %s", response.Body.String())
	}
	if response.Header().Get("Content-Length") == "" {
		t.Error("Content-Length ausente")
	}
}

func TestDeveServirOpenAPIQuandoAcessarEspecificacao(t *testing.T) {
	response := request(t, testApplication().routes(), http.MethodGet, "/openapi.yaml", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "application/yaml") ||
		!strings.Contains(response.Body.String(), "openapi: 3.0.3") ||
		!strings.Contains(response.Body.String(), "/v1/charges:") {
		t.Fatalf("especificação OpenAPI inválida: %s", response.Body.String())
	}
	if response.Header().Get("Content-Length") == "" {
		t.Error("Content-Length ausente")
	}
}
