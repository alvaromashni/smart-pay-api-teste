package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func creditApp() (http.Handler, *clock) {
	app := testApplication()
	c := &clock{now: time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)}
	app.now = c.Now
	return app.routes(), c
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("JSON inválido: %v; corpo: %s", err, response.Body.String())
	}
	return value
}

func credit(t *testing.T, h http.Handler, id string) creditView {
	t.Helper()
	response := request(t, h, http.MethodGet, "/v1/machines/"+id+"/credit", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET credit: status %d; corpo %s", response.Code, response.Body.String())
	}
	return decode[creditView](t, response)
}

func load(t *testing.T, h http.Handler, id string, value string) {
	t.Helper()
	response := request(t, h, http.MethodPut, "/v1/machines/"+id+"/credit", `{"value":`+value+`}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT credit: status %d; corpo %s", response.Code, response.Body.String())
	}
}

func buy(t *testing.T, h http.Handler, id, value, query string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodPost, "/v1/machines/"+id+"/vends"+query, `{"product_id":"23","value":`+value+`}`, headers)
}

func finish(t *testing.T, h http.Handler, vendID, result string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodPost, "/v1/vends/"+vendID+"/result", `{"result":"`+result+`"}`, nil)
}

func TestDeveInformarSemCreditoQuandoMaquinaNova(t *testing.T) {
	h, _ := creditApp()
	got := credit(t, h, "99999")
	if got.MachineID != "99999" || got.Available || got.Value != 0 || got.Currency != "BRL" {
		t.Fatalf("crédito inesperado: %+v", got)
	}
}

func TestDeveInformarCreditoQuandoCarregado(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "500")
	got := credit(t, h, "99999")
	if !got.Available || got.Value != 500 || got.UpdatedAt != "2026-10-05T13:00:00Z" {
		t.Fatalf("crédito inesperado: %+v", got)
	}
	if other := credit(t, h, "12345"); other.Value != 0 {
		t.Fatalf("crédito vazou para outra máquina: %+v", other)
	}
}

func TestDeveAutorizarEConsumirQuandoCreditoSuficiente(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "500")

	response := buy(t, h, "99999", "350", "", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("status %d; corpo %s", response.Code, response.Body.String())
	}
	v := decode[vend](t, response)
	if v.Status != vendAuthorized || v.CreditAfter != 150 || !strings.HasPrefix(v.VendID, "vnd_") {
		t.Fatalf("venda inesperada: %+v", v)
	}
	if got := credit(t, h, "99999"); got.Value != 150 || got.Held != 350 {
		t.Fatalf("reserva não refletida: %+v", got)
	}

	done := decode[vend](t, finish(t, h, v.VendID, "success"))
	if done.Status != vendCompleted {
		t.Fatalf("status final = %s", done.Status)
	}
	if got := credit(t, h, "99999"); got.Value != 150 || got.Held != 0 {
		t.Fatalf("saldo após venda: %+v", got)
	}
}

func TestDeveNegarSemReservarQuandoCreditoInsuficiente(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "150")
	response := buy(t, h, "99999", "350", "", nil)
	if response.Code != http.StatusPaymentRequired || errorCode(t, response) != "insufficient_credit" {
		t.Fatalf("status %d; corpo %s", response.Code, response.Body.String())
	}
	if got := credit(t, h, "99999"); got.Value != 150 || got.Held != 0 {
		t.Fatalf("saldo mudou numa venda negada: %+v", got)
	}
}

func TestDeveDevolverCreditoQuandoDispensaFalha(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "500")
	v := decode[vend](t, buy(t, h, "99999", "350", "", nil))
	refunded := decode[vend](t, finish(t, h, v.VendID, "failed"))
	if refunded.Status != vendRefunded || refunded.CreditAfter != 500 {
		t.Fatalf("estorno inesperado: %+v", refunded)
	}
	if got := credit(t, h, "99999"); got.Value != 500 || got.Held != 0 {
		t.Fatalf("crédito não voltou: %+v", got)
	}
}

func TestDeveDevolverCreditoQuandoReservaExpira(t *testing.T) {
	h, c := creditApp()
	load(t, h, "99999", "500")
	v := decode[vend](t, buy(t, h, "99999", "350", "?hold_ms=5000", nil))
	c.Advance(5 * time.Second)
	if got := credit(t, h, "99999"); got.Value != 500 || got.Held != 0 {
		t.Fatalf("reserva vencida não devolveu: %+v", got)
	}
	late := finish(t, h, v.VendID, "success")
	if late.Code != http.StatusConflict || errorCode(t, late) != "vend_expired" {
		t.Fatalf("resultado tardio: status %d; corpo %s", late.Code, late.Body.String())
	}
}

func TestDeveDevolverMesmaVendaQuandoChaveRepetida(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "500")
	headers := map[string]string{"Idempotency-Key": "venda-1"}
	first := buy(t, h, "99999", "350", "", headers)
	second := buy(t, h, "99999", "350", "", headers)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("status %d e %d", first.Code, second.Code)
	}
	if decode[vend](t, first).VendID != decode[vend](t, second).VendID {
		t.Fatal("reenvio criou outra venda")
	}
	if got := credit(t, h, "99999"); got.Value != 150 {
		t.Fatalf("reenvio descontou duas vezes: %+v", got)
	}
}

func TestDeveAutorizarSomenteUmaQuandoVendasSimultaneas(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "350")
	const attempts = 50
	var wg sync.WaitGroup
	codes := make(chan int, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- buy(t, h, "99999", "350", "", nil).Code
		}()
	}
	wg.Wait()
	close(codes)
	authorized := 0
	for code := range codes {
		if code == http.StatusCreated {
			authorized++
		} else if code != http.StatusPaymentRequired {
			t.Fatalf("status inesperado %d", code)
		}
	}
	if authorized != 1 {
		t.Fatalf("%d vendas autorizadas com crédito para uma", authorized)
	}
}

func TestDeveSerIdempotenteQuandoResultadoRepetido(t *testing.T) {
	h, _ := creditApp()
	load(t, h, "99999", "500")
	v := decode[vend](t, buy(t, h, "99999", "350", "", nil))
	if finish(t, h, v.VendID, "success").Code != http.StatusOK || finish(t, h, v.VendID, "success").Code != http.StatusOK {
		t.Fatal("reenvio do mesmo resultado deveria responder 200")
	}
	conflict := finish(t, h, v.VendID, "failed")
	if conflict.Code != http.StatusConflict || errorCode(t, conflict) != "vend_already_finished" {
		t.Fatalf("status %d; corpo %s", conflict.Code, conflict.Body.String())
	}
	if got := credit(t, h, "99999"); got.Value != 150 {
		t.Fatalf("saldo mudou: %+v", got)
	}
}

func TestDeveRecusarEntradasQuandoInvalidas(t *testing.T) {
	h, _ := creditApp()
	tests := []struct {
		name, method, target, body, code string
		status                           int
	}{
		{"machine_id inválido", http.MethodGet, "/v1/machines/abc!/credit", "", "invalid_machine_id", 422},
		{"crédito negativo", http.MethodPut, "/v1/machines/99999/credit", `{"value":-1}`, "invalid_value", 422},
		{"crédito decimal", http.MethodPut, "/v1/machines/99999/credit", `{"value":5.5}`, "invalid_value", 422},
		{"campo extra", http.MethodPut, "/v1/machines/99999/credit", `{"value":5,"x":1}`, "invalid_json", 400},
		{"hold inválido", http.MethodPost, "/v1/machines/99999/vends?hold_ms=10", `{"product_id":"23","value":350}`, "invalid_hold", 422},
		{"resultado inválido", http.MethodPost, "/v1/vends/vnd_x/result", `{"result":"ok"}`, "invalid_result", 422},
		{"venda inexistente", http.MethodPost, "/v1/vends/vnd_x/result", `{"result":"success"}`, "not_found", 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, h, tt.method, tt.target, tt.body, nil)
			if response.Code != tt.status || errorCode(t, response) != tt.code {
				t.Fatalf("status %d; corpo %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeveExigirTokenQuandoConsultaCredito(t *testing.T) {
	app := testApplication()
	app.token = "segredo"
	response := request(t, app.routes(), http.MethodGet, "/v1/machines/99999/credit", "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", response.Code)
	}
}
