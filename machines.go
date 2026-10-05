package main

// Crédito por máquina (fictício, em memória).
//
// Fluxo pensado para a PCB:
//  1. Um pagamento finalizado carrega crédito na máquina (PUT .../credit simula isso).
//  2. O ESP32 consulta periodicamente GET .../credit e vê se há crédito e quanto.
//  3. Com o produto escolhido, o ESP32 pede a venda (POST .../vends). A API reserva
//     o valor de forma atômica: duas vendas nunca usam o mesmo crédito.
//  4. Depois de tentar dispensar, o ESP32 informa o resultado (POST /v1/vends/{id}/result):
//     success consome a reserva; failed devolve o crédito.
//  5. Reserva sem resultado expira e o crédito volta sozinho.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	maxCreditCents = 100_000
	defaultHold    = 60 * time.Second
	minHold        = 1 * time.Second
	maxHold        = 10 * time.Minute
	vendAuthorized = "authorized"
	vendCompleted  = "completed"
	vendRefunded   = "refunded"
	vendExpired    = "expired"
	resultSuccess  = "success"
	resultFailed   = "failed"
)

var machineIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type machineState struct {
	available int64 // crédito livre para novas vendas, em centavos
	held      int64 // soma das reservas em aberto
	updatedAt time.Time
}

type vend struct {
	VendID      string `json:"vend_id"`
	MachineID   string `json:"machine_id"`
	ProductID   string `json:"product_id"`
	Value       int64  `json:"value"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	CreditAfter int64  `json:"credit_remaining"`
	expires     time.Time
}

type creditView struct {
	MachineID string `json:"machine_id"`
	Available bool   `json:"available"`
	Value     int64  `json:"value"`
	Held      int64  `json:"held"`
	Currency  string `json:"currency"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type creditStore struct {
	mu       sync.Mutex
	machines map[string]*machineState
	vends    map[string]*vend
	vendKeys map[string]string // machine_id + Idempotency-Key -> vend_id
}

func newCreditStore() *creditStore {
	return &creditStore{
		machines: make(map[string]*machineState),
		vends:    make(map[string]*vend),
		vendKeys: make(map[string]string),
	}
}

// machine devolve o estado da máquina, criando-o zerado se ainda não existir.
// Deve ser chamado com mu travado.
func (store *creditStore) machine(id string) *machineState {
	state, exists := store.machines[id]
	if !exists {
		state = &machineState{}
		store.machines[id] = state
	}
	return state
}

// expire devolve o crédito das reservas vencidas. Deve ser chamado com mu travado.
func (store *creditStore) expire(now time.Time) {
	for _, current := range store.vends {
		if current.Status == vendAuthorized && !now.Before(current.expires) {
			state := store.machine(current.MachineID)
			state.held -= current.Value
			state.available += current.Value
			state.updatedAt = now
			current.Status = vendExpired
			current.FinishedAt = now.UTC().Format(time.RFC3339)
			current.CreditAfter = state.available
		}
	}
}

func viewOf(id string, state *machineState) creditView {
	view := creditView{
		MachineID: id,
		Available: state.available > 0,
		Value:     state.available,
		Held:      state.held,
		Currency:  "BRL",
	}
	if !state.updatedAt.IsZero() {
		view.UpdatedAt = state.updatedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func (app *application) machineID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("machine_id")
	if !machineIDPattern.MatchString(id) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_machine_id", "machine_id deve ter de 1 a 32 caracteres: letras, números, - ou _")
		return "", false
	}
	return id, true
}

// GET /v1/machines/{machine_id}/credit
func (app *application) getCredit(w http.ResponseWriter, r *http.Request) {
	id, ok := app.machineID(w, r)
	if !ok {
		return
	}
	store := app.credit
	store.mu.Lock()
	store.expire(app.now())
	view := viewOf(id, store.machine(id))
	store.mu.Unlock()
	writeJSON(w, http.StatusOK, view)
}

// PUT /v1/machines/{machine_id}/credit — simula um pagamento finalizado.
// Define o crédito livre da máquina (não mexe nas reservas em aberto).
func (app *application) setCredit(w http.ResponseWriter, r *http.Request) {
	id, ok := app.machineID(w, r)
	if !ok {
		return
	}
	var payload struct {
		Value json.RawMessage `json:"value"`
	}
	if requestErr := decodeStrict(r.Body, &payload); requestErr != nil {
		writeError(w, requestErr.status, requestErr.code, requestErr.message)
		return
	}
	value, valid := parseCredit(payload.Value)
	if !valid {
		writeError(w, http.StatusUnprocessableEntity, "invalid_value", "value deve ser inteiro em centavos entre 0 e 100000 (ex.: 500 = R$ 5,00)")
		return
	}
	store := app.credit
	now := app.now()
	store.mu.Lock()
	store.expire(now)
	state := store.machine(id)
	state.available = value
	state.updatedAt = now
	view := viewOf(id, state)
	store.mu.Unlock()
	writeJSON(w, http.StatusOK, view)
}

// POST /v1/machines/{machine_id}/vends — reserva o valor do produto escolhido.
func (app *application) createVend(w http.ResponseWriter, r *http.Request) {
	id, ok := app.machineID(w, r)
	if !ok {
		return
	}
	hold, ok := parseHold(r)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_hold", "hold_ms deve ser um inteiro entre 1000 e 600000")
		return
	}
	productID, value, requestErr := decodeChargeRequest(r.Body)
	if requestErr != nil {
		writeError(w, requestErr.status, requestErr.code, requestErr.message)
		return
	}

	store := app.credit
	now := app.now()
	key := r.Header.Get("Idempotency-Key")
	store.mu.Lock()
	defer store.mu.Unlock()
	store.expire(now)

	if key != "" {
		if vendID, exists := store.vendKeys[id+"\x00"+key]; exists {
			writeJSON(w, http.StatusOK, store.vends[vendID])
			return
		}
	}

	state := store.machine(id)
	if state.available < value {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":      "insufficient_credit",
			"message":    "crédito insuficiente para este produto",
			"machine_id": id,
			"value":      state.available,
			"required":   value,
		})
		return
	}

	vendID, err := newID("vnd_")
	if err != nil {
		app.logger.Error("falha ao gerar identificador", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "erro interno")
		return
	}
	state.available -= value
	state.held += value
	state.updatedAt = now
	created := &vend{
		VendID:      vendID,
		MachineID:   id,
		ProductID:   productID,
		Value:       value,
		Status:      vendAuthorized,
		CreatedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:   now.Add(hold).UTC().Format(time.RFC3339),
		CreditAfter: state.available,
		expires:     now.Add(hold),
	}
	store.vends[vendID] = created
	if key != "" {
		store.vendKeys[id+"\x00"+key] = vendID
	}
	writeJSON(w, http.StatusCreated, created)
}

// GET /v1/vends/{vend_id}
func (app *application) getVend(w http.ResponseWriter, r *http.Request) {
	store := app.credit
	store.mu.Lock()
	store.expire(app.now())
	found, exists := store.vends[r.PathValue("vend_id")]
	var copyOf vend
	if exists {
		copyOf = *found
	}
	store.mu.Unlock()
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "venda não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, copyOf)
}

// POST /v1/vends/{vend_id}/result — {"result":"success"|"failed"}
func (app *application) vendResult(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Result string `json:"result"`
	}
	if requestErr := decodeStrict(r.Body, &payload); requestErr != nil {
		writeError(w, requestErr.status, requestErr.code, requestErr.message)
		return
	}
	if payload.Result != resultSuccess && payload.Result != resultFailed {
		writeError(w, http.StatusUnprocessableEntity, "invalid_result", "result deve ser success ou failed")
		return
	}

	store := app.credit
	now := app.now()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.expire(now)
	current, exists := store.vends[r.PathValue("vend_id")]
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "venda não encontrada")
		return
	}

	wanted := vendCompleted
	if payload.Result == resultFailed {
		wanted = vendRefunded
	}
	switch current.Status {
	case wanted:
		// Reenvio do mesmo resultado (ex.: a resposta anterior se perdeu): idempotente.
		writeJSON(w, http.StatusOK, current)
		return
	case vendExpired:
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "vend_expired",
			"message": "a reserva expirou e o crédito já foi devolvido",
			"vend":    current,
		})
		return
	case vendCompleted, vendRefunded:
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "vend_already_finished",
			"message": "a venda já foi finalizada com outro resultado",
			"vend":    current,
		})
		return
	}

	state := store.machine(current.MachineID)
	state.held -= current.Value
	if wanted == vendRefunded {
		state.available += current.Value
	}
	state.updatedAt = now
	current.Status = wanted
	current.FinishedAt = now.UTC().Format(time.RFC3339)
	current.CreditAfter = state.available
	writeJSON(w, http.StatusOK, current)
}

func decodeStrict(body io.Reader, target any) *requestError {
	data, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return &requestError{http.StatusBadRequest, "invalid_json", "não foi possível ler o corpo da requisição"}
	}
	if len(data) > maxBodyBytes {
		return &requestError{http.StatusBadRequest, "body_too_large", "o corpo da requisição excede o limite de 4 KB"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &requestError{http.StatusBadRequest, "invalid_json", "o corpo deve conter um JSON válido"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return &requestError{http.StatusBadRequest, "invalid_json", "o corpo deve conter somente um objeto JSON"}
	}
	return nil
}

func parseCredit(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || !isInteger(string(raw)) {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil && value >= 0 && value <= maxCreditCents
}

func parseHold(r *http.Request) (time.Duration, bool) {
	values, present := r.URL.Query()["hold_ms"]
	if !present {
		return defaultHold, true
	}
	if len(values) != 1 {
		return 0, false
	}
	milliseconds, err := strconv.Atoi(values[0])
	hold := time.Duration(milliseconds) * time.Millisecond
	if err != nil || hold < minHold || hold > maxHold {
		return 0, false
	}
	return hold, true
}

func newID(prefix string) (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}
